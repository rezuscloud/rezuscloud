package backup

import (
	"context"
	"fmt"
	"io"
	"os"
	"path/filepath"
)

// FileStore stores backup objects on the local filesystem.
type FileStore struct {
	root string
}

// NewFileStore creates a local filesystem backup store.
func NewFileStore(root string) (*FileStore, error) {
	if root == "" {
		return nil, fmt.Errorf("backup root is required")
	}
	if err := os.MkdirAll(root, 0o755); err != nil {
		return nil, fmt.Errorf("create backup root: %w", err)
	}
	return &FileStore{root: root}, nil
}

func (s *FileStore) path(key string) string {
	return filepath.Join(s.root, filepath.Clean(key))
}

// Upload writes data into the backup store.
func (s *FileStore) Upload(_ context.Context, key string, data io.Reader) error {
	path := s.path(key)
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return fmt.Errorf("create backup dir: %w", err)
	}
	f, err := os.Create(path)
	if err != nil {
		return fmt.Errorf("create backup file: %w", err)
	}
	defer f.Close()
	if _, err := io.Copy(f, data); err != nil {
		return fmt.Errorf("write backup file: %w", err)
	}
	return nil
}

// Download reads a backup object.
func (s *FileStore) Download(_ context.Context, key string) (io.ReadCloser, error) {
	f, err := os.Open(s.path(key))
	if err != nil {
		return nil, fmt.Errorf("open backup file: %w", err)
	}
	return f, nil
}

// Delete removes a backup object.
func (s *FileStore) Delete(_ context.Context, key string) error {
	if err := os.Remove(s.path(key)); err != nil && !os.IsNotExist(err) {
		return fmt.Errorf("delete backup file: %w", err)
	}
	return nil
}
