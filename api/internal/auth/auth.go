// Package auth issues and checks signed bearer tokens carrying a role and a team.
//
// This is deliberately small: HS256 with one shared key and a development
// issuer. It is enough to make the authorization rules real and testable. It
// is not production authentication — a deployment would verify tokens from its
// identity provider and map groups to roles here.
package auth

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"strings"
	"time"

	"github.com/golang-jwt/jwt/v5"
)

// Role is what a principal may do.
type Role string

// Roles.
const (
	Admin    Role = "admin"    // manages the registry; cannot acknowledge for a consumer
	Owner    Role = "owner"    // owns datasets for their team; publishes contracts and lineage
	Consumer Role = "consumer" // registers dependencies and reviews breaking changes for their team
)

// ParseRole validates a role name.
func ParseRole(s string) (Role, error) {
	switch r := Role(s); r {
	case Admin, Owner, Consumer:
		return r, nil
	}
	return "", fmt.Errorf("unknown role %q (want admin, owner or consumer)", s)
}

// Principal is who is calling.
type Principal struct {
	Subject string `json:"subject"`
	Role    Role   `json:"role"`
	Team    string `json:"team"`
}

// Anonymous is the principal for requests without a token. It can read.
var Anonymous = Principal{Subject: "anonymous"}

// IsAnonymous reports whether no token was presented.
func (p Principal) IsAnonymous() bool { return p.Role == "" }

// Actor is how the principal appears in the audit log.
func (p Principal) Actor() string {
	if p.IsAnonymous() {
		return "anonymous"
	}
	return fmt.Sprintf("%s (%s, %s)", p.Subject, p.Role, p.Team)
}

const issuer = "dataset-registry"

type claims struct {
	Role Role   `json:"role"`
	Team string `json:"team"`
	jwt.RegisteredClaims
}

// Issuer signs and verifies tokens.
type Issuer struct {
	key []byte
	ttl time.Duration
	now func() time.Time
}

// NewIssuer needs a key of at least 32 bytes.
func NewIssuer(key []byte, ttl time.Duration) (*Issuer, error) {
	if len(key) < 32 {
		return nil, errors.New("signing key must be at least 32 bytes")
	}
	return &Issuer{key: key, ttl: ttl, now: time.Now}, nil
}

// Issue signs a token for p.
func (i *Issuer) Issue(p Principal) (string, error) {
	if _, err := ParseRole(string(p.Role)); err != nil {
		return "", err
	}
	if p.Subject == "" || p.Team == "" {
		return "", errors.New("subject and team are required")
	}
	now := i.now()
	c := claims{Role: p.Role, Team: p.Team, RegisteredClaims: jwt.RegisteredClaims{
		Issuer: issuer, Subject: p.Subject,
		IssuedAt: jwt.NewNumericDate(now), ExpiresAt: jwt.NewNumericDate(now.Add(i.ttl)),
	}}
	return jwt.NewWithClaims(jwt.SigningMethodHS256, c).SignedString(i.key)
}

// Verify checks signature, issuer, expiry and role.
func (i *Issuer) Verify(token string) (Principal, error) {
	var c claims
	_, err := jwt.ParseWithClaims(token, &c, func(*jwt.Token) (any, error) { return i.key, nil },
		jwt.WithValidMethods([]string{"HS256"}), jwt.WithIssuer(issuer), jwt.WithExpirationRequired(),
		jwt.WithTimeFunc(i.now))
	if err != nil {
		return Principal{}, err
	}
	if _, err := ParseRole(string(c.Role)); err != nil {
		return Principal{}, err
	}
	if c.Subject == "" || c.Team == "" {
		return Principal{}, errors.New("token lacks subject or team")
	}
	return Principal{Subject: c.Subject, Role: c.Role, Team: c.Team}, nil
}

type ctxKey struct{}

// WithPrincipal stores p in ctx.
func WithPrincipal(ctx context.Context, p Principal) context.Context {
	return context.WithValue(ctx, ctxKey{}, p)
}

// From returns the caller, or Anonymous.
func From(ctx context.Context) Principal {
	if p, ok := ctx.Value(ctxKey{}).(Principal); ok {
		return p
	}
	return Anonymous
}

// Middleware resolves the bearer token, if any. A malformed or invalid token is
// rejected outright rather than downgraded to anonymous.
func (i *Issuer) Middleware(onError func(http.ResponseWriter, *http.Request, error)) func(http.Handler) http.Handler {
	return func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			h := r.Header.Get("Authorization")
			if h == "" {
				next.ServeHTTP(w, r)
				return
			}
			tok, ok := strings.CutPrefix(h, "Bearer ")
			if !ok {
				onError(w, r, errors.New("authorization header must be 'Bearer <token>'"))
				return
			}
			p, err := i.Verify(strings.TrimSpace(tok))
			if err != nil {
				onError(w, r, err)
				return
			}
			next.ServeHTTP(w, r.WithContext(WithPrincipal(r.Context(), p)))
		})
	}
}
