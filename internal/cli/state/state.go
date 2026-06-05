// Package state defines the types and interfaces for RezusCloud state management.
package state

// StepStatus represents the state of a single boot step.
type StepStatus string

const (
	StatusCreated  StepStatus = "created"
	StatusRunning  StepStatus = "running"
	StatusComplete StepStatus = "complete"
	StatusFailed   StepStatus = "failed"
)
