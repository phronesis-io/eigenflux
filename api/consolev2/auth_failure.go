package consolev2

import (
	"context"
	"errors"
	"fmt"
	"net/http"

	"github.com/cloudwego/hertz/pkg/app"
	"gorm.io/gorm"
	gormlogger "gorm.io/gorm/logger"

	"eigenflux_server/pkg/logger"
)

var errRefreshExpired = errors.New("refresh credential expired")

// authenticationFailure records only server-owned classifications and identifiers.
// Credential material and database error messages must never reach this log.
type authenticationFailure struct {
	ctx                             context.Context
	stage                           string
	agentID, principalID, sessionID int64
	consoleSessionID                string
}

func (f authenticationFailure) log(status int, code, reason string, err error) {
	fields := []interface{}{"stage", f.stage, "reason", reason, "status", status, "code", code}
	if f.agentID != 0 {
		fields = append(fields, "agent_id", f.agentID)
	}
	if f.principalID != 0 {
		fields = append(fields, "principal_id", f.principalID)
	}
	if f.sessionID != 0 {
		fields = append(fields, "session_id", f.sessionID)
	}
	if f.consoleSessionID != "" {
		fields = append(fields, "console_session_id", f.consoleSessionID)
	}
	if err != nil {
		fields = append(fields, "error_type", fmt.Sprintf("%T", err))
		if state := sqlState(err); validSQLState(state) {
			fields = append(fields, "sql_state", state)
		}
	}
	logger.Ctx(f.ctx).Warn("Authentication rejected", fields...)
}

type refreshFailure struct {
	authenticationFailure
	c *app.RequestContext
}

func (f refreshFailure) reject(status int, code, reason, message string, err error, details map[string]interface{}) {
	f.log(status, code, reason, err)
	fail(f.c, status, code, message, details)
}

func validSQLState(state string) bool {
	if len(state) != 5 {
		return false
	}
	for _, c := range state {
		if (c < '0' || c > '9') && (c < 'A' || c > 'Z') {
			return false
		}
	}
	return true
}

func (f refreshFailure) invalid(reason string) {
	f.reject(http.StatusUnauthorized, "REFRESH_INVALID", reason, "refresh credential or proof is invalid", nil, nil)
}

func (f refreshFailure) expired() {
	f.reject(http.StatusUnauthorized, "REFRESH_EXPIRED", "credential_expired", "refresh credential has expired", nil, nil)
}

func (f refreshFailure) reuse() {
	f.reject(http.StatusUnauthorized, "REFRESH_REUSE_DETECTED", "credential_reuse", "refresh credential reuse revoked this session family", nil, nil)
}

func (f refreshFailure) unavailable(reason string, err error) {
	f.reject(http.StatusServiceUnavailable, "AGENT_AUTH_UNAVAILABLE", reason, "Agent authentication is temporarily unavailable", err, nil)
}

// Authentication emits sanitized failure records instead of GORM's raw SQL
// and driver messages, which may contain credential hashes or connection data.
func (s *Service) authenticationDB(ctx context.Context) *gorm.DB {
	return s.db.WithContext(ctx).Session(&gorm.Session{Logger: gormlogger.Discard})
}
