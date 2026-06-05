// Package auth provides JWT authentication and role-based authorization.
package auth

import (
	"errors"
	"fmt"
	"time"

	"github.com/golang-jwt/jwt/v5"
	"github.com/rezuscloud/rezuscloud/internal/state"
)

// Role constants following K8s default ClusterRole naming.
const (
	RoleView  = "view"
	RoleEdit  = "edit"
	RoleAdmin = "admin"
)

// Token expiry defaults.
const (
	DefaultTokenExpiry = 24 * time.Hour
	MinTokenExpiry     = 1 * time.Hour
	MaxTokenExpiry     = 30 * 24 * time.Hour
)

// Claims contains JWT claims for a management plane user.
type Claims struct {
	jwt.RegisteredClaims
	Username string `json:"sub"`
	Role     string `json:"role"`
}

// TokenPair contains access token and optional refresh token.
type TokenPair struct {
	AccessToken string `json:"accessToken"`
	ExpiresAt   int64  `json:"expiresAt"`
	TokenType   string `json:"tokenType"`
}

var (
	ErrInvalidToken = errors.New("invalid token")
	ErrExpiredToken = errors.New("token expired")
	ErrInvalidRole  = errors.New("invalid role")
)

// ValidRoles are the allowed role values.
var ValidRoles = map[string]bool{
	RoleView:  true,
	RoleEdit:  true,
	RoleAdmin: true,
}

// JWTManager handles JWT token generation and validation.
type JWTManager struct {
	secret []byte
	issuer string
}

// NewJWTManager creates a new JWT manager with the given secret.
func NewJWTManager(secret string) *JWTManager {
	return &JWTManager{
		secret: []byte(secret),
		issuer: "rezuscloud",
	}
}

// GenerateToken generates a JWT for the given user.
func (m *JWTManager) GenerateToken(user *state.User, expiry time.Duration) (TokenPair, error) {
	if !ValidRoles[user.Spec.Role] {
		return TokenPair{}, ErrInvalidRole
	}

	expiresAt := time.Now().UTC().Add(expiry)

	claims := Claims{
		RegisteredClaims: jwt.RegisteredClaims{
			Issuer:    m.issuer,
			Subject:   user.Metadata.Name,
			ExpiresAt: jwt.NewNumericDate(expiresAt),
			IssuedAt:  jwt.NewNumericDate(time.Now().UTC()),
		},
		Username: user.Metadata.Name,
		Role:     user.Spec.Role,
	}

	token := jwt.NewWithClaims(jwt.SigningMethodHS256, claims)
	tokenString, err := token.SignedString(m.secret)
	if err != nil {
		return TokenPair{}, fmt.Errorf("sign token: %w", err)
	}

	return TokenPair{
		AccessToken: tokenString,
		ExpiresAt:   expiresAt.Unix(),
		TokenType:   "Bearer",
	}, nil
}

// ValidateToken validates a JWT and returns the claims.
func (m *JWTManager) ValidateToken(tokenString string) (*Claims, error) {
	token, err := jwt.ParseWithClaims(tokenString, &Claims{}, func(token *jwt.Token) (any, error) {
		if _, ok := token.Method.(*jwt.SigningMethodHMAC); !ok {
			return nil, fmt.Errorf("unexpected signing method: %v", token.Header["alg"])
		}
		return m.secret, nil
	})
	if err != nil {
		if errors.Is(err, jwt.ErrTokenExpired) {
			return nil, ErrExpiredToken
		}
		return nil, ErrInvalidToken
	}

	claims, ok := token.Claims.(*Claims)
	if !ok || !token.Valid {
		return nil, ErrInvalidToken
	}

	return claims, nil
}

// HashPassword hashes a password using bcrypt.
func HashPassword(password string) (string, error) {
	return state.BcryptCost(password)
}

// CheckPassword compares a password against a bcrypt hash.
func CheckPassword(password, hash string) bool {
	return state.CheckBcrypt(password, hash)
}
