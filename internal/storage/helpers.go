package storage

import (
	"crypto/md5"
	"encoding/hex"
	"fmt"
	"net"
	"sort"
	"strings"
	"unicode/utf8"
)

// ValidateBucketName applies the S3 general purpose bucket naming rules.
func ValidateBucketName(name string) error {
	if len(name) < 3 || len(name) > 63 {
		return ErrInvalidBucketName
	}
	for i := 0; i < len(name); i++ {
		c := name[i]
		isLower := c >= 'a' && c <= 'z'
		isDigit := c >= '0' && c <= '9'
		if !isLower && !isDigit && c != '-' && c != '.' {
			return ErrInvalidBucketName
		}
	}
	first, last := name[0], name[len(name)-1]
	if first == '-' || first == '.' || last == '-' || last == '.' {
		return ErrInvalidBucketName
	}
	if strings.Contains(name, "..") || strings.Contains(name, ".-") || strings.Contains(name, "-.") {
		return ErrInvalidBucketName
	}
	if net.ParseIP(name) != nil {
		return ErrInvalidBucketName
	}
	return nil
}

// ValidateKey rejects keys S3 would reject. Keys such as "../a" or "a//b"
// are valid S3 keys: filesystem backends must encode them safely.
func ValidateKey(key string) error {
	if key == "" || !utf8.ValidString(key) {
		return ErrInvalidKey
	}
	if len(key) > MaxKeyBytes {
		return ErrKeyTooLong
	}
	return nil
}

// ByteRange is an HTTP byte range before resolution against the object size.
//
//	bytes=10-20 -> {Start: 10, End: 20}
//	bytes=10-   -> {Start: 10, End: -1}
//	bytes=-5    -> {Start: -5, End: -1} (suffix: last 5 bytes)
type ByteRange struct {
	Start int64
	End   int64
}

// Resolve returns the absolute offset and length for an object of size bytes.
func (r ByteRange) Resolve(size int64) (offset, length int64, err error) {
	if r.Start < 0 {
		n := -r.Start
		if n == 0 || size == 0 {
			return 0, 0, ErrInvalidRange
		}
		if n > size {
			n = size
		}
		return size - n, n, nil
	}
	if r.Start >= size {
		return 0, 0, ErrInvalidRange
	}
	end := r.End
	if end < 0 || end >= size {
		end = size - 1
	}
	if end < r.Start {
		return 0, 0, ErrInvalidRange
	}
	return r.Start, end - r.Start + 1, nil
}

// MultipartETag computes the S3 multipart ETag: md5 of concatenated binary
// part md5s, suffixed by "-<parts>".
func MultipartETag(partETags []string) (string, error) {
	h := md5.New()
	for _, e := range partETags {
		b, err := hex.DecodeString(strings.Trim(e, `"`))
		if err != nil {
			return "", fmt.Errorf("%w: bad etag %q", ErrInvalidPart, e)
		}
		h.Write(b)
	}
	return fmt.Sprintf("%s-%d", hex.EncodeToString(h.Sum(nil)), len(partETags)), nil
}

// ValidateCompleteParts checks ordering and that every listed part exists
// with a matching ETag and minimum size. uploaded maps part number to info.
func ValidateCompleteParts(parts []CompletePart, uploaded map[int]PartInfo) error {
	if len(parts) == 0 {
		return ErrInvalidPart
	}
	for i := 1; i < len(parts); i++ {
		if parts[i].PartNumber <= parts[i-1].PartNumber {
			return ErrInvalidPartOrder
		}
	}
	for i, p := range parts {
		info, ok := uploaded[p.PartNumber]
		if !ok || strings.Trim(p.ETag, `"`) != info.ETag {
			return ErrInvalidPart
		}
		if i < len(parts)-1 && info.Size < MinPartSize {
			return ErrEntityTooSmall
		}
	}
	return nil
}

// ListFromSorted applies S3 listing semantics (prefix, delimiter, start-after,
// max-keys) to objects sorted ascending by key. Backends that can produce a
// full sorted slice should use it to keep listing behaviour identical.
func ListFromSorted(objs []ObjectInfo, opts ListOptions) ListResult {
	maxKeys := opts.MaxKeys
	if maxKeys <= 0 {
		maxKeys = DefaultMaxKeys
	}
	start := sort.Search(len(objs), func(i int) bool { return objs[i].Key > opts.StartAfter })

	var res ListResult
	count := 0
	lastPrefix := ""
	for i := start; i < len(objs); i++ {
		key := objs[i].Key
		if !strings.HasPrefix(key, opts.Prefix) {
			if key > opts.Prefix {
				break
			}
			continue
		}
		if opts.Delimiter != "" {
			rest := key[len(opts.Prefix):]
			if idx := strings.Index(rest, opts.Delimiter); idx >= 0 {
				cp := opts.Prefix + rest[:idx+len(opts.Delimiter)]
				// Skip prefixes already returned in this page or a previous one.
				if cp == lastPrefix || cp <= opts.StartAfter {
					continue
				}
				if count == maxKeys {
					res.IsTruncated = true
					break
				}
				res.CommonPrefixes = append(res.CommonPrefixes, cp)
				lastPrefix = cp
				res.NextStartAfter = cp
				count++
				continue
			}
		}
		if count == maxKeys {
			res.IsTruncated = true
			break
		}
		res.Objects = append(res.Objects, objs[i])
		res.NextStartAfter = key
		count++
	}
	if !res.IsTruncated {
		res.NextStartAfter = ""
	}
	return res
}
