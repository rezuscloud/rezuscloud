package tfbackend

import (
	"encoding/json"
	"fmt"
)

func marshalLockInfo(info LockInfo) ([]byte, error) {
	b, err := json.Marshal(info)
	if err != nil {
		return nil, fmt.Errorf("tfbackend: marshal lock info: %w", err)
	}
	return b, nil
}

func unmarshalLockInfo(b []byte) (*LockInfo, error) {
	var info LockInfo
	if err := json.Unmarshal(b, &info); err != nil {
		return nil, fmt.Errorf("tfbackend: unmarshal lock info: %w", err)
	}
	return &info, nil
}
