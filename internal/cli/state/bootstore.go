package state

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"sync"
	"time"
)

// BootState tracks ephemeral boot progress — which orchestrator steps completed.
// Written after each step. On re-run, completed steps are skipped.
// Deleted on `rezusctl destroy`.
type BootState struct {
	ClusterName string                `json:"clusterName"`
	Platform    string                `json:"platform"`
	Steps       map[string]StepStatus `json:"steps"`
	CreatedAt   string                `json:"createdAt"`
	UpdatedAt   string                `json:"updatedAt"`
}

// BootStore manages boot.json persistence.
type BootStore struct {
	baseDir string
	mu      sync.Mutex
}

// NewBootStore creates a BootStore rooted at the given directory.
func NewBootStore(baseDir string) *BootStore {
	return &BootStore{baseDir: baseDir}
}

func (s *BootStore) path(clusterName string) string {
	return filepath.Join(s.baseDir, clusterName, "boot.json")
}

// Load reads the boot state for a cluster.
func (s *BootStore) Load(_ context.Context, clusterName string) (*BootState, error) {
	s.mu.Lock()
	defer s.mu.Unlock()

	p := s.path(clusterName)
	data, err := os.ReadFile(p)
	if err != nil {
		if os.IsNotExist(err) {
			return nil, nil
		}
		return nil, fmt.Errorf("read boot state %s: %w", p, err)
	}
	var state BootState
	if err := json.Unmarshal(data, &state); err != nil {
		return nil, fmt.Errorf("parse boot state %s: %w", p, err)
	}
	return &state, nil
}

// Save writes the boot state for a cluster.
func (s *BootStore) Save(_ context.Context, state *BootState) error {
	s.mu.Lock()
	defer s.mu.Unlock()

	p := s.path(state.ClusterName)
	if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
		return fmt.Errorf("create state directory: %w", err)
	}
	state.UpdatedAt = time.Now().UTC().Format(time.RFC3339)
	if state.CreatedAt == "" {
		state.CreatedAt = state.UpdatedAt
	}
	data, err := json.MarshalIndent(state, "", "  ")
	if err != nil {
		return fmt.Errorf("marshal boot state: %w", err)
	}
	if err := os.WriteFile(p, data, 0o644); err != nil {
		return fmt.Errorf("write boot state %s: %w", p, err)
	}
	return nil
}

// MarkStep marks a boot step with the given status.
func (s *BootStore) MarkStep(ctx context.Context, clusterName, platform, stepName string, status StepStatus) error {
	state, err := s.Load(ctx, clusterName)
	if err != nil {
		return err
	}
	if state == nil {
		state = &BootState{
			ClusterName: clusterName,
			Platform:    platform,
			Steps:       make(map[string]StepStatus),
		}
	}
	state.Steps[stepName] = status
	return s.Save(ctx, state)
}

// IsStepComplete checks if a boot step has completed.
func (s *BootStore) IsStepComplete(ctx context.Context, clusterName, stepName string) (bool, error) {
	state, err := s.Load(ctx, clusterName)
	if err != nil {
		return false, err
	}
	if state == nil {
		return false, nil
	}
	return state.Steps[stepName] == StatusComplete, nil
}

// CompletedSteps returns a map of step names that have completed.
func (s *BootStore) CompletedSteps(ctx context.Context, clusterName string) (map[string]bool, error) {
	state, err := s.Load(ctx, clusterName)
	if err != nil {
		return nil, err
	}
	result := make(map[string]bool)
	if state == nil {
		return result, nil
	}
	for step, status := range state.Steps {
		if status == StatusComplete {
			result[step] = true
		}
	}
	return result, nil
}

// Delete removes the boot state for a cluster.
func (s *BootStore) Delete(_ context.Context, clusterName string) error {
	s.mu.Lock()
	defer s.mu.Unlock()

	p := s.path(clusterName)
	if err := os.Remove(p); err != nil && !os.IsNotExist(err) {
		return fmt.Errorf("delete boot state: %w", err)
	}
	return nil
}
