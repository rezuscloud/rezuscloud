package state

import (
	"fmt"
	"os"
	"strconv"

	"golang.org/x/crypto/bcrypt"
)

const defaultBcryptCost = 12

// BcryptCost hashes a password using bcrypt.
// In tests/CI, REZUSCLOUD_BCRYPT_COST can lower cost to reduce runtime.
func BcryptCost(password string) (string, error) {
	cost := defaultBcryptCost
	if raw := os.Getenv("REZUSCLOUD_BCRYPT_COST"); raw != "" {
		if v, err := strconv.Atoi(raw); err == nil && v >= bcrypt.MinCost && v <= bcrypt.MaxCost {
			cost = v
		}
	}
	hash, err := bcrypt.GenerateFromPassword([]byte(password), cost)
	if err != nil {
		return "", fmt.Errorf("bcrypt hash: %w", err)
	}
	return string(hash), nil
}

// CheckBcrypt compares a password against a bcrypt hash.
func CheckBcrypt(password, hash string) bool {
	return bcrypt.CompareHashAndPassword([]byte(hash), []byte(password)) == nil
}
