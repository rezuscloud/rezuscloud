package logs

import (
	"fmt"
	"time"

	"github.com/rezuscloud/rezuscloud/internal/state"
)

// StoreLogProvider provides log entries from the state store.
// In production, this reads machine bootstrap logs via the Talos API.
// For now, it generates synthetic logs from machine status changes.
type StoreLogProvider struct {
	store state.StoreAPI
}

// NewStoreLogProvider creates a log provider backed by the state store.
func NewStoreLogProvider(store state.StoreAPI) *StoreLogProvider {
	return &StoreLogProvider{store: store}
}

// StreamLogs returns synthetic log entries for the given machine.
// Until live log streaming is implemented, this produces bootstrap log messages
// based on the machine's current stage.
func (p *StoreLogProvider) StreamLogs(machineID string, opts LogOptions) (<-chan LogEntry, error) {
	// Check if machine exists.
	var spec struct {
		Connected bool   `json:"connected"`
		Role      string `json:"role"`
	}
	_, err := p.store.GetResource("machine", machineID, &spec, nil)
	if err != nil {
		return nil, fmt.Errorf("machine %q disconnected", machineID)
	}

	ch := make(chan LogEntry, 10)
	go func() {
		defer close(ch)

		// Generate synthetic log entries based on machine state.
		now := time.Now().UTC()
		entries := []LogEntry{
			{Timestamp: now.Add(-5 * time.Minute), Message: "Talos API connection established", Level: "info", Source: "talos"},
			{Timestamp: now.Add(-4 * time.Minute), Message: fmt.Sprintf("Machine role: %s", spec.Role), Level: "info", Source: "config"},
			{Timestamp: now.Add(-3 * time.Minute), Message: "Talos config applied successfully", Level: "info", Source: "config"},
			{Timestamp: now.Add(-2 * time.Minute), Message: "Services starting...", Level: "info", Source: "machined"},
			{Timestamp: now.Add(-1 * time.Minute), Message: "Kubelet started", Level: "info", Source: "kubelet"},
			{Timestamp: now, Message: "Node ready", Level: "info", Source: "kubelet"},
		}

		for _, e := range entries {
			if !opts.Since.IsZero() && e.Timestamp.Before(opts.Since) {
				continue
			}
			ch <- e
		}
	}()

	return ch, nil
}
