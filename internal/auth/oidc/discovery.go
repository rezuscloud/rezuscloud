package oidc

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"strings"
	"sync"
	"time"
)

// ProviderMetadata is the subset of the OpenID discovery document the flow
// needs (https://openid.net/specs/openid-connect-discovery-1_0.html).
type ProviderMetadata struct {
	Issuer      string   `json:"issuer"`
	AuthURL     string   `json:"authorization_endpoint"`
	TokenURL    string   `json:"token_endpoint"`
	JWKSURL     string   `json:"jwks_uri"`
	IDTokenAlgs []string `json:"id_token_signing_alg_values_supported"`
}

// discover fetches {issuer}/.well-known/openid-configuration and validates
// that the document's issuer matches the expected one (mix-up protection).
// Results are cached for an hour — discovery is stable and a login should
// not cost a round-trip to the IdP metadata endpoint every time.
func discover(ctx context.Context, client *http.Client, issuer string) (*ProviderMetadata, error) {
	if cached, ok := discoveryCache.get(issuer); ok {
		return cached, nil
	}

	wellKnown := strings.TrimRight(issuer, "/") + "/.well-known/openid-configuration"
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, wellKnown, nil)
	if err != nil {
		return nil, fmt.Errorf("build discovery request: %w", err)
	}
	resp, err := client.Do(req)
	if err != nil {
		return nil, fmt.Errorf("fetch discovery document: %w", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("discovery document: %s from %s", resp.Status, wellKnown)
	}
	body, err := io.ReadAll(io.LimitReader(resp.Body, 1<<20))
	if err != nil {
		return nil, fmt.Errorf("read discovery document: %w", err)
	}

	var md ProviderMetadata
	if err := json.Unmarshal(body, &md); err != nil {
		return nil, fmt.Errorf("parse discovery document: %w", err)
	}
	if md.Issuer != issuer {
		return nil, fmt.Errorf("discovery issuer mismatch: got %q, want %q", md.Issuer, issuer)
	}
	if md.AuthURL == "" || md.TokenURL == "" || md.JWKSURL == "" {
		return nil, fmt.Errorf("discovery document missing endpoints (authorization/token/jwks)")
	}

	discoveryCache.put(issuer, &md)
	return &md, nil
}

// discoveryCache is a tiny TTL cache keyed by issuer.
var discoveryCache = &cache{ttl: time.Hour}

type cache struct {
	mu  sync.Mutex
	ttl time.Duration
	m   map[string]cacheEntry
}

type cacheEntry struct {
	md      *ProviderMetadata
	expires time.Time
}

func (c *cache) get(issuer string) (*ProviderMetadata, bool) {
	c.mu.Lock()
	defer c.mu.Unlock()
	e, ok := c.m[issuer]
	if !ok || time.Now().After(e.expires) {
		return nil, false
	}
	return e.md, true
}

func (c *cache) put(issuer string, md *ProviderMetadata) {
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.m == nil {
		c.m = make(map[string]cacheEntry)
	}
	c.m[issuer] = cacheEntry{md: md, expires: time.Now().Add(c.ttl)}
}
