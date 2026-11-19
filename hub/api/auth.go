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

// validSpectator reports whether the request carries the configured spectator
// token, comparing in constant time. A Hub without HUB_SPECTATOR_TOKEN has no
// spectators at all, so the answer is false rather than "any token matches".
func (s *Server) validSpectator(r *http.Request) bool {
	if s.SpectatorToken == "" {
		return false
	}
	return subtle.ConstantTimeCompare(
		[]byte(r.Header.Get("X-Spectator-Token")),
		[]byte(s.SpectatorToken),
	) == 1
}

// spectatorReadable reports whether this path is a read-only session view.
//
// The omniscient stream already tells a spectator everything these two routes
// return, so gating them behind an owner token granted strictly less access than
// the stream it accompanied: a spectator could receive every event and still not
// know the seat names, the metrics or the round. That is an inconsistency, not a
// boundary, and it is why these reads accept the spectator token.
func spectatorReadable(path string) bool {
	parts := strings.Split(strings.Trim(path, "/"), "/")
	if len(parts) < 3 || parts[0] != "hub" || parts[1] != "sessions" || parts[2] == "" {
		return false
	}
	if len(parts) == 3 {
		return true
	}
	return len(parts) == 4 && parts[3] == "snapshot"
}

// publicExempt reports whether a request is one of the three documented routes
// that authenticate themselves and therefore bypass the owner bearer gate
// (ADR-0009 §2.1 addendum, fluxspace-contracts.md §3):
//
//	GET /health                       open liveness check
//	GET /hub/sessions/{id}/events     per-seat X-Seat-Token
//	GET /hub/sessions/{id}/spectate   X-Spectator-Token
//
// The match is exact and restricted to GET. An earlier version used three
// method-agnostic suffix checks, so a POST/PUT/DELETE to /health or to any path
// ending in /events or /spectate skipped the token gate entirely (it reached the
// mux as an unauthenticated 405). Default-deny requires every exemption to be
// deliberate: anything not listed here falls through to the bearer-token check.
func publicExempt(method, path string) bool {
	if method != http.MethodGet {
		return false
	}
	if path == "/health" {
		return true
	}
	return sessionStreamPath(path, "events") || sessionStreamPath(path, "spectate")
}

// sessionStreamPath reports whether path is /hub/sessions/{id}/<leaf> with a
// non-empty id. It replaces the loose strings.HasSuffix exemption so a path such
// as /anything/events cannot inherit the stream routes' exemption.
func sessionStreamPath(path, leaf string) bool {
	parts := strings.Split(strings.Trim(path, "/"), "/")
	return len(parts) == 4 && parts[0] == "hub" && parts[1] == "sessions" &&
		parts[2] != "" && parts[3] == leaf
}

// WithAuth wraps the mux with owner authentication.
//
// Three GET routes keep their own mechanisms and are exempt (see publicExempt):
//   - /health, so a deployment can be checked with one curl and no credential;
//   - /hub/sessions/{id}/spectate, which requires X-Spectator-Token and admits
//     non-owners;
//   - /hub/sessions/{id}/events, which requires the per-seat stream token
//     issued at join.
//
// Two read-only session views are additionally reachable with a valid spectator
// token (GET only, compared in constant time), because a spectator who can watch
// every event must be able to read the view those events describe. Writes stay
// owner-only: the spectator token reads the theatre, it does not play in it.
//
// Everything else — every write and every privileged read — is default-denied:
// it requires a bearer token when the Hub is authenticated. A request that
// carries no token is 401, not 403: the caller has not identified itself, which
// is a different failure from being identified and refused.
func WithAuth(next http.Handler, srv *Server) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if !srv.authenticated() {
			next.ServeHTTP(w, r)
			return
		}
		path := r.URL.Path
		if publicExempt(r.Method, path) {
			next.ServeHTTP(w, r)
			return
		}
		if r.Method == http.MethodGet && spectatorReadable(path) && srv.validSpectator(r) {
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
