package oidc

import (
	"crypto"
	"crypto/rand"
	"crypto/rsa"
	"crypto/sha256"
	"encoding/base64"
	"encoding/json"
	"math/big"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"
)

// testIdP is an in-process OpenID Provider: a discovery document, a token
// endpoint minting RSA-signed ID tokens, and a JWKS endpoint — served from
// one httptest.Server. It lets the flow tests exercise real discovery,
// token exchange, and signature verification without network access.
type testIdP struct {
	*httptest.Server
	key    *rsa.PrivateKey
	issuer string
	mint   func(nonce, subject, username, email string, groups []string) string
}

func newTestIdP(t *testing.T) *testIdP {
	t.Helper()

	key, err := rsa.GenerateKey(rand.Reader, 2048)
	if err != nil {
		t.Fatalf("generate rsa key: %v", err)
	}
	p := &testIdP{key: key}

	var lastIssued string
	mux := http.NewServeMux()

	mux.HandleFunc("/.well-known/openid-configuration", func(w http.ResponseWriter, _ *http.Request) {
		json.NewEncoder(w).Encode(ProviderMetadata{
			Issuer:      p.URL,
			AuthURL:     p.URL + "/auth",
			TokenURL:    p.URL + "/token",
			JWKSURL:     p.URL + "/jwks",
			IDTokenAlgs: []string{"RS256"},
		})
	})

	mux.HandleFunc("/token", func(w http.ResponseWriter, r *http.Request) {
		_ = r.ParseForm()
		if r.FormValue("code") == "" {
			w.WriteHeader(http.StatusBadRequest)
			return
		}
		// Re-issue the most recently prepared token; tests set it via mint.
		w.Header().Set("Content-Type", "application/json")
		json.NewEncoder(w).Encode(map[string]any{
			"access_token": "at-123",
			"token_type":   "Bearer",
			"id_token":     lastIssued,
		})
	})

	mux.HandleFunc("/jwks", func(w http.ResponseWriter, _ *http.Request) {
		b := key.N.Bytes()
		eBig := big.NewInt(int64(key.E))
		json.NewEncoder(w).Encode(jwks{Keys: []jwk{{
			Kty: "RSA", Kid: "test-key", Alg: "RS256",
			N: base64.RawURLEncoding.EncodeToString(b),
			E: base64.RawURLEncoding.EncodeToString(eBig.Bytes()),
		}}})
	})

	p.Server = httptest.NewServer(mux)
	t.Cleanup(p.Close)
	p.issuer = p.URL

	p.mint = func(nonce, subject, username, email string, groups []string) string {
		claims := map[string]any{
			"iss": p.URL,
			"sub": subject,
			"aud": "rezuscloud-test",
			"exp": time.Now().Add(5 * time.Minute).Unix(),
			"iat": time.Now().Unix(),
		}
		if nonce != "" {
			claims["nonce"] = nonce
		}
		if username != "" {
			claims["preferred_username"] = username
		}
		if email != "" {
			claims["email"] = email
		}
		if groups != nil {
			claims["groups"] = groups
		}
		raw := mintRS256(t, key, "test-key", claims)
		lastIssued = raw
		return raw
	}
	return p
}

// mintRS256 signs claims with the key and produces a compact JWS.
func mintRS256(t *testing.T, key *rsa.PrivateKey, kid string, claims map[string]any) string {
	t.Helper()
	header := map[string]any{"alg": "RS256", "typ": "JWT", "kid": kid}
	h, _ := json.Marshal(header)
	c, _ := json.Marshal(claims)
	payload := base64.RawURLEncoding.EncodeToString(h) + "." + base64.RawURLEncoding.EncodeToString(c)
	sum := sha256.Sum256([]byte(payload))
	sig, err := rsa.SignPKCS1v15(rand.Reader, key, crypto.SHA256, sum[:])
	if err != nil {
		t.Fatalf("sign: %v", err)
	}
	return payload + "." + base64.RawURLEncoding.EncodeToString(sig)
}
