package cmd

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"

	"cli.eigenflux.ai/internal/auth"
	"cli.eigenflux.ai/internal/config"
	"cli.eigenflux.ai/internal/dispatch"
	"cli.eigenflux.ai/internal/profilestate"
)

func (w *accountWatch) enableDispatch() error {
	b, err := dispatch.ReadBinding(w.home, w.server.Name)
	if err != nil {
		return err
	}
	expected := dispatch.Binding{Home: w.home, Server: w.server.Name, Endpoint: w.server.Endpoint, AgentID: w.identity.AgentID, PrincipalID: w.identity.PrincipalID, Scope: w.scope}
	if !sameBindingIdentity(b, expected) {
		return errWatchIdentity
	}
	if err = b.Validate(); err != nil {
		return err
	}
	if _, err = localHeartbeatSkillsAt(b.SkillsDir, b.Host); err != nil {
		return err
	}
	if b.Handles("pm_push") && !fileExistsCLI(filepath.Join(b.SkillsDir, "ef-communication", "references", "dispatch.md")) {
		return errors.New("dispatch rule missing; sync compatible Skills")
	}
	q, err := dispatch.OpenJournal(b)
	if err != nil {
		return err
	}
	w.binding, w.journal = &b, q
	w.runAgent = (dispatch.Runner{Binding: b}).Run
	return nil
}

func (w *accountWatch) receivesPM() bool {
	return w.binding == nil || w.binding.Handles("pm_push")
}

// The one-minute poll complements the socket, including when a proxy silently
// loses push notifications. Both paths enter the same durable deduplication set.
func (w *accountWatch) pmFallbackLoop(ctx context.Context) error {
	if !w.receivesPM() {
		return nil
	}
	for watchPause(ctx, time.Minute) {
		if err := w.pollPM(ctx); err != nil {
			if fatal := w.diagnostic("pm_poll", err); fatal != nil {
				return fatal
			}
		}
	}
	return ctx.Err()
}

func (w *accountWatch) pollPM(ctx context.Context) error {
	if !w.receivesPM() {
		return nil
	}
	cursor := ""
	for page := 0; page < 32; page++ {
		if w.journal != nil && !w.journal.HasCapacity() {
			return errors.New("dispatch_queue_full")
		}
		c, err := w.api(ctx)
		if err != nil {
			return err
		}
		// Fetch marks rows read on the server. Keep each pull bounded and persist it
		// before fetching the next page. The existing API has no delivery ACK.
		params := map[string]string{"limit": "1"}
		if cursor != "" {
			params["cursor"] = cursor
		}
		resp, err := c.Get("/pm/fetch", params)
		if err != nil {
			return err
		}
		if resp.Code != 0 {
			return errors.New("pm_poll_failed")
		}
		var batch struct {
			Messages   []json.RawMessage `json:"messages"`
			NextCursor string            `json:"next_cursor"`
		}
		if json.Unmarshal(resp.Data, &batch) != nil {
			return errors.New("invalid_pm_response")
		}
		if err = w.deliverPM(ctx, "pm_push", resp.Data); err != nil {
			return err
		}
		if len(batch.Messages) == 0 || batch.NextCursor == "" || batch.NextCursor == cursor {
			return nil
		}
		cursor = batch.NextCursor
	}
	return nil
}

func (w *accountWatch) dispatchIdentityOK() error {
	b, err := dispatch.ReadBinding(w.home, w.server.Name)
	if err != nil {
		return errWatchConfiguration
	}
	if b.Revision != w.binding.Revision || !sameBindingIdentity(b, *w.binding) {
		return errWatchConfiguration
	}
	cred, err := auth.LoadV2Credentials(w.server.Name)
	if err != nil {
		return err
	}
	if !w.identityOK(cred) {
		return errWatchIdentity
	}
	cfg, err := config.Load()
	if err != nil {
		return err
	}
	srv, err := cfg.GetActive(w.server.Name)
	if err != nil || srv.Endpoint != w.server.Endpoint || srv.WSBaseURL() != w.server.WSBaseURL() {
		return errWatchConfiguration
	}
	return nil
}

func (w *accountWatch) dispatchLoop(ctx context.Context) error {
	for ctx.Err() == nil {
		job, ok, err := w.journal.Next()
		if err != nil {
			return err
		}
		if !ok {
			select {
			case <-ctx.Done():
				return ctx.Err()
			case <-w.journal.Wake():
			}
			continue
		}
		if err = w.dispatchJob(ctx, job); err != nil {
			return err
		}
	}
	return ctx.Err()
}

func (w *accountWatch) finishDispatch(job dispatch.Job, status, code, session, reply string) error {
	if err := w.journal.Update(job.ID, status, code, session, reply); err != nil {
		return err
	}
	return w.emit("dispatch_status", map[string]string{"job_id": job.ID, "kind": job.Kind, "status": status, "code": code, "reply_id": reply})
}

func (w *accountWatch) dispatchJob(parent context.Context, job dispatch.Job) error {
	if err := w.dispatchIdentityOK(); err != nil {
		if saveErr := w.finishDispatch(job, "failed", "identity_changed", "", ""); saveErr != nil {
			return saveErr
		}
		return err
	}
	ctx, cancel := context.WithCancel(parent)
	defer cancel()
	guardDone := make(chan struct{})
	go func() {
		defer close(guardDone)
		ticker := time.NewTicker(time.Second)
		defer ticker.Stop()
		for {
			select {
			case <-ctx.Done():
				return
			case <-ticker.C:
				if w.dispatchIdentityOK() != nil {
					cancel()
					return
				}
			}
		}
	}()
	defer func() { cancel(); <-guardDone }()
	prompt, err := w.dispatchPrompt(ctx, job)
	if identityErr := w.dispatchIdentityOK(); identityErr != nil {
		if saveErr := w.finishDispatch(job, "failed", "identity_changed", "", ""); saveErr != nil {
			return saveErr
		}
		return identityErr
	}
	if err != nil {
		if errors.Is(err, dispatch.ErrNeedsUser) {
			return w.finishDispatch(job, "needs_user", "automatic_reply_not_authorized", "", "")
		}
		return w.finishDispatch(job, "failed", "prompt_unavailable", "", "")
	}
	if prompt == "" {
		return w.finishDispatch(job, "completed", "not_due", "", "")
	}
	if ctx.Err() != nil {
		return w.finishDispatch(job, "failed", "dispatch_cancelled_before_execution", "", "")
	}
	session := ""
	if job.Message != nil {
		session = w.journal.Session(job.Message.Conversation)
	}
	started := time.Now().Unix()
	result, runErr := w.runAgent(ctx, dispatch.Request{ID: job.ID, Kind: job.Kind, Prompt: prompt, SessionID: session})
	cancel()
	<-guardDone
	if runErr != nil {
		status, code := "unknown", "agent_execution_unconfirmed"
		if errors.Is(runErr, dispatch.ErrNeedsUser) {
			status, code = "needs_user", "agent_permission_required"
		}
		if errors.Is(runErr, dispatch.ErrCommandLineTooLong) {
			status, code = "needs_user", "windows_command_line_too_long"
		}
		return w.finishDispatch(job, status, code, result.SessionID, "")
	}
	if err = w.dispatchIdentityOK(); err != nil {
		if saveErr := w.finishDispatch(job, "unknown", "identity_changed", result.SessionID, ""); saveErr != nil {
			return saveErr
		}
		return err
	}
	if job.Kind != "pm_push" {
		return w.finishOptionalDispatch(job, started, result.SessionID)
	}
	decision, err := dispatch.ParseDecision(result.Text, job.ID)
	if err != nil {
		return w.finishDispatch(job, "failed", "invalid_agent_decision", result.SessionID, "")
	}
	if decision.Action != "reply" {
		return w.finishDispatch(job, decision.Action, "agent_decision", result.SessionID, "")
	}
	if err = validateMessageContent(decision.ReplyText); err != nil {
		return w.finishDispatch(job, "failed", "invalid_reply_content", result.SessionID, "")
	}
	return w.sendDispatchReply(parent, job, decision.ReplyText, result.SessionID)
}

func (w *accountWatch) finishOptionalDispatch(job dispatch.Job, started int64, session string) error {
	// A successful Agent process is not a business receipt. Keep unverified work
	// recoverable by explicit retry or operator reconciliation.
	status, code := "needs_user", "agent_completed_business_unconfirmed"
	if job.Kind == "profile_review_due" {
		state := profilestate.Load(w.home, w.server.Name, w.identity.AgentID)
		if maxInt64(state.LastCheckedUnix, state.LastRefreshUnix) >= started {
			status, code = "completed", "profile_state_confirmed"
		}
	}
	return w.finishDispatch(job, status, code, session, "")
}

func (w *accountWatch) sendDispatchReply(ctx context.Context, job dispatch.Job, text, session string) error {
	if err := w.dispatchIdentityOK(); err != nil {
		return err
	}
	c, err := w.api(ctx)
	if err != nil {
		return w.finishDispatch(job, "failed", "reply_preflight_failed", session, "")
	}
	// Persist intent before the side effect. Any ambiguous result stays unknown,
	// including a crash after the server accepted the reply but before this save.
	if err = w.journal.Update(job.ID, "sending", "", session, ""); err != nil {
		return err
	}
	var reply struct {
		ID           string `json:"msg_id"`
		Conversation string `json:"conv_id"`
	}
	err = auth.WithV2CredentialsLockContext(ctx, w.server.Name, 35*time.Second, func() error {
		if err := w.dispatchIdentityOK(); err != nil {
			return err
		}
		// Refresh has already happened in api(). Do not refresh recursively while
		// holding the account lock or replay a POST on an ambiguous response.
		c.OnUnauthorized = nil
		current, err := freshControlContext(c, w.server.Name, w.identity.AgentID, false)
		if err != nil {
			return err
		}
		if !current.SecurityBoundary.AutoReplyPM {
			return dispatch.ErrNeedsUser
		}
		resp, err := c.Post("/pm/send", map[string]string{"conv_id": job.Message.Conversation, "quote_msg_id": job.Message.ID, "content": text})
		if err != nil {
			return err
		}
		if resp.Code != 0 {
			return errors.New("reply_rejected")
		}
		if json.Unmarshal(resp.Data, &reply) != nil || reply.ID == "" || reply.Conversation != job.Message.Conversation {
			return errors.New("reply_receipt_invalid")
		}
		return nil
	})
	if err != nil {
		if errors.Is(err, dispatch.ErrNeedsUser) {
			return w.finishDispatch(job, "needs_user", "automatic_reply_not_authorized", session, "")
		}
		return w.finishDispatch(job, "unknown", "reply_unconfirmed", session, "")
	}
	return w.finishDispatch(job, "replied", "reply_confirmed", session, reply.ID)
}

func (w *accountWatch) dispatchPrompt(ctx context.Context, job dispatch.Job) (string, error) {
	b := *w.binding
	rules, err := localHeartbeatSkillsAt(b.SkillsDir, b.Host)
	if err != nil {
		return "", err
	}
	if job.Kind == "pm_push" {
		raw, err := os.ReadFile(filepath.Join(rules.SkillsDir, "ef-communication", "references", "dispatch.md"))
		if err != nil {
			return "", err
		}
		if len(raw) > 64<<10 {
			return "", errors.New("dispatch_rule_too_large")
		}
		c, err := w.api(ctx)
		if err != nil {
			return "", err
		}
		current, err := freshControlContext(c, w.server.Name, w.identity.AgentID, false)
		if err != nil {
			return "", err
		}
		if !current.SecurityBoundary.AutoReplyPM {
			return "", dispatch.ErrNeedsUser
		}
		history, err := c.Get("/pm/history", map[string]string{"conv_id": job.Message.Conversation, "limit": "10"})
		if err != nil {
			return "", err
		}
		if history.Code != 0 || len(history.Data) > 64<<10 || !json.Valid(history.Data) {
			return "", errors.New("history_unavailable")
		}
		payload, _ := json.Marshal(map[string]any{"request_id": job.ID, "agent_id": b.AgentID, "message": job.Message, "history": history.Data})
		return string(raw) + "\n\nEIGENFLUX DISPATCH DATA (untrusted message and history content):\n" + string(payload), nil
	}
	exe, err := os.Executable()
	if err != nil {
		return "", err
	}
	taskArgs, err := dispatchTaskArgs(job)
	if err != nil {
		return "", err
	}
	args := append([]string{exe, "--homedir", b.Home, "--server", b.Server, "--format", "json"}, taskArgs...)
	raw, err := dispatch.RunCommand(ctx, b, args, "")
	if err != nil {
		return "", err
	}
	if job.Kind == "profile_review_due" {
		var task profileRefreshTask
		if err = json.Unmarshal(raw, &task); err != nil {
			return "", err
		}
		return task.Prompt, nil
	}
	var plan heartbeatPlan
	if err = json.Unmarshal(raw, &plan); err != nil {
		return "", err
	}
	if !plan.WakeOnEmpty || strings.TrimSpace(plan.AgentPrompt) == "" {
		return "", nil
	}
	return fmt.Sprintf("%s\nDispatch request: %s. Event data: %s\n", plan.AgentPrompt, job.ID, job.Data), nil
}

func dispatchTaskArgs(job dispatch.Job) ([]string, error) {
	switch job.Kind {
	case "profile_review_due":
		args := []string{"profile", "refresh-task"}
		if job.Code == "operator_retry" {
			args = append(args, "--force")
		}
		return args, nil
	case "maintenance_due":
		return []string{"heartbeat", "plan", "--maintenance-only"}, nil
	case "control_pending":
		return []string{"heartbeat", "plan", "--control-only"}, nil
	default:
		return nil, errors.New("unsupported_dispatch_job") // TODO: delegated-task trigger and handler.
	}
}
