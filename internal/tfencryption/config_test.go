package tfencryption

import (
	"encoding/json"
	"errors"
	"strings"
	"testing"
)

func TestValidatePassphrase(t *testing.T) {
	cases := []struct {
		name    string
		in      string
		wantErr bool
	}{
		{"empty", "", true},
		{"too short", "short", true},
		{"fifteen chars", "123456789012345", true}, // exactly one under the limit
		{"sixteen chars", "1234567890123456", false},
		{"long", "correct-horse-battery-staple", false},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			err := ValidatePassphrase(c.in)
			if c.wantErr && !errors.Is(err, ErrPassphraseTooShort) {
				t.Errorf("want ErrPassphraseTooShort, got %v", err)
			}
			if !c.wantErr && err != nil {
				t.Errorf("want nil, got %v", err)
			}
		})
	}
}

func TestConfig_RejectsShortPassphrase(t *testing.T) {
	if _, err := Config("too-short"); !errors.Is(err, ErrPassphraseTooShort) {
		t.Fatalf("want ErrPassphraseTooShort, got %v", err)
	}
}

func TestConfig_BuildsValidEncryptionBlock(t *testing.T) {
	out, err := Config("correct-horse-battery-staple")
	if err != nil {
		t.Fatal(err)
	}

	// Must be valid JSON.
	var raw map[string]any
	if err := json.Unmarshal([]byte(out), &raw); err != nil {
		t.Fatalf("output is not valid JSON: %v\n%s", err, out)
	}

	// The output is the encryption block DIRECTLY (no terraform wrapper): tofu's
	// TF_ENCRYPTION env expects exactly these top-level keys.
	wantKeys := []string{"key_provider", "method", "state", "plan"}
	for _, k := range wantKeys {
		if _, ok := raw[k]; !ok {
			t.Errorf("missing top-level key %q in:\n%s", k, out)
		}
	}
	for _, k := range []string{"terraform", "encryption"} {
		if _, ok := raw[k]; ok {
			t.Errorf("output must NOT wrap in %q (TF_ENCRYPTION takes the block directly): %s", k, out)
		}
	}

	// The pbkdf2 provider carries the passphrase; the aes_gcm method references it;
	// state + plan link the method.
	if !strings.Contains(out, "correct-horse-battery-staple") {
		t.Errorf("passphrase not embedded: %s", out)
	}
	if !strings.Contains(out, `"keys": "key_provider.pbkdf2.rezus"`) {
		t.Errorf("method does not reference the key provider: %s", out)
	}
	if !strings.Contains(out, `"method": "method.aes_gcm.rezus"`) {
		t.Errorf("state/plan does not link the method: %s", out)
	}
}

func TestConfigWithFallback_AddsUnencryptedMigration(t *testing.T) {
	out, err := ConfigWithFallback("correct-horse-battery-staple")
	if err != nil {
		t.Fatal(err)
	}
	var raw map[string]any
	if err := json.Unmarshal([]byte(out), &raw); err != nil {
		t.Fatalf("invalid JSON: %v", err)
	}
	methods, _ := raw["method"].(map[string]any)
	if _, ok := methods["unencrypted"]; !ok {
		t.Errorf("fallback config missing unencrypted method: %s", out)
	}
	if !strings.Contains(out, `"method": "method.unencrypted.migrate"`) {
		t.Errorf("fallback linkage missing: %s", out)
	}
}

func TestMustConfig_PanicsOnBadPassphrase(t *testing.T) {
	defer func() {
		if r := recover(); r == nil {
			t.Fatal("expected panic for short passphrase")
		}
	}()
	MustConfig("short")
}

func TestMustConfig_SucceedsOnGoodPassphrase(t *testing.T) {
	if out := MustConfig("correct-horse-battery-staple"); !strings.Contains(out, "pbkdf2") {
		t.Fatalf("unexpected output: %s", out)
	}
}

// TestConfig_ProducesDeterministicShape confirms two calls with the same
// passphrase produce byte-identical output (no maps/maps iteration leak). This
// matters because the value is injected as an env var on every apply.
func TestConfig_ProducesDeterministicShape(t *testing.T) {
	a, _ := Config("correct-horse-battery-staple")
	b, _ := Config("correct-horse-battery-staple")
	if a != b {
		t.Errorf("non-deterministic output:\na=%s\nb=%s", a, b)
	}
}
