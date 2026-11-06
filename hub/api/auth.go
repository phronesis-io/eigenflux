package api

import (
	"context"
	"crypto/subtle"
	"net/http"
	"strings"

	"fluxspace.ai/hub/apperr"
)

// Owner tokens are the Hub's only proof of "who is calling" (ADR-0009 §2.1).
//
// Server.OwnerTokens maps a bearer token to the owner id it authenticates. An
// empty map means the Hub is running unauthenticated: main refuses to start that
// way unless HUB_ALLOW_UNAUTHENTICATED is set explicitly, so the insecure mode is
// always a decision rather than a default.
//
// The map is token → owner, not owner → token, because the lookup goes that way
// on every request. It is reversed once at startup instead of per request.
type ownerContextKey struct{}

// ownerFrom returns the authenticated owner attached by WithAuth, or "" when the
// Hub runs unauthenticated.
func ownerFrom(r *http.Request) string {
	owner, _ := r.Context().Value(ownerContextKey{}).(string)
	return owner
}

// authenticated reports whether this Server was configured with any token.
func (s *Server) authenticated() bool { return len(s.OwnerTokens) > 0 }

// ownerForToken resolves a bearer token to its owner.
//
// Every token is compared, without an early return, using a constant-time
// comparison: returning as soon as a match is found would leak, through timing,
// how many tokens the Hub holds and roughly where the presented one falls.
func (s *Server) ownerForToken(token string) (string, bool) {
	if token == "" {
		return "", false
	}
	var (
		matched string
		found   int
	)
	for candidate, owner := range s.OwnerTokens {
		if subtle.ConstantTimeCompare([]byte(token), []byte(candidate)) == 1 {
			matched = owner
			found = 1
		}
	}
	return matched, found == 1
}

// WithAuth wraps the mux with owner authentication.
//
// Three routes keep their own mechanisms and are exempt:
//   - /health, so a deployment can be checked with one curl and no credential;
//   - /spectate, which requires X-Spectator-Token and admits non-owners;
//   - /events, which requires the per-seat stream token issued at join.
//
// Everything else under /hub/ requires a bearer token when the Hub is
// authenticated. A request that carries no token is 401, not 403: the caller has
// not identified itself, which is a different failure from being identified and
// refused.
func WithAuth(next http.Handler, srv *Server) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if !srv.authenticated() {
			next.ServeHTTP(w, r)
			return
		}
		path := r.URL.Path
		if path == "/health" || strings.HasSuffix(path, "/spectate") || strings.HasSuffix(path, "/events") {
			next.ServeHTTP(w, r)
			return
		}
		token := bearerToken(r)
		owner, ok := srv.ownerForToken(token)
		if !ok {
			if token == "" {
				writeDomainError(w, apperr.New(http.StatusUnauthorized, "AUTH_REQUIRED",
					"this Hub requires an owner bearer token; X-Spectator-Token and seat stream tokens cover only their own routes"))
				return
			}
			writeDomainError(w, apperr.New(http.StatusUnauthorized, "AUTH_INVALID",
				"the bearer token does not match any configured owner"))
			return
		}
		next.ServeHTTP(w, r.WithContext(context.WithValue(r.Context(), ownerContextKey{}, owner)))
	})
}

func bearerToken(r *http.Request) string {
	header := r.Header.Get("Authorization")
	const prefix = "Bearer "
	if len(header) <= len(prefix) || !strings.EqualFold(header[:len(prefix)], prefix) {
		return ""
	}
	return strings.TrimSpace(header[len(prefix):])
}

// authorizeCompanion decides whether the caller may act on companionID.
//
// The registry is consulted before any tier, budget or approval, so a refusal
// here cannot be confused with a governance decision — and an unknown companion
// is 404 rather than 403, because "this companion does not exist here" and "it
// exists but is not yours" are different answers and merging them hides a
// configuration mistake behind an authorisation failure.
//
// When the Hub runs unauthenticated there is no caller to check, so the registry
// is a record rather than a gate. That is the honest behaviour of an anonymous
// Hub, and it is why main makes that mode explicit.
func (s *Server) authorizeCompanion(w http.ResponseWriter, r *http.Request, gov *Governance, companionID string) bool {
	owner := ownerFrom(r)
	if owner == "" {
		return true
	}
	binding, ok := gov.Owners.Get(companionID)
	if !ok {
		writeDomainError(w, apperr.New(http.StatusNotFound, "COMPANION_NOT_REGISTERED",
			"companion %q is not bound to an owner; bind it with POST /hub/companions first", companionID))
		return false
	}
	if binding.OwnerID != owner {
		writeDomainError(w, apperr.New(http.StatusForbidden, "COMPANION_NOT_YOURS",
			"companion %q belongs to a different owner", companionID))
		return false
	}
	return true
}
