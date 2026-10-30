package cmd

import (
	"errors"
	"os"
	"strings"
	"sync"

	"cli.eigenflux.ai/internal/auth"
	"cli.eigenflux.ai/internal/config"
)

const watchSupervisedEnv = "EIGENFLUX_WATCH_SUPERVISED"
const watchAgentEnv = "EIGENFLUX_WATCH_EXPECTED_AGENT"
const watchPrincipalEnv = "EIGENFLUX_WATCH_EXPECTED_PRINCIPAL"

var errWatchCLIUpdated = errors.New("watch CLI updated: ready for adoption")

// Each destructive PM pull through durable intake, and each claimed journal
// entry through its business writes, holds one admission.
// Closing admission never cancels work already admitted.
type watchHandoff struct {
	mu      sync.Mutex
	pending string
	active  int
	done    chan struct{}
	closed  bool
}

func newWatchHandoff() *watchHandoff { return &watchHandoff{done: make(chan struct{})} }
func (h *watchHandoff) begin() bool {
	if h == nil {
		return true
	}
	h.mu.Lock()
	defer h.mu.Unlock()
	if h.pending != "" {
		return false
	}
	h.active++
	return true
}
func (h *watchHandoff) end() {
	if h == nil {
		return
	}
	h.mu.Lock()
	defer h.mu.Unlock()
	h.active--
	h.ready()
}
func (h *watchHandoff) request(version string) {
	if h == nil {
		return
	}
	h.mu.Lock()
	defer h.mu.Unlock()
	if h.pending == "" {
		h.pending = version
	}
	h.ready()
}
func (h *watchHandoff) ready() {
	if h.pending != "" && h.active == 0 && !h.closed {
		h.closed = true
		close(h.done)
	}
}
func (h *watchHandoff) version() string {
	h.mu.Lock()
	defer h.mu.Unlock()
	return h.pending
}

func watchExpectedIdentity(c *auth.V2Credentials) error {
	if agent := os.Getenv(watchAgentEnv); agent != "" && (c == nil || agent != c.AgentID || os.Getenv(watchPrincipalEnv) != c.PrincipalID) {
		return errWatchIdentity
	}
	return nil
}

func (w *accountWatch) verifyHandoffIdentity() error {
	if w.notificationsOnly {
		return w.notificationIdentityOK()
	}
	credentials, err := auth.LoadV2Credentials(w.server.Name)
	if err != nil {
		return err
	}
	if !w.identityOK(credentials) {
		return errWatchIdentity
	}
	if w.binding != nil {
		return w.commissionIdentityOK()
	}
	cfg, err := config.Load()
	if err != nil {
		return err
	}
	server, err := cfg.GetActive(w.server.Name)
	if err != nil || server.Endpoint != w.server.Endpoint || server.WSBaseURL() != w.server.WSBaseURL() {
		return errWatchConfiguration
	}
	return nil
}

func watchReexecEnvironment(env []string, home string, identity auth.V2Credentials) []string {
	out := make([]string, 0, len(env)+3)
	for _, value := range env {
		skip := false
		for _, key := range []string{"EIGENFLUX_HOME", watchAgentEnv, watchPrincipalEnv, updateReexecEnv, updateAttemptEnv, updateIdentityEnv, updateFromVersionEnv} {
			if strings.HasPrefix(value, key+"=") {
				skip = true
				break
			}
		}
		if !skip {
			out = append(out, value)
		}
	}
	return append(out, "EIGENFLUX_HOME="+home, watchAgentEnv+"="+identity.AgentID, watchPrincipalEnv+"="+identity.PrincipalID)
}
