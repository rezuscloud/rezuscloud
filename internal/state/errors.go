package state

import "errors"

// Sentinel errors for the store.
var (
	ErrNotFound = errors.New("resource not found")
	ErrConflict = errors.New("resource version conflict")
)
