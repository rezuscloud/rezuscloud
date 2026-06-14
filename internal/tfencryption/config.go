// Package tfencryption builds OpenTofu native state-encryption configuration
// from a passphrase RezusCloud holds.
//
// State at rest is encrypted by OpenTofu's OWN encryption, never by RezusCloud
// (ADR 21): RezusCloud's HTTP backend (#84) stores opaque encrypted blobs and
// never implements cryptography. The encrypted blob is self-describing — its
// serial/lineage live inside the (possibly encrypted) envelope.
//
// RezusCloud holds a passphrase (v1: a single root passphrase read from the
// REZUSCLOUD_ENCRYPTION_PASSPHRASE environment variable) and turns it into a
// TF_ENCRYPTION environment value that the tofu subprocess consumes. Using the
// environment — NOT a file in the per-tenant workdir — means the passphrase
// never touches disk; it lives only in RezusCloud's process and the transient
// tofu subprocess it spawns (injected via tfexec's clean-env mechanism, #85).
//
// The configuration uses the pbkdf2 key provider + aes_gcm method, the
// passphrase-based scheme recommended for new projects in the OpenTofu docs:
//
//	https://opentofu.org/docs/language/state/encryption/
//
// All builders return the encryption block DIRECTLY (the value TF_ENCRYPTION
// expects) — no surrounding `terraform` wrapper, per tofu's env-config grammar.
package tfencryption

import (
	"encoding/json"
	"fmt"
)

// MinPassphraseLen is the minimum passphrase length enforced by RezusCloud.
// OpenTofu's pbkdf2 key provider requires at least 16 characters; RezusCloud
// validates this at construction time so a misconfigured deploy fails fast
// rather than producing an unencrypted state.
const MinPassphraseLen = 16

// Names of the single key provider + method RezusCloud writes. Fixed (not
// per-tenant) so the encrypted_metadata_alias stays stable across tenants in v1;
// a future per-tenant passphrase scheme can key these off the tenant name.
const (
	keyProviderName = "rezus"
	methodName      = "rezus"
)

// ErrPassphraseTooShort is returned by ValidatePassphrase (and transitively by
// the builders) when the passphrase is shorter than MinPassphraseLen.
var ErrPassphraseTooShort = fmt.Errorf("tfencryption: passphrase must be at least %d characters", MinPassphraseLen)

// ValidatePassphrase returns ErrPassphraseTooShort if the passphrase is missing
// or too short. RezusCloud calls this at startup (and tfexec.WithEncryption
// calls it at construction) to fail fast on a misconfigured deploy.
func ValidatePassphrase(passphrase string) error {
	if len(passphrase) < MinPassphraseLen {
		return ErrPassphraseTooShort
	}
	return nil
}

// Config builds the TF_ENCRYPTION environment value for the primary
// passphrase-based encryption (pbkdf2 + aes_gcm, linked to both state and plan).
// This is the default: RezusCloud owns its backend from day one, so state is
// encrypted on first write and there is no pre-existing unencrypted state to
// migrate. Returns the encryption block JSON directly (what TF_ENCRYPTION
// expects).
func Config(passphrase string) (string, error) {
	if err := ValidatePassphrase(passphrase); err != nil {
		return "", err
	}
	return marshal(encryptionBlock{
		KeyProvider: map[string]map[string]map[string]string{
			"pbkdf2": {keyProviderName: {"passphrase": passphrase}},
		},
		Method: map[string]map[string]map[string]string{
			"aes_gcm": {methodName: {"keys": "key_provider.pbkdf2." + keyProviderName}},
		},
		State: linkage(methodName),
		Plan:  linkage(methodName),
	})
}

// ConfigWithFallback is like Config but adds an `unencrypted` fallback method
// under state and plan. OpenTofu tries the fallback when it cannot read with the
// primary method — the documented path for migrating pre-existing unencrypted
// state. RezusCloud's own tenants do not need this (they start encrypted), but it
// is provided for importing an external unencrypted state into a tenant.
func ConfigWithFallback(passphrase string) (string, error) {
	if err := ValidatePassphrase(passphrase); err != nil {
		return "", err
	}
	return marshal(encryptionBlock{
		KeyProvider: map[string]map[string]map[string]string{
			"pbkdf2": {keyProviderName: {"passphrase": passphrase}},
		},
		Method: map[string]map[string]map[string]string{
			"aes_gcm":     {methodName: {"keys": "key_provider.pbkdf2." + keyProviderName}},
			"unencrypted": {"migrate": {}},
		},
		State: linkageWithFallback(methodName),
		Plan:  linkageWithFallback(methodName),
	})
}

// MustConfig is a convenience for tests/main wiring: panics on a validation
// error. Production code should use Config and handle the error.
func MustConfig(passphrase string) string {
	c, err := Config(passphrase)
	if err != nil {
		panic(err)
	}
	return c
}

// encryptionBlock is the JSON shape of the TF_ENCRYPTION value, mirroring the
// native-syntax `terraform.encryption` block's children. The `//` comment marks
// the value as RezusCloud-generated so a human inspecting a dumped env knows not
// to hand-edit it.
type encryptionBlock struct {
	KeyProvider map[string]map[string]map[string]string `json:"key_provider"`
	Method      map[string]map[string]map[string]string `json:"method"`
	State       map[string]any                          `json:"state"`
	Plan        map[string]any                          `json:"plan"`
}

// linkage returns the state/plan block pointing method at the named aes_gcm key.
func linkage(method string) map[string]any {
	return map[string]any{"method": "method.aes_gcm." + method}
}

// linkageWithFallback is linkage plus an `unencrypted` fallback for migration.
func linkageWithFallback(method string) map[string]any {
	return map[string]any{
		"method": "method.aes_gcm." + method,
		"fallback": map[string]any{
			"method": "method.unencrypted.migrate",
		},
	}
}

func marshal(b encryptionBlock) (string, error) {
	raw, err := json.MarshalIndent(b, "", "  ")
	if err != nil {
		return "", fmt.Errorf("tfencryption: marshal block: %w", err)
	}
	return string(raw), nil
}
