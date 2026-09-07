package oidc

import (
	"context"
	"crypto"
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rsa"
	"encoding/base64"
	"encoding/binary"
	"encoding/json"
	"fmt"
	"io"
	"math/big"
	"net/http"
	"sync"
	"time"
)

// jwks is the JSON Web Key Set document (RFC 7517).
type jwks struct {
	Keys []jwk `json:"keys"`
}

type jwk struct {
	Kty string `json:"kty"`
	Kid string `json:"kid"`
	Alg string `json:"alg"`
	N   string `json:"n"`
	E   string `json:"e"`
	Crv string `json:"crv"`
	X   string `json:"x"`
	Y   string `json:"y"`
}

// keyAt fetches the JWKS document and returns the public key with the given
// key ID. JWKS responses are cached per URI for one hour; a miss on kid
// forces one refresh (key rotation protection).
func keyAt(ctx context.Context, client *http.Client, jwksURL, kid string) (crypto.PublicKey, error) {
	if kid == "" {
		return nil, fmt.Errorf("id token header missing kid")
	}

	keys, cached := jwksCache.get(jwksURL)
	if !cached || findKey(keys, kid) == nil {
		var err error
		if keys, err = fetchJWKS(ctx, client, jwksURL); err != nil {
			return nil, err
		}
		jwksCache.put(jwksURL, keys)
	}

	key := findKey(keys, kid)
	if key == nil {
		return nil, fmt.Errorf("jwks has no key %q", kid)
	}
	return key.materialize()
}

func findKey(keys *jwks, kid string) *jwk {
	for i := range keys.Keys {
		if keys.Keys[i].Kid == kid && (keys.Keys[i].Kty == "RSA" || keys.Keys[i].Kty == "EC") {
			return &keys.Keys[i]
		}
	}
	return nil
}

func fetchJWKS(ctx context.Context, client *http.Client, jwksURL string) (*jwks, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, jwksURL, nil)
	if err != nil {
		return nil, fmt.Errorf("build jwks request: %w", err)
	}
	resp, err := client.Do(req)
	if err != nil {
		return nil, fmt.Errorf("fetch jwks: %w", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("jwks: %s from %s", resp.Status, jwksURL)
	}
	var set jwks
	if err := json.NewDecoder(io.LimitReader(resp.Body, 1<<20)).Decode(&set); err != nil {
		return nil, fmt.Errorf("parse jwks: %w", err)
	}
	return &set, nil
}

// materialize converts a JWK into a verify-ready crypto.PublicKey.
func (k *jwk) materialize() (crypto.PublicKey, error) {
	switch k.Kty {
	case "RSA":
		n, err := base64.RawURLEncoding.DecodeString(k.N)
		if err != nil {
			return nil, fmt.Errorf("jwk n: %w", err)
		}
		e, err := base64.RawURLEncoding.DecodeString(k.E)
		if err != nil {
			return nil, fmt.Errorf("jwk e: %w", err)
		}
		if len(e) > 4 {
			return nil, fmt.Errorf("jwk e: exponent too large")
		}
		var eb [4]byte
		copy(eb[4-len(e):], e)
		return &rsa.PublicKey{N: new(big.Int).SetBytes(n), E: int(binary.BigEndian.Uint32(eb[:]))}, nil
	case "EC":
		var curve elliptic.Curve
		switch k.Crv {
		case "P-256":
			curve = elliptic.P256()
		case "P-384":
			curve = elliptic.P384()
		case "P-521":
			curve = elliptic.P521()
		default:
			return nil, fmt.Errorf("jwk: unsupported curve %q", k.Crv)
		}
		x, err := base64.RawURLEncoding.DecodeString(k.X)
		if err != nil {
			return nil, fmt.Errorf("jwk x: %w", err)
		}
		y, err := base64.RawURLEncoding.DecodeString(k.Y)
		if err != nil {
			return nil, fmt.Errorf("jwk y: %w", err)
		}
		return &ecdsa.PublicKey{Curve: curve, X: new(big.Int).SetBytes(x), Y: new(big.Int).SetBytes(y)}, nil
	default:
		return nil, fmt.Errorf("jwk: unsupported key type %q", k.Kty)
	}
}

// jwksCache is a tiny TTL cache keyed by JWKS URI.
var jwksCache = &jwksTTL{ttl: time.Hour}

type jwksTTL struct {
	mu  sync.Mutex
	ttl time.Duration
	m   map[string]jwksEntry
}

type jwksEntry struct {
	set     *jwks
	expires time.Time
}

func (c *jwksTTL) get(uri string) (*jwks, bool) {
	c.mu.Lock()
	defer c.mu.Unlock()
	e, ok := c.m[uri]
	if !ok || time.Now().After(e.expires) {
		return nil, false
	}
	return e.set, true
}

func (c *jwksTTL) put(uri string, set *jwks) {
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.m == nil {
		c.m = make(map[string]jwksEntry)
	}
	c.m[uri] = jwksEntry{set: set, expires: time.Now().Add(c.ttl)}
}
