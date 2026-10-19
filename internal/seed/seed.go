// Package seed creates buckets and uploads fixture files at startup and on reset.
package seed

import (
	"context"
	"errors"
	"fmt"
	"io/fs"
	"mime"
	"os"
	"path"
	"path/filepath"
	"slices"
	"strings"

	"github.com/LeoMachado94/s3lab/internal/storage"
)

type Seeder struct {
	backend storage.Backend
	buckets []string
	dir     string
}

func New(b storage.Backend, buckets []string, dir string) *Seeder {
	return &Seeder{backend: b, buckets: buckets, dir: dir}
}

// Apply creates every configured bucket and uploads the seed directory.
func (s *Seeder) Apply(ctx context.Context) error { return s.apply(ctx, "") }

// ApplyBucket seeds only bucket; it is a no-op when the seed does not define it.
func (s *Seeder) ApplyBucket(ctx context.Context, bucket string) error {
	return s.apply(ctx, bucket)
}

func (s *Seeder) apply(ctx context.Context, only string) error {
	for _, name := range s.buckets {
		if only != "" && name != only {
			continue
		}
		if err := s.ensureBucket(ctx, name); err != nil {
			return err
		}
	}
	if s.dir == "" {
		return nil
	}
	entries, err := os.ReadDir(s.dir)
	if err != nil {
		return fmt.Errorf("seed dir: %w", err)
	}
	for _, e := range entries {
		name := e.Name()
		if only != "" && name != only {
			continue
		}
		root := filepath.Join(s.dir, name)
		if info, err := os.Stat(root); err != nil || !info.IsDir() {
			continue
		}
		if err := storage.ValidateBucketName(name); err != nil {
			return fmt.Errorf("seed dir %q: %w", name, err)
		}
		if err := s.ensureBucket(ctx, name); err != nil {
			return err
		}
		if err := s.uploadTree(ctx, name, root); err != nil {
			return err
		}
	}
	return nil
}

func (s *Seeder) ensureBucket(ctx context.Context, name string) error {
	err := s.backend.CreateBucket(ctx, name)
	if err != nil && !errors.Is(err, storage.ErrBucketAlreadyOwnedByYou) {
		return fmt.Errorf("seed bucket %q: %w", name, err)
	}
	return nil
}

func (s *Seeder) uploadTree(ctx context.Context, bucket, root string) error {
	realRoot, err := filepath.EvalSymlinks(root)
	if err != nil {
		return fmt.Errorf("seed bucket %q: %w", bucket, err)
	}
	return filepath.WalkDir(root, func(p string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if d.IsDir() || slices.Contains([]string{".DS_Store", "Thumbs.db"}, d.Name()) {
			return nil
		}
		real, err := filepath.EvalSymlinks(p)
		if err != nil || !strings.HasPrefix(real, realRoot+string(filepath.Separator)) {
			return nil
		}
		info, err := os.Stat(real)
		if err != nil || !info.Mode().IsRegular() {
			return nil
		}
		rel, err := filepath.Rel(root, p)
		if err != nil {
			return err
		}
		return s.upload(ctx, bucket, filepath.ToSlash(rel), real, info.Size())
	})
}

func (s *Seeder) upload(ctx context.Context, bucket, key, file string, size int64) error {
	f, err := os.Open(file)
	if err != nil {
		return err
	}
	defer func() { _ = f.Close() }()
	ct := mime.TypeByExtension(path.Ext(key))
	if ct == "" {
		ct = "application/octet-stream"
	}
	_, err = s.backend.PutObject(ctx, bucket, key, f, storage.PutOptions{Size: size, Meta: storage.ObjectMeta{ContentType: ct}})
	if err != nil {
		return fmt.Errorf("seed %s/%s: %w", bucket, key, err)
	}
	return nil
}
