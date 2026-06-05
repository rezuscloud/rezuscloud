// Package boot defines the boot orchestrator.
// Boot is a sequential state machine that provisions infrastructure
// and installs platform components with health checks at every step.
package boot

import "context"

// StepStatus represents the state of a boot step.
type StepStatus string

const (
	StatusPending  StepStatus = "pending"
	StatusRunning  StepStatus = "running"
	StatusComplete StepStatus = "complete"
	StatusFailed   StepStatus = "failed"
	StatusSkipped  StepStatus = "skipped"
)

// Step represents a single step in the boot sequence.
type Step struct {
	Name        string     `json:"name"`
	Status      StepStatus `json:"status"`
	Duration    string     `json:"duration,omitempty"`
	Error       string     `json:"error,omitempty"`
	Description string     `json:"description"`
}

// Progress represents the overall boot progress.
type Progress struct {
	ClusterName string `json:"clusterName"`
	Platform    string `json:"platform"`
	TotalSteps  int    `json:"totalSteps"`
	CurrentStep int    `json:"currentStep"`
	Steps       []Step `json:"steps"`
	Complete    bool   `json:"complete"`
	Error       string `json:"error,omitempty"`
}

// Event represents a boot event emitted during the sequence.
type Event struct {
	Type      string `json:"type"` // step-start, step-complete, step-failed, boot-complete
	Step      string `json:"step"`
	Message   string `json:"message"`
	Timestamp int64  `json:"timestamp"`
}

// Orchestrator drives the boot sequence.
type Orchestrator interface {
	// Run executes the full boot sequence. Emits events to the channel.
	Run(ctx context.Context, events chan<- Event) error

	// Resume resumes a partially completed boot from the last successful step.
	Resume(ctx context.Context, events chan<- Event) error

	// Status returns the current boot progress.
	Status(ctx context.Context) (*Progress, error)

	// Destroy tears down all provisioned infrastructure.
	Destroy(ctx context.Context) error
}
