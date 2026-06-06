package backup

import (
	"fmt"
	"os"
	"path/filepath"

	"github.com/rezuscloud/rezuscloud/internal/state"
)

// Component bundles the backup subsystem's runtime pieces so main.go can
// construct them once and pass them wherever needed.
//
// The component owns:
//   - Manager — the storage backend adapter (FileStore by default) and retention config
//   - Service  — the high-level trigger/list/restore surface used by both API and WebUI
//   - API      — the HTTP handlers for /api/v1/backups/*
//
// Lifecycle: construct once at startup; pass to api.Router and web.Handler.
// The component does not own a goroutine; backups are user-triggered.
type Component struct {
	Manager *Manager
	Service *Service
	API     *API
}

// ComponentOptions configures the backup component.
type ComponentOptions struct {
	// Root is the directory holding backup artifacts.
	// If empty, defaults to $TMPDIR/rezuscloud-backups (or /tmp/... on Linux).
	Root string
	// Prefix is the key prefix used inside the backup store.
	// If empty, defaults to "backups".
	Prefix string
}

// NewComponent constructs a backup subsystem rooted at opts.Root.
// Returns (nil, err) if the root cannot be created — callers should treat
// a nil Component as "backups disabled" and surface 503 to the user,
// which matches the prior behaviour.
func NewComponent(store *state.Store, opts ComponentOptions) (*Component, error) {
	root := opts.Root
	if root == "" {
		root = filepath.Join(os.TempDir(), "rezuscloud-backups")
	}
	fs, err := NewFileStore(root)
	if err != nil {
		return nil, fmt.Errorf("backup store: %w", err)
	}
	prefix := opts.Prefix
	if prefix == "" {
		prefix = "backups"
	}
	mgr := NewManager(fs, Config{Prefix: prefix})
	svc := NewService(mgr, store)
	api := NewAPI(svc)
	return &Component{Manager: mgr, Service: svc, API: api}, nil
}
