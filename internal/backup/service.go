package backup

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"

	"github.com/rezuscloud/rezuscloud/internal/state"
)

const (
	resourceTypeSnapshot = "backupsnapshot"
	resourceTypePolicy   = "backuppolicy"
	defaultPolicyName    = "default"
)

// SnapshotSpec is the persisted desired metadata for a snapshot.
type SnapshotSpec struct {
	Type      string `json:"type"`
	Key       string `json:"key"`
	Size      int64  `json:"size"`
	Checksum  string `json:"checksum"`
	Duration  int64  `json:"durationMs"`
	StartedAt string `json:"startedAt"`
}

// SnapshotStatus is the persisted observed status for a snapshot.
type SnapshotStatus struct {
	Status string `json:"status"` // success|failed
	Error  string `json:"error,omitempty"`
}

// SnapshotRecord is the API/web representation.
type SnapshotRecord struct {
	ID        string         `json:"id"`
	CreatedAt string         `json:"createdAt"`
	Spec      SnapshotSpec   `json:"spec"`
	Status    SnapshotStatus `json:"status"`
}

// Policy defines retention policy.
type Policy struct {
	Retention int `json:"retention"`
}

// RestoreResult is the output of restore operations.
type RestoreResult struct {
	SnapshotID    string         `json:"snapshotId"`
	DryRun        bool           `json:"dryRun"`
	ResourcesSeen int            `json:"resourcesSeen"`
	Restored      int            `json:"restored"`
	ByType        map[string]int `json:"byType"`
}

// Service performs backup and restore operations.
type Service struct {
	manager *Manager
	store   state.StoreAPI
}

// NewService creates a backup service.
func NewService(manager *Manager, store state.StoreAPI) *Service {
	return &Service{manager: manager, store: store}
}

// TriggerDatabase creates a real SQLite snapshot artifact and persists metadata.
func (s *Service) TriggerDatabase(ctx context.Context) (*SnapshotRecord, error) {
	if s.manager == nil {
		return nil, fmt.Errorf("backup not configured")
	}
	started := time.Now().UTC()
	data, err := s.databaseSnapshot()
	if err != nil {
		s.recordFailure("database", started, err)
		return nil, err
	}
	return s.persistSnapshot(ctx, "database", started, data)
}

// TriggerResources exports resources and persists metadata.
func (s *Service) TriggerResources(ctx context.Context) (*SnapshotRecord, error) {
	if s.manager == nil {
		return nil, fmt.Errorf("backup not configured")
	}
	started := time.Now().UTC()
	data, err := s.resourceSnapshot()
	if err != nil {
		s.recordFailure("resources", started, err)
		return nil, err
	}
	return s.persistSnapshot(ctx, "resources", started, data)
}

func (s *Service) persistSnapshot(ctx context.Context, snapshotType string, started time.Time, data []byte) (*SnapshotRecord, error) {
	duration := time.Since(started)
	checksum := sha256.Sum256(data)
	key := SnapshotKey(s.manager.config.Prefix, snapshotType, started)
	if err := s.manager.store.Upload(ctx, key, bytes.NewReader(data)); err != nil {
		s.recordFailure(snapshotType, started, err)
		return nil, err
	}

	id := fmt.Sprintf("%s-%d", snapshotType, started.UnixNano())
	spec := SnapshotSpec{
		Type:      snapshotType,
		Key:       key,
		Size:      int64(len(data)),
		Checksum:  hex.EncodeToString(checksum[:]),
		Duration:  duration.Milliseconds(),
		StartedAt: started.Format(time.RFC3339),
	}
	status := SnapshotStatus{Status: "success"}
	md, err := s.store.CreateResource(resourceTypeSnapshot, id, spec, status, nil, nil)
	if err != nil {
		return nil, err
	}

	if err := s.enforceRetention(ctx); err != nil {
		return nil, err
	}

	return &SnapshotRecord{ID: md.Name, CreatedAt: md.CreatedAt.Format(time.RFC3339), Spec: spec, Status: status}, nil
}

func (s *Service) recordFailure(snapshotType string, started time.Time, failure error) {
	spec := SnapshotSpec{
		Type:      snapshotType,
		Duration:  time.Since(started).Milliseconds(),
		StartedAt: started.Format(time.RFC3339),
	}
	status := SnapshotStatus{Status: "failed", Error: failure.Error()}
	_, _ = s.store.CreateResource(resourceTypeSnapshot, fmt.Sprintf("%s-%d", snapshotType, started.UnixNano()), spec, status, nil, nil)
}

func (s *Service) databaseSnapshot() ([]byte, error) {
	tmp := filepath.Join(os.TempDir(), fmt.Sprintf("rezuscloud-backup-%d.db", time.Now().UTC().UnixNano()))
	defer func() { _ = os.Remove(tmp) }()
	quoted := strings.ReplaceAll(tmp, "'", "''")
	if _, err := s.store.DB().Exec("VACUUM INTO '" + quoted + "'"); err != nil {
		return nil, fmt.Errorf("vacuum into snapshot: %w", err)
	}
	data, err := os.ReadFile(tmp)
	if err != nil {
		return nil, fmt.Errorf("read sqlite snapshot: %w", err)
	}
	return data, nil
}

type exportEntry struct {
	Type        string            `json:"type"`
	Name        string            `json:"name"`
	Spec        json.RawMessage   `json:"spec"`
	Status      json.RawMessage   `json:"status"`
	Labels      map[string]string `json:"labels,omitempty"`
	Annotations map[string]string `json:"annotations,omitempty"`
}

func (s *Service) resourceSnapshot() ([]byte, error) {
	kinds := []string{"tenant", "nodegroup", "machine", "provider", "configpatch"}
	entries := make([]exportEntry, 0)
	for _, kind := range kinds {
		mds, specs, statuses, _, err := s.store.ListResources(kind, state.ListOptions{})
		if err != nil {
			return nil, fmt.Errorf("list %s: %w", kind, err)
		}
		for i := range mds {
			entries = append(entries, exportEntry{
				Type:        kind,
				Name:        mds[i].Name,
				Spec:        specs[i],
				Status:      statuses[i],
				Labels:      mds[i].Labels,
				Annotations: mds[i].Annotations,
			})
		}
	}
	return json.MarshalIndent(entries, "", "  ")
}

// ListSnapshots returns newest-first snapshots.
func (s *Service) ListSnapshots() ([]SnapshotRecord, error) {
	mds, specs, statuses, _, err := s.store.ListResources(resourceTypeSnapshot, state.ListOptions{})
	if err != nil {
		return nil, err
	}
	out := make([]SnapshotRecord, 0, len(mds))
	for i := range mds {
		var spec SnapshotSpec
		var status SnapshotStatus
		_ = json.Unmarshal(specs[i], &spec)
		_ = json.Unmarshal(statuses[i], &status)
		out = append(out, SnapshotRecord{
			ID:        mds[i].Name,
			CreatedAt: mds[i].CreatedAt.Format(time.RFC3339),
			Spec:      spec,
			Status:    status,
		})
	}
	sort.Slice(out, func(i, j int) bool { return out[i].CreatedAt > out[j].CreatedAt })
	return out, nil
}

// GetPolicy returns current policy, creating defaults on first use.
func (s *Service) GetPolicy() (Policy, error) {
	var spec Policy
	md, err := s.store.GetResource(resourceTypePolicy, defaultPolicyName, &spec, nil)
	if err != nil || md.Name == "" {
		policy := Policy{Retention: 7}
		_, _ = s.store.CreateResource(resourceTypePolicy, defaultPolicyName, policy, map[string]any{}, nil, nil)
		return policy, nil
	}
	if spec.Retention <= 0 {
		spec.Retention = 7
	}
	return spec, nil
}

// UpdatePolicy updates retention policy.
func (s *Service) UpdatePolicy(policy Policy) error {
	if policy.Retention <= 0 {
		return fmt.Errorf("retention must be greater than zero")
	}
	var current Policy
	md, err := s.store.GetResource(resourceTypePolicy, defaultPolicyName, &current, nil)
	if err != nil || md.Name == "" {
		_, err = s.store.CreateResource(resourceTypePolicy, defaultPolicyName, policy, map[string]any{}, nil, nil)
		return err
	}
	_, err = s.store.UpdateResource(resourceTypePolicy, defaultPolicyName, md.ResourceVersion, policy, md.Labels, md.Annotations)
	return err
}

func (s *Service) enforceRetention(ctx context.Context) error {
	policy, err := s.GetPolicy()
	if err != nil {
		return err
	}
	items, err := s.ListSnapshots()
	if err != nil {
		return err
	}
	if len(items) <= policy.Retention {
		return nil
	}
	for _, item := range items[policy.Retention:] {
		if item.Spec.Key != "" {
			_ = s.manager.store.Delete(ctx, item.Spec.Key)
		}
		_ = s.store.RemoveResource(resourceTypeSnapshot, item.ID)
	}
	return nil
}

// Restore restores resources snapshot with optional dry-run.
func (s *Service) Restore(ctx context.Context, snapshotID string, dryRun bool) (*RestoreResult, error) {
	var spec SnapshotSpec
	var status SnapshotStatus
	md, err := s.store.GetResource(resourceTypeSnapshot, snapshotID, &spec, &status)
	if err != nil || md.Name == "" {
		return nil, fmt.Errorf("snapshot not found")
	}
	if status.Status != "success" {
		return nil, fmt.Errorf("snapshot is not restorable: %s", status.Status)
	}
	if spec.Type != "resources" {
		return nil, fmt.Errorf("restore currently supports only resources snapshots")
	}
	r, err := s.manager.store.Download(ctx, spec.Key)
	if err != nil {
		return nil, err
	}
	defer r.Close()
	data, err := io.ReadAll(r)
	if err != nil {
		return nil, err
	}

	var entries []exportEntry
	if err := json.Unmarshal(data, &entries); err != nil {
		return nil, fmt.Errorf("decode snapshot: %w", err)
	}

	result := &RestoreResult{SnapshotID: snapshotID, DryRun: dryRun, ByType: map[string]int{}, ResourcesSeen: len(entries)}
	for _, entry := range entries {
		result.ByType[entry.Type]++
		if dryRun {
			continue
		}
		if err := s.applyEntry(entry); err != nil {
			return nil, err
		}
		result.Restored++
	}
	return result, nil
}

func (s *Service) applyEntry(entry exportEntry) error {
	md, err := s.store.GetResource(entry.Type, entry.Name, nil, nil)
	if err != nil || md.Name == "" {
		_, err = s.store.CreateResource(entry.Type, entry.Name, json.RawMessage(entry.Spec), json.RawMessage(entry.Status), entry.Labels, entry.Annotations)
		return err
	}
	_, err = s.store.UpdateResource(entry.Type, entry.Name, md.ResourceVersion, json.RawMessage(entry.Spec), entry.Labels, entry.Annotations)
	if err != nil {
		return err
	}
	_, err = s.store.UpdateStatus(entry.Type, entry.Name, json.RawMessage(entry.Status))
	return err
}
