package console

import (
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"strconv"
	"time"

	"github.com/LeoMachado94/s3lab/internal/faults"
	"github.com/LeoMachado94/s3lab/internal/reqlog"
)

type entryView struct {
	reqlog.Entry
	Time string `json:"time"`
}

func viewEntry(e reqlog.Entry) entryView { return entryView{Entry: e, Time: ts(e.Time)} }

func parseFilter(r *http.Request) (reqlog.Filter, error) {
	q := r.URL.Query()
	f := reqlog.Filter{
		Bucket:     q.Get("bucket"),
		Op:         q.Get("op"),
		KeyPrefix:  q.Get("key"),
		ErrorsOnly: q.Get("errors") == "true",
	}
	if v := q.Get("status"); v != "" {
		n, err := strconv.Atoi(v)
		if err != nil || n < 0 {
			return f, errors.New("invalid status")
		}
		f.Status = n
	}
	if v := q.Get("since"); v != "" {
		n, err := strconv.ParseUint(v, 10, 64)
		if err != nil {
			return f, errors.New("invalid since")
		}
		f.SinceID = n
	}
	if v := q.Get("limit"); v != "" {
		n, err := strconv.Atoi(v)
		if err != nil || n < 0 {
			return f, errors.New("invalid limit")
		}
		f.Limit = n
	}
	return f, nil
}

func (s *server) listRequests(w http.ResponseWriter, r *http.Request) {
	f, err := parseFilter(r)
	if err != nil {
		writeError(w, http.StatusBadRequest, "BadRequest", err.Error())
		return
	}
	entries := s.Requests.List(f)
	out := make([]entryView, len(entries))
	for i, e := range entries {
		out[i] = viewEntry(e)
	}
	writeJSON(w, http.StatusOK, map[string]any{"entries": out})
}

func (s *server) clearRequests(w http.ResponseWriter, _ *http.Request) {
	s.Requests.Clear()
	w.WriteHeader(http.StatusNoContent)
}

func (s *server) streamRequests(w http.ResponseWriter, r *http.Request) {
	f, err := parseFilter(r)
	if err != nil {
		writeError(w, http.StatusBadRequest, "BadRequest", err.Error())
		return
	}
	f.Limit = 0
	if v := r.Header.Get("Last-Event-ID"); v != "" {
		if n, err := strconv.ParseUint(v, 10, 64); err == nil {
			f.SinceID = n
		}
	}

	// Subscribe before replaying so no entry falls between the two.
	live, cancel := s.Requests.Subscribe(256)
	defer cancel()

	h := w.Header()
	h.Set("Content-Type", "text/event-stream")
	h.Set("Cache-Control", "no-cache")
	h.Set("X-Accel-Buffering", "no")
	w.WriteHeader(http.StatusOK)
	rc := http.NewResponseController(w)
	if rc.Flush() != nil {
		return
	}

	lastID := f.SinceID
	send := func(e reqlog.Entry) bool {
		data, err := json.Marshal(viewEntry(e))
		if err != nil {
			return true
		}
		if _, err := fmt.Fprintf(w, "event: request\nid: %d\ndata: %s\n\n", e.ID, data); err != nil {
			return false
		}
		lastID = e.ID
		return rc.Flush() == nil
	}
	for _, e := range s.Requests.List(f) {
		if !send(e) {
			return
		}
	}

	ping := time.NewTicker(s.pingInterval)
	defer ping.Stop()
	for {
		select {
		case <-r.Context().Done():
			return
		case <-s.Done:
			return
		case e, ok := <-live:
			if !ok {
				return
			}
			if e.ID <= lastID || !f.Match(e) {
				continue
			}
			if !send(e) {
				return
			}
		case <-ping.C:
			if _, err := fmt.Fprint(w, ": ping\n\n"); err != nil || rc.Flush() != nil {
				return
			}
		}
	}
}

func (s *server) listFaults(w http.ResponseWriter, _ *http.Request) {
	writeJSON(w, http.StatusOK, map[string]any{"rules": s.Faults.List()})
}

func (s *server) addFault(w http.ResponseWriter, r *http.Request) {
	var rule faults.Rule
	if !decodeBody(w, r, &rule) {
		return
	}
	saved, err := s.Faults.Add(rule)
	switch {
	case errors.Is(err, faults.ErrDuplicateRule):
		writeError(w, http.StatusConflict, "Conflict", err.Error())
	case errors.Is(err, faults.ErrInvalidRule):
		writeError(w, http.StatusBadRequest, "BadRequest", err.Error())
	case err != nil:
		s.storageError(w, err)
	default:
		writeJSON(w, http.StatusCreated, saved)
	}
}

func (s *server) deleteFault(w http.ResponseWriter, r *http.Request) {
	id := r.PathValue("id")
	if !s.Faults.Remove(id) {
		writeError(w, http.StatusNotFound, "NotFound", fmt.Sprintf("fault rule %q not found", id))
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

func (s *server) clearFaults(w http.ResponseWriter, _ *http.Request) {
	s.Faults.Clear()
	w.WriteHeader(http.StatusNoContent)
}

func (s *server) reset(w http.ResponseWriter, r *http.Request) {
	q := r.URL.Query()
	bucket := q.Get("bucket")
	s.mu.Lock()
	defer s.mu.Unlock()
	if err := s.Backend.Reset(r.Context(), bucket); err != nil {
		s.storageError(w, err)
		return
	}
	if s.Seeder != nil {
		var err error
		if bucket == "" {
			err = s.Seeder.Apply(r.Context())
		} else {
			err = s.Seeder.ApplyBucket(r.Context(), bucket)
		}
		if err != nil {
			s.Logger.Error("console reseed failed", "err", err)
			writeError(w, http.StatusInternalServerError, "InternalError", "store was reset but seeding failed (partial): "+err.Error())
			return
		}
	}
	if q.Get("requests") == "true" {
		s.Requests.Clear()
	}
	if q.Get("faults") == "true" {
		s.Faults.Clear()
	}
	w.WriteHeader(http.StatusNoContent)
}
