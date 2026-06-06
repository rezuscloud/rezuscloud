// Package backup implements S3-compatible backup for management plane state.
// It periodically snapshots the SQLite database and exports CRD resources
// to S3-compatible storage (AWS S3, MinIO, Cloudflare R2, etc.).
package backup

import (
	"context"
	"fmt"
	"io"
	"time"
)

// Store is the S3-compatible storage backend.
type Store interface {
	// Upload writes data to the given key in the backup bucket.
	Upload(ctx context.Context, key string, data io.Reader) error
	// Download reads data from the given key.
	Download(ctx context.Context, key string) (io.ReadCloser, error)
	// Delete removes the given key.
	Delete(ctx context.Context, key string) error
}

// Snapshot represents a backup snapshot.
type Snapshot struct {
	ID        string    `json:"id"`
	Timestamp time.Time `json:"timestamp"`
	Size      int64     `json:"size"`
	Type      string    `json:"type"` // "database" or "resources"
	Key       string    `json:"key"`  // S3 object key
}

// Config configures the backup manager.
type Config struct {
	// Bucket is the S3 bucket name.
	Bucket string
	// Prefix is the key prefix for backup objects.
	Prefix string
	// Interval is the time between automatic backups.
	// Zero means manual-only.
	Interval time.Duration
	// Retention is how many snapshots to keep.
	Retention int
}

// Manager manages backup operations.
type Manager struct {
	store  Store
	config Config
}

// NewManager creates a backup manager.
func NewManager(store Store, config Config) *Manager {
	if config.Prefix == "" {
		config.Prefix = "backups"
	}
	if config.Retention == 0 {
		config.Retention = 7
	}
	return &Manager{store: store, config: config}
}

// BackupDatabase creates a backup of the SQLite database.
// The caller provides the database content as a reader.
func (m *Manager) BackupDatabase(ctx context.Context, data io.Reader, size int64) (*Snapshot, error) {
	now := time.Now().UTC()
	key := fmt.Sprintf("%s/%s/database.db", m.config.Prefix, now.Format("2006-01-02T15-04-05"))

	if err := m.store.Upload(ctx, key, data); err != nil {
		return nil, fmt.Errorf("upload database: %w", err)
	}

	snap := &Snapshot{
		ID:        fmt.Sprintf("db-%d", now.Unix()),
		Timestamp: now,
		Size:      size,
		Type:      "database",
		Key:       key,
	}

	return snap, nil
}

// BackupResources creates a backup of CRD resources as JSON.
func (m *Manager) BackupResources(ctx context.Context, data io.Reader, size int64) (*Snapshot, error) {
	now := time.Now().UTC()
	key := fmt.Sprintf("%s/%s/resources.json", m.config.Prefix, now.Format("2006-01-02T15-04-05"))

	if err := m.store.Upload(ctx, key, data); err != nil {
		return nil, fmt.Errorf("upload resources: %w", err)
	}

	snap := &Snapshot{
		ID:        fmt.Sprintf("res-%d", now.Unix()),
		Timestamp: now,
		Size:      size,
		Type:      "resources",
		Key:       key,
	}

	return snap, nil
}

// SnapshotKey generates the S3 key for a snapshot at the given time.
func SnapshotKey(prefix, snapshotType string, t time.Time) string {
	ext := ".json"
	if snapshotType == "database" {
		ext = ".db"
	}
	return fmt.Sprintf("%s/%s/%s%s", prefix, t.Format("2006-01-02"), snapshotType, ext)
}
