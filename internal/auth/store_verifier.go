package auth

import "github.com/rezuscloud/rezuscloud/internal/state"

// StoreTokenVerifier adapts a *state.Store to the APITokenVerifier interface
// used by AuthenticateWithTokens. It delegates to auth.VerifyAPIToken so the
// hash + expiry + last-used logic lives in exactly one place.
type StoreTokenVerifier struct {
	Store *state.Store
}

// VerifyAPIToken satisfies auth.APITokenVerifier.
func (v StoreTokenVerifier) VerifyAPIToken(plaintext string) (*state.User, bool) {
	user, _, ok := VerifyAPIToken(v.Store, plaintext)
	return user, ok
}
