package oidc

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"time"

	"github.com/golang-jwt/jwt/v5"
)

// clockLeeway tolerates small clock skew between the platform and the IdP.
const clockLeeway = time.Minute

// IDTokenClaims carries the ID token claims the flow consumes. Registered
// claims (iss, aud, sub, exp, iat, nbf) are validated by the parser itself;
// nonce, username/email and groups are consumed by the identity resolution.
type IDTokenClaims struct {
	jwt.RegisteredClaims
	Nonce             string   `json:"nonce"`
	PreferredUsername string   `json:"preferred_username"`
	Email             string   `json:"email"`
	Name              string   `json:"name"`
	Groups            []string `json:"-"`
}

// VerifyIDToken validates a raw ID token against the provider's JWKS and
// the protocol expectations of the flow: signed with a key from the set
// (RSA/ECDSA only — no symmetric or none), issued by the expected issuer,
// for this client, unexpired, and bound to the flow's nonce.
func VerifyIDToken(ctx context.Context, client *http.Client, raw, issuer, jwksURL, clientID, nonce string) (*IDTokenClaims, error) {
	parser := jwt.NewParser(
		jwt.WithValidMethods([]string{"RS256", "RS384", "RS512", "ES256", "ES384", "ES512"}),
		jwt.WithIssuer(issuer),
		jwt.WithAudience(clientID),
		jwt.WithExpirationRequired(),
		jwt.WithTimeFunc(func() time.Time { return time.Now().Add(clockLeeway) }),
	)

	claims := &IDTokenClaims{}
	token, err := parser.ParseWithClaims(raw, claims, func(t *jwt.Token) (any, error) {
		kid, _ := t.Header["kid"].(string)
		key, err := keyAt(ctx, client, jwksURL, kid)
		if err != nil {
			return nil, fmt.Errorf("resolve signing key: %w", err)
		}
		return key, nil
	})
	if err != nil {
		return nil, fmt.Errorf("id token: %w", err)
	}
	if !token.Valid {
		return nil, fmt.Errorf("id token invalid")
	}
	if nonce == "" || claims.Nonce != nonce {
		return nil, fmt.Errorf("id token nonce mismatch")
	}
	if claims.Subject == "" {
		return nil, fmt.Errorf("id token missing sub claim")
	}
	return claims, nil
}

// UnmarshalJSON decodes the groups claim in both its string and array
// forms (aud is handled by jwt.RegisteredClaims' ClaimStrings).
func (c *IDTokenClaims) UnmarshalJSON(data []byte) error {
	type alias IDTokenClaims
	aux := &struct {
		Groups json.RawMessage `json:"groups"`
		*alias
	}{alias: (*alias)(c)}
	if err := json.Unmarshal(data, aux); err != nil {
		return err
	}
	if len(aux.Groups) > 0 {
		if err := json.Unmarshal(aux.Groups, &c.Groups); err != nil {
			var single string
			if err2 := json.Unmarshal(aux.Groups, &single); err2 != nil {
				return fmt.Errorf("groups: %w", err)
			}
			c.Groups = []string{single}
		}
	}
	return nil
}
