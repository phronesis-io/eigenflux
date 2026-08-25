package consolev2

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"strconv"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/cloudwego/hertz/pkg/app"
	"github.com/lib/pq"
	"gorm.io/gorm"

	"eigenflux_server/pkg/activity"
	"eigenflux_server/pkg/agentcard"
	"eigenflux_server/pkg/logger"
	"eigenflux_server/pkg/mq"
	"eigenflux_server/pkg/runtimeidentity"
	profiledal "eigenflux_server/rpc/profile/dal"
)

type onboardingState struct {
	State                 string `gorm:"column:state" json:"state"`
	CurrentStep           int16  `gorm:"column:current_step" json:"current_step"`
	Revision              int64  `gorm:"column:revision" json:"revision"`
	ActiveContextRevision *int64 `gorm:"column:active_context_revision" json:"active_context_revision"`
	CompletedAt           *int64 `gorm:"column:completed_at" json:"completed_at"`
}

type onboardingDraft struct {
	Revision        int64           `gorm:"column:revision" json:"revision"`
	DraftData       json.RawMessage `gorm:"column:draft_data" json:"data"`
	FieldProvenance json.RawMessage `gorm:"column:field_provenance" json:"field_provenance"`
	ActorType       string          `gorm:"column:actor_type" json:"actor_type"`
	CreatedAt       int64           `gorm:"column:created_at" json:"created_at"`
}

func (s *Service) loadOnboarding(agentID int64) (onboardingState, onboardingDraft, error) {
	var state onboardingState
	if err := s.db.Raw(`SELECT state, current_step, revision, active_context_revision, completed_at
		FROM agent_onboarding_v2 WHERE agent_id = ?`, agentID).Scan(&state).Error; err != nil {
		return state, onboardingDraft{}, err
	}
	if state.State == "" {
		return state, onboardingDraft{}, gorm.ErrRecordNotFound
	}
	var stored struct {
		Revision        int64  `gorm:"column:revision"`
		DraftData       string `gorm:"column:draft_data"`
		FieldProvenance string `gorm:"column:field_provenance"`
		ActorType       string `gorm:"column:actor_type"`
		CreatedAt       int64  `gorm:"column:created_at"`
	}
	err := s.db.Raw(`SELECT revision, draft_data::text AS draft_data,
			field_provenance::text AS field_provenance, actor_type, created_at
		FROM agent_onboarding_drafts WHERE agent_id = ? ORDER BY revision DESC LIMIT 1`, agentID).Scan(&stored).Error
	draft := onboardingDraft{
		Revision: stored.Revision, DraftData: json.RawMessage(stored.DraftData),
		FieldProvenance: json.RawMessage(stored.FieldProvenance), ActorType: stored.ActorType, CreatedAt: stored.CreatedAt,
	}
	if err != nil {
		return state, draft, err
	}
	normalized, normalizeErr := normalizeLoadedOnboardingDraft(draft)
	return state, normalized, normalizeErr
}

func normalizeLoadedOnboardingDraft(draft onboardingDraft) (onboardingDraft, error) {
	normalizedData, draftObject, err := normalizeOnboardingDraftJSON(draft.DraftData)
	if err != nil {
		return draft, err
	}
	provenance := decodeProvenance(draft.FieldProvenance)
	if len(provenance) == 0 && validProvenance(draft.ActorType) {
		provenance = deriveInitialProvenance(draftObject, draft.ActorType, nil, draft.CreatedAt)
	}
	normalizedProvenance, err := json.Marshal(provenance)
	if err != nil {
		return draft, err
	}
	draft.DraftData = normalizedData
	draft.FieldProvenance = normalizedProvenance
	return draft, nil
}

func (s *Service) getConsoleSession(_ context.Context, c *app.RequestContext) {
	id, ok := agentID(c)
	if !ok {
		fail(c, http.StatusUnauthorized, "CONSOLE_SESSION_REQUIRED", "Console V2 session is required", nil)
		return
	}
	state, _, err := s.loadOnboarding(id)
	if err != nil {
		fail(c, http.StatusInternalServerError, "SESSION_READ_FAILED", "could not read onboarding state", nil)
		return
	}
	var identity struct {
		AgentName      string `gorm:"column:agent_name"`
		ShortID        string `gorm:"column:short_id"`
		Bio            string `gorm:"column:bio"`
		CreatedAt      int64  `gorm:"column:created_at"`
		IsOfficial     bool   `gorm:"column:is_official"`
		BoundEmail     string `gorm:"column:bound_email"`
		EmailVerified  bool   `gorm:"column:email_verified"`
		RuntimeName    string `gorm:"column:runtime_name"`
		RuntimeVersion string `gorm:"column:runtime_version"`
		RuntimeMode    string `gorm:"column:runtime_mode"`
		ClientHost     string `gorm:"column:client_host"`
		DeviceName     string `gorm:"column:device_name"`
	}
	if err := s.db.Raw(`SELECT a.agent_name, a.short_id, a.bio, a.created_at, a.is_official,
		COALESCE(b.normalized_email, '') AS bound_email,
		(b.binding_id IS NOT NULL) AS email_verified,
		COALESCE(settings.runtime_name, '') AS runtime_name,
		COALESCE(settings.runtime_version, '') AS runtime_version,
		COALESCE(settings.mode, '') AS runtime_mode,
		COALESCE(settings.client_host, '') AS client_host,
		COALESCE(settings.device_name, '') AS device_name
		FROM agents a
		LEFT JOIN agent_email_bindings b ON b.agent_id = a.agent_id
			AND b.status = 'active' AND b.verification_state = 'verified'
		LEFT JOIN agent_settings settings ON settings.agent_id = a.agent_id
		WHERE a.agent_id = ?`, id).Scan(&identity).Error; err != nil {
		fail(c, http.StatusInternalServerError, "SESSION_READ_FAILED", "could not read Agent identity", nil)
		return
	}
	verificationLevel := "unverified"
	if identity.EmailVerified {
		verificationLevel = "email_verified"
	}
	if identity.IsOfficial {
		verificationLevel = "official"
	}
	runtime, runtimeName, runtimeVersion := consoleSessionRuntime(
		identity.RuntimeName, identity.RuntimeVersion, identity.ClientHost,
	)
	reply(c, http.StatusOK, map[string]interface{}{
		"agent_id":           fmt.Sprintf("%d", id),
		"short_id":           identity.ShortID,
		"eigenflux_id":       "eigenflux#" + identity.ShortID,
		"agent_name":         identity.AgentName,
		"bio":                identity.Bio,
		"created_at":         identity.CreatedAt,
		"email":              identity.BoundEmail,
		"email_bound":        identity.EmailVerified,
		"verification_level": verificationLevel,
		"runtime":            runtime,
		"runtime_name":       runtimeName,
		"runtime_version":    runtimeVersion,
		"runtime_mode":       identity.RuntimeMode,
		"device_name":        identity.DeviceName,
		"onboarding":         state,
	})
}

func consoleSessionRuntime(name, version, clientHost string) (runtime, runtimeName, runtimeVersion string) {
	runtimeName = strings.TrimSpace(name)
	runtimeVersion = strings.TrimSpace(version)
	if runtimeName == "" {
		if identity, ok := runtimeidentity.Parse(clientHost); ok {
			runtimeName, runtimeVersion = identity.Name, identity.Version
		}
	}
	if runtimeName == "" {
		return "", "", ""
	}
	runtime = runtimeName
	if runtimeVersion != "" {
		runtime += "/" + runtimeVersion
	}
	return runtime, runtimeName, runtimeVersion
}

func (s *Service) deleteConsoleSession(_ context.Context, c *app.RequestContext) {
	sessionValue, ok := c.Get("console_session_id")
	sessionID, typeOK := sessionValue.(string)
	if !ok || !typeOK || sessionID == "" {
		fail(c, http.StatusUnauthorized, "CONSOLE_SESSION_REQUIRED", "Console V2 session is required", nil)
		return
	}
	now := time.Now().UnixMilli()
	if err := s.db.Exec(`UPDATE console_v2_sessions SET status = 'revoked', revoked_at = ?
		WHERE session_id = ? AND status = 'active'`, now, sessionID).Error; err != nil {
		fail(c, http.StatusInternalServerError, "LOGOUT_FAILED", "could not revoke Console V2 session", nil)
		return
	}
	s.setConsoleCookie(c, "", -1)
	s.setCSRFCookie(c, "", -1)
	reply(c, http.StatusOK, map[string]interface{}{"revoked": true})
}

type putDraftRequest struct {
	ExpectedRevision int64             `json:"expected_revision"`
	IdempotencyKey   string            `json:"idempotency_key"`
	Draft            json.RawMessage   `json:"draft"`
	FieldProvenance  map[string]string `json:"field_provenance,omitempty"`
}

type putDraftResponse struct {
	Revision        int64                      `json:"revision"`
	FieldProvenance map[string]fieldProvenance `json:"field_provenance"`
	BlockedPaths    []string                   `json:"blocked_paths"`
}

func (s *Service) putOnboardingDraft(_ context.Context, c *app.RequestContext) {
	id, ok := agentID(c)
	if !ok {
		fail(c, http.StatusUnauthorized, "AGENT_AUTH_REQUIRED", "Agent V2 authentication is required", nil)
		return
	}
	var req putDraftRequest
	if err := decodeBody(c, &req); err != nil {
		fail(c, http.StatusBadRequest, "INVALID_REQUEST", err.Error(), nil)
		return
	}
	if req.ExpectedRevision <= 0 || len(req.IdempotencyKey) == 0 || len(req.IdempotencyKey) > 128 {
		fail(c, http.StatusBadRequest, "INVALID_REQUEST", "expected_revision and idempotency_key are required", nil)
		return
	}
	if len(req.Draft) == 0 || len(req.Draft) > 64<<10 {
		fail(c, http.StatusBadRequest, "INVALID_DRAFT", "draft must be a JSON object no larger than 64KB", nil)
		return
	}
	normalizedDraft, draftObject, normalizeErr := normalizeOnboardingDraftJSON(req.Draft)
	if normalizeErr != nil {
		fail(c, http.StatusBadRequest, "INVALID_DRAFT", normalizeErr.Error(), nil)
		return
	}
	req.Draft = normalizedDraft
	actorType := "agent_prefill"
	if _, console := c.Get("console_session_id"); console {
		actorType = "human_edit"
	}
	if actorType == provenanceAgent {
		if err := validateRequestedAgentProvenance(req.FieldProvenance); err != nil {
			fail(c, http.StatusBadRequest, "INVALID_FIELD_PROVENANCE", err.Error(), nil)
			return
		}
	} else {
		req.FieldProvenance = nil
	}
	provenanceRequestJSON, _ := json.Marshal(req.FieldProvenance)
	requestHash := hashString(fmt.Sprintf("%d:%s:%s:%s", req.ExpectedRevision, req.Draft, provenanceRequestJSON, actorType))
	response := putDraftResponse{FieldProvenance: map[string]fieldProvenance{}, BlockedPaths: []string{}}
	now := time.Now().UnixMilli()
	err := s.db.Transaction(func(tx *gorm.DB) error {
		var prior struct {
			RequestHash string `gorm:"column:request_hash"`
			Response    string `gorm:"column:response_snapshot"`
		}
		if err := tx.Raw(`SELECT request_hash, response_snapshot::text AS response_snapshot FROM agent_idempotency_requests
			WHERE agent_id = ? AND operation = 'onboarding_draft_put' AND idempotency_key = ?`, id, req.IdempotencyKey).Scan(&prior).Error; err != nil {
			return err
		}
		if prior.RequestHash != "" {
			if prior.RequestHash != requestHash {
				return errConflict
			}
			return json.Unmarshal([]byte(prior.Response), &response)
		}
		var row onboardingState
		if err := tx.Raw(`SELECT state, current_step, revision FROM agent_onboarding_v2
			WHERE agent_id = ? FOR UPDATE`, id).Scan(&row).Error; err != nil {
			return err
		}
		if row.State == "completed" || row.Revision != req.ExpectedRevision {
			return errConflict
		}
		var stored struct {
			DraftData       string `gorm:"column:draft_data"`
			FieldProvenance string `gorm:"column:field_provenance"`
			ActorType       string `gorm:"column:actor_type"`
		}
		if err := tx.Raw(`SELECT draft_data::text AS draft_data,
				field_provenance::text AS field_provenance, actor_type
			FROM agent_onboarding_drafts WHERE agent_id = ?
			ORDER BY revision DESC LIMIT 1`, id).Scan(&stored).Error; err != nil {
			return err
		}
		previous, err := decodeJSONObject(json.RawMessage(stored.DraftData))
		if err != nil {
			return err
		}
		incoming := draftObject
		provenance := decodeProvenance(json.RawMessage(stored.FieldProvenance))
		if len(provenance) == 0 && validProvenance(stored.ActorType) {
			provenance = deriveInitialProvenance(previous, stored.ActorType, nil, now)
		}
		merged, provenance, blockedPaths := mergeOnboardingDraft(previous, incoming, provenance, actorType, req.FieldProvenance, now)
		mergedJSON, err := json.Marshal(merged)
		if err != nil {
			return err
		}
		provenanceJSON, err := json.Marshal(provenance)
		if err != nil {
			return err
		}
		newRevision := row.Revision + 1
		response = putDraftResponse{
			Revision: newRevision, FieldProvenance: provenance, BlockedPaths: blockedPaths,
		}
		if err := tx.Exec(`INSERT INTO agent_onboarding_drafts
			(agent_id, revision, draft_data, field_provenance, actor_type, request_id, created_at)
			VALUES (?, ?, ?::jsonb, ?::jsonb, ?, ?, ?)`, id, newRevision,
			string(mergedJSON), string(provenanceJSON), actorType, req.IdempotencyKey, now).Error; err != nil {
			return err
		}
		if res := tx.Exec(`UPDATE agent_onboarding_v2 SET revision = ?, updated_at = ?
			WHERE agent_id = ? AND revision = ?`, newRevision, now, id, row.Revision); res.Error != nil || res.RowsAffected != 1 {
			return errConflict
		}
		snapshot, _ := json.Marshal(response)
		return tx.Exec(`INSERT INTO agent_idempotency_requests
			(agent_id, operation, idempotency_key, request_hash, response_snapshot, expires_at, created_at)
			VALUES (?, 'onboarding_draft_put', ?, ?, ?::jsonb, ?, ?)`, id, req.IdempotencyKey,
			requestHash, string(snapshot), now+int64(24*time.Hour/time.Millisecond), now).Error
	})
	if err != nil && (errors.Is(err, errConflict) || isUniqueViolation(err)) {
		found, hashConflict, replayErr := s.loadIdempotentResponse(id, "onboarding_draft_put", req.IdempotencyKey, requestHash, &response)
		switch {
		case replayErr != nil:
			err = replayErr
		case found && hashConflict:
			err = errConflict
		case found:
			err = nil
		}
	}
	if errors.Is(err, errConflict) {
		fail(c, http.StatusConflict, "REVISION_CONFLICT", "draft changed or idempotency key was reused with a different request", nil)
		return
	}
	if err != nil {
		fail(c, http.StatusInternalServerError, "DRAFT_SAVE_FAILED", "could not save onboarding draft", nil)
		return
	}
	reply(c, http.StatusOK, response)
}

func (s *Service) getOnboardingDraft(_ context.Context, c *app.RequestContext) {
	id, ok := agentID(c)
	if !ok {
		fail(c, http.StatusUnauthorized, "CONSOLE_SESSION_REQUIRED", "Console V2 session is required", nil)
		return
	}
	state, draft, err := s.loadOnboarding(id)
	if err != nil {
		fail(c, http.StatusInternalServerError, "DRAFT_READ_FAILED", "could not read onboarding draft", nil)
		return
	}
	reply(c, http.StatusOK, map[string]interface{}{"onboarding": state, "draft": draft})
}

type confirmStepRequest struct {
	Step                       int16  `json:"step"`
	ExpectedOnboardingRevision int64  `json:"expected_onboarding_revision"`
	IdempotencyKey             string `json:"idempotency_key"`
}

type identityCardDraft struct {
	AgentName         string   `json:"agent_name"`
	Bio               string   `json:"bio"`
	AgentDescription  string   `json:"agent_description"`
	HumanDescription  *string  `json:"human_description"`
	WorkingLanguages  []string `json:"working_languages"`
	Seeking           []string `json:"seeking"`
	Offering          []string `json:"offering"`
	Geo               *string  `json:"geo"`
	Timezone          *string  `json:"timezone"`
	AgentStatus       []string `json:"agent_status"`
	HumanStatus       []string `json:"human_status"`
	InterestsNegative []string `json:"interests_negative"`
}

type draftPayload struct {
	IdentityCard     identityCardDraft `json:"identity_card"`
	SecurityBoundary struct {
		RecurringPublish bool `json:"recurring_publish"`
		AutoReplyPM      bool `json:"auto_reply_pm"`
		AutoComment      bool `json:"auto_comment"`
		ShowAddFriend    bool `json:"show_add_friend"`
	} `json:"security_boundary"`
	NetworkGoal   string `json:"network_goal"`
	IntentActions []struct {
		WatchFor          string `json:"watch_for"`
		TriggerWhen       string `json:"trigger_when"`
		ActionInstruction string `json:"action_instruction"`
		ActionPolicy      string `json:"action_policy"`
		Priority          int16  `json:"priority"`
	} `json:"intent_actions"`
}

var (
	errInvalidOnboardingDraft = errors.New("invalid onboarding draft")
	errEmailBindingRequired   = errors.New("verified email binding required")
)

func validateDraftPayload(payload draftPayload) error {
	if utf8.RuneCountInString(payload.IdentityCard.AgentName) > 100 || utf8.RuneCountInString(payload.IdentityCard.Bio) > 2000 {
		return errors.New("identity card exceeds its length limit")
	}
	if utf8.RuneCountInString(payload.NetworkGoal) > 2000 {
		return errors.New("network goal exceeds 2000 characters")
	}
	if len(payload.IntentActions) > 10 {
		return errors.New("at most 10 intent actions are allowed")
	}
	for _, intent := range payload.IntentActions {
		if err := validateIntent(IntentWriteFields{
			WatchFor: intent.WatchFor, TriggerWhen: intent.TriggerWhen,
			ActionInstruction: intent.ActionInstruction, ActionPolicy: intent.ActionPolicy,
			Priority: intent.Priority,
		}); err != nil {
			return err
		}
	}
	return nil
}

func validateDraftStep(payload draftPayload, step int16) error {
	switch step {
	case 2:
		description := payload.IdentityCard.AgentDescription
		if description == "" {
			description = payload.IdentityCard.Bio
		}
		if err := validateIdentityCardFields(payload.IdentityCard.AgentName, description); err != nil {
			return fmt.Errorf("%w: %v", errInvalidOnboardingDraft, err)
		}
		identityFields := map[string]interface{}{}
		for name, value := range map[string]*string{
			"human_description": payload.IdentityCard.HumanDescription,
			"geo":               payload.IdentityCard.Geo,
			"timezone":          payload.IdentityCard.Timezone,
		} {
			if value != nil {
				identityFields[name] = *value
			}
		}
		for name, value := range map[string][]string{
			"working_languages":  payload.IdentityCard.WorkingLanguages,
			"seeking":            payload.IdentityCard.Seeking,
			"offering":           payload.IdentityCard.Offering,
			"agent_status":       payload.IdentityCard.AgentStatus,
			"human_status":       payload.IdentityCard.HumanStatus,
			"interests_negative": payload.IdentityCard.InterestsNegative,
		} {
			// Older drafts did not include these additive Card fields. Missing
			// must remain valid; an explicit empty array is still persisted.
			if value != nil {
				identityFields[name] = value
			}
		}
		for name, value := range identityFields {
			spec, ok := agentcard.LookupField(name)
			if !ok {
				return fmt.Errorf("%w: unsupported identity field %s", errInvalidOnboardingDraft, name)
			}
			raw, _ := json.Marshal(value)
			normalized, err := agentcard.ValidateValue(spec, raw)
			if err != nil {
				return fmt.Errorf("%w: %v", errInvalidOnboardingDraft, err)
			}
			if err := agentcard.ValidateConsoleV2Value(name, normalized); err != nil {
				return fmt.Errorf("%w: %v", errInvalidOnboardingDraft, err)
			}
			if spec.Public {
				if err := agentcard.ValidatePublicContent(spec, normalized); err != nil {
					return fmt.Errorf("%w: %v", errInvalidOnboardingDraft, err)
				}
			}
		}
	case 3:
		if strings.TrimSpace(payload.NetworkGoal) == "" {
			return fmt.Errorf("%w: network_goal is required", errInvalidOnboardingDraft)
		}
		if utf8.RuneCountInString(payload.NetworkGoal) > 2000 {
			return fmt.Errorf("%w: network goal exceeds 2000 characters", errInvalidOnboardingDraft)
		}
	case 4:
		if len(payload.IntentActions) > 10 {
			return fmt.Errorf("%w: at most 10 intent actions are allowed", errInvalidOnboardingDraft)
		}
		for _, intent := range payload.IntentActions {
			if err := validateIntent(IntentWriteFields{
				WatchFor: intent.WatchFor, TriggerWhen: intent.TriggerWhen,
				ActionInstruction: intent.ActionInstruction, ActionPolicy: intent.ActionPolicy,
				Priority: intent.Priority,
			}); err != nil {
				return fmt.Errorf("%w: %v", errInvalidOnboardingDraft, err)
			}
		}
	case 5:
		return nil
	default:
		return fmt.Errorf("%w: unsupported onboarding step", errInvalidOnboardingDraft)
	}
	return nil
}

func canConfirmOnboardingStep(state string, currentStep, requestedStep int16) bool {
	return state != "completed" && requestedStep >= 2 && requestedStep <= currentStep
}

func nextOnboardingStep(currentStep, confirmedStep int16) int16 {
	if confirmedStep == currentStep && currentStep < 5 {
		return currentStep + 1
	}
	return currentStep
}

func (s *Service) confirmOnboardingStep(ctx context.Context, c *app.RequestContext) {
	id, ok := agentID(c)
	if !ok {
		fail(c, http.StatusUnauthorized, "CONSOLE_SESSION_REQUIRED", "Console V2 session is required", nil)
		return
	}
	var req confirmStepRequest
	if err := decodeBody(c, &req); err != nil || req.Step < 2 || req.Step > 5 || req.ExpectedOnboardingRevision <= 0 || req.IdempotencyKey == "" {
		fail(c, http.StatusBadRequest, "INVALID_REQUEST", "step, expected_onboarding_revision, and idempotency_key are required", nil)
		return
	}
	requestHash := hashString(fmt.Sprintf("%d:%d", req.Step, req.ExpectedOnboardingRevision))
	var response map[string]interface{}
	replayed := false
	confirmedIntentCount := 0
	now := time.Now().UnixMilli()
	err := s.db.Transaction(func(tx *gorm.DB) error {
		var prior struct {
			RequestHash string `gorm:"column:request_hash"`
			Response    string `gorm:"column:response_snapshot"`
		}
		if err := tx.Raw(`SELECT request_hash, response_snapshot::text AS response_snapshot FROM agent_idempotency_requests
			WHERE agent_id = ? AND operation = 'onboarding_confirm' AND idempotency_key = ?`, id, req.IdempotencyKey).Scan(&prior).Error; err != nil {
			return err
		}
		if prior.RequestHash != "" {
			if prior.RequestHash != requestHash {
				return errConflict
			}
			return json.Unmarshal([]byte(prior.Response), &response)
		}
		var state onboardingState
		if err := tx.Raw(`SELECT state, current_step, revision, active_context_revision, completed_at
			FROM agent_onboarding_v2 WHERE agent_id = ? FOR UPDATE`, id).Scan(&state).Error; err != nil {
			return err
		}
		// A user may revisit and re-confirm any already unlocked step. A prior
		// step updates its canonical data but never moves the onboarding cursor
		// backwards; future/locked steps remain rejected.
		if !canConfirmOnboardingStep(state.State, state.CurrentStep, req.Step) || state.Revision != req.ExpectedOnboardingRevision {
			return errConflict
		}
		var emailBound bool
		if err := tx.Raw(`SELECT EXISTS (
			SELECT 1 FROM agent_email_bindings
			WHERE agent_id = ? AND status = 'active' AND verification_state = 'verified'
		)`, id).Scan(&emailBound).Error; err != nil {
			return err
		}
		if !emailBound {
			return errEmailBindingRequired
		}
		var storedDraft struct {
			DraftData       string `gorm:"column:draft_data"`
			FieldProvenance string `gorm:"column:field_provenance"`
			ActorType       string `gorm:"column:actor_type"`
		}
		if err := tx.Raw(`SELECT draft_data::text AS draft_data,
				field_provenance::text AS field_provenance, actor_type
			FROM agent_onboarding_drafts
			WHERE agent_id = ? ORDER BY revision DESC LIMIT 1`, id).Scan(&storedDraft).Error; err != nil {
			return err
		}
		normalizedDraftJSON, rawDraft, err := normalizeOnboardingDraftJSON(json.RawMessage(storedDraft.DraftData))
		if err != nil {
			return fmt.Errorf("%w: %v", errInvalidOnboardingDraft, err)
		}
		var payload draftPayload
		if err := json.Unmarshal(normalizedDraftJSON, &payload); err != nil {
			return fmt.Errorf("%w: %v", errInvalidOnboardingDraft, err)
		}
		if req.Step == 4 {
			confirmedIntentCount = len(payload.IntentActions)
		}
		provenance := decodeProvenance(json.RawMessage(storedDraft.FieldProvenance))
		if len(provenance) == 0 && validProvenance(storedDraft.ActorType) {
			provenance = deriveInitialProvenance(rawDraft, storedDraft.ActorType, nil, now)
		}
		if err := validateDraftStep(payload, req.Step); err != nil {
			return err
		}
		provenance = confirmStepProvenance(rawDraft, provenance, req.Step, now)
		if err := applyConfirmedStep(tx, id, req.Step, payload, provenance, now); err != nil {
			return err
		}
		newRevision := state.Revision + 1
		nextStep := nextOnboardingStep(state.CurrentStep, req.Step)
		newState := "in_progress"
		var contextRevision interface{}
		if req.Step == 5 && req.Step == state.CurrentStep {
			compiled, revision, err := compileAndActivateContext(tx, id, now)
			if err != nil {
				return err
			}
			_ = compiled
			if err := tx.Exec(`UPDATE agent_principals SET status = 'active', last_seen_at = ?
				WHERE agent_id = ? AND status = 'limited' AND revoked_at IS NULL`, now, id).Error; err != nil {
				return err
			}
			if err := tx.Exec(`UPDATE agent_credential_sessions SET scopes = ?
				WHERE principal_id IN (SELECT principal_id FROM agent_principals WHERE agent_id = ?)
				  AND revoked_at IS NULL AND expires_at > ?`, pq.Array([]string{
				"onboarding:write", "context:read", "feed:read", "notifications:ack", "commands:claim",
				"communication:read", "communication:write", "broadcast:write", "trade:write",
				"attention:write", "console:handoff:create",
			}), id, now).Error; err != nil {
				return err
			}
			if err := tx.Exec(`UPDATE agents SET profile_completed_at = COALESCE(profile_completed_at, ?),
				updated_at = ? WHERE agent_id = ?`, now, now, id).Error; err != nil {
				return err
			}
			contextRevision = revision
			newState = "completed"
			nextStep = 5
			if res := tx.Exec(`UPDATE agent_onboarding_v2 SET state = 'completed', current_step = 5,
				revision = ?, active_context_revision = ?, completed_at = ?, updated_at = ?
				WHERE agent_id = ? AND revision = ?`, newRevision, revision, now, now, id, state.Revision); res.Error != nil || res.RowsAffected != 1 {
				return errConflict
			}
		} else {
			if res := tx.Exec(`UPDATE agent_onboarding_v2 SET current_step = ?, revision = ?, updated_at = ?
				WHERE agent_id = ? AND revision = ?`, nextStep, newRevision, now, id, state.Revision); res.Error != nil || res.RowsAffected != 1 {
				return errConflict
			}
		}
		provenanceJSON, err := json.Marshal(provenance)
		if err != nil {
			return err
		}
		if err := tx.Exec(`INSERT INTO agent_onboarding_drafts
			(agent_id, revision, draft_data, field_provenance, actor_type, request_id, created_at)
			VALUES (?, ?, ?::jsonb, ?::jsonb, 'human_edit', ?, ?)`, id, newRevision,
			string(normalizedDraftJSON), string(provenanceJSON), "confirm:"+hashString(req.IdempotencyKey), now).Error; err != nil {
			return err
		}
		response = map[string]interface{}{
			"state": newState, "current_step": nextStep, "revision": newRevision,
			"active_context_revision": contextRevision,
		}
		snapshot, _ := json.Marshal(response)
		return tx.Exec(`INSERT INTO agent_idempotency_requests
			(agent_id, operation, idempotency_key, request_hash, response_snapshot, expires_at, created_at)
			VALUES (?, 'onboarding_confirm', ?, ?, ?::jsonb, ?, ?)`, id, req.IdempotencyKey,
			requestHash, string(snapshot), now+int64(24*time.Hour/time.Millisecond), now).Error
	})
	if err != nil && (errors.Is(err, errConflict) || isUniqueViolation(err)) {
		found, hashConflict, replayErr := s.loadIdempotentResponse(id, "onboarding_confirm", req.IdempotencyKey, requestHash, &response)
		switch {
		case replayErr != nil:
			err = replayErr
		case found && hashConflict:
			err = errConflict
		case found:
			replayed = true
			err = nil
		}
	}
	if errors.Is(err, errConflict) {
		fail(c, http.StatusConflict, "REVISION_CONFLICT", "onboarding step or revision changed", nil)
		return
	}
	if errors.Is(err, errEmailBindingRequired) {
		fail(c, http.StatusConflict, "EMAIL_BINDING_REQUIRED", "bind and verify an email before confirming onboarding", nil)
		return
	}
	if errors.Is(err, errInvalidOnboardingDraft) {
		fail(c, http.StatusBadRequest, "CONFIRM_FAILED", err.Error(), nil)
		return
	}
	if err != nil {
		fail(c, http.StatusInternalServerError, "CONFIRM_FAILED", "could not confirm onboarding step", nil)
		return
	}
	if !replayed && req.Step == 2 {
		agentcard.PublishRebuild(ctx, id, "console_v2_onboarding_identity")
		activity.PublishAgentCardUpdate(ctx, id)
	}
	if !replayed && req.Step == 3 {
		activity.PublishNetworkGoalUpdate(ctx, id)
	}
	if !replayed && req.Step == 4 {
		activity.PublishIntentActionsUpdate(ctx, id, confirmedIntentCount)
	}
	if !replayed && req.Step == 5 && response["state"] == "completed" {
		publishProfileCompletion(ctx, id)
		activity.PublishOnboardingCompleted(ctx, id)
	}
	reply(c, http.StatusOK, response)
}

func publishProfileCompletion(ctx context.Context, agentID int64) {
	if mq.RDB == nil {
		logger.Default().Warn("profile completion publish skipped: redis unavailable", "agentID", agentID)
		return
	}
	if _, err := mq.Publish(ctx, "stream:profile:update", map[string]interface{}{
		"agent_id": strconv.FormatInt(agentID, 10),
	}); err != nil {
		logger.Default().Warn("profile completion publish failed", "agentID", agentID, "err", err)
	}
}

func applyConfirmedStep(tx *gorm.DB, agentID int64, step int16, payload draftPayload, provenance map[string]fieldProvenance, now int64) error {
	switch step {
	case 2:
		if strings.TrimSpace(payload.IdentityCard.AgentName) == "" {
			return fmt.Errorf("%w: agent_name is required", errInvalidOnboardingDraft)
		}
		description := payload.IdentityCard.AgentDescription
		if description == "" {
			description = payload.IdentityCard.Bio
		}
		if err := tx.Exec(`UPDATE agents SET agent_name = ?, bio = ?, updated_at = ? WHERE agent_id = ?`,
			payload.IdentityCard.AgentName, description, now, agentID).Error; err != nil {
			return err
		}
		if err := profiledal.EnsureAgentProfileRow(tx, agentID); err != nil {
			return err
		}
		version, _, err := profiledal.GetProfileVersionAndData(tx, agentID)
		if err != nil {
			return err
		}
		values := map[string]interface{}{}
		for key, value := range map[string]*string{
			"human_description": payload.IdentityCard.HumanDescription,
			"geo":               payload.IdentityCard.Geo,
			"timezone":          payload.IdentityCard.Timezone,
		} {
			if value != nil {
				values[key] = *value
			}
		}
		for key, value := range map[string][]string{
			"working_languages":  payload.IdentityCard.WorkingLanguages,
			"seeking":            payload.IdentityCard.Seeking,
			"offering":           payload.IdentityCard.Offering,
			"agent_status":       payload.IdentityCard.AgentStatus,
			"human_status":       payload.IdentityCard.HumanStatus,
			"interests_negative": payload.IdentityCard.InterestsNegative,
		} {
			if value != nil {
				values[key] = value
			}
		}
		merge := make(map[string]json.RawMessage, len(values))
		for key, value := range values {
			encoded, marshalErr := json.Marshal(value)
			if marshalErr != nil {
				return marshalErr
			}
			merge[key] = encoded
		}
		_, conflict, err := profiledal.ApplyVersionedProfileDataUpdate(tx, agentID, version, merge)
		if conflict {
			return errConflict
		}
		return err
	case 3:
		if strings.TrimSpace(payload.NetworkGoal) == "" {
			return fmt.Errorf("%w: network_goal is required", errInvalidOnboardingDraft)
		}
		if err := tx.Exec(`UPDATE agent_network_goals SET status = 'deleted', updated_at = ?
			WHERE agent_id = ? AND status = 'active'`, now, agentID).Error; err != nil {
			return err
		}
		return tx.Exec(`INSERT INTO agent_network_goals
			(agent_id, goal_text, source, status, version, created_at, updated_at)
			VALUES (?, ?, ?, 'active', 1, ?, ?)`, agentID, payload.NetworkGoal,
			canonicalSource(provenance, "network_goal"), now, now).Error
	case 4:
		if err := tx.Exec(`UPDATE agent_intent_actions SET status = 'deleted', updated_at = ?
			WHERE agent_id = ? AND status <> 'deleted'`, now, agentID).Error; err != nil {
			return err
		}
		for _, intent := range payload.IntentActions {
			if err := tx.Exec(`INSERT INTO agent_intent_actions
				(agent_id, watch_for, trigger_when, action_instruction, action_policy, priority,
				 source, status, version, created_at, updated_at)
				VALUES (?, ?, ?, ?, ?, ?, ?, 'active', 1, ?, ?)`, agentID,
				intent.WatchFor, intent.TriggerWhen, intent.ActionInstruction, intent.ActionPolicy,
				intent.Priority, canonicalSource(provenance, "intent_actions"), now, now).Error; err != nil {
				return err
			}
		}
	case 5:
		return tx.Exec(`INSERT INTO agent_settings
			(agent_id, recurring_publish, auto_reply_pm, auto_comment, show_add_friend, updated_at)
			VALUES (?, ?, ?, ?, ?, ?)
			ON CONFLICT (agent_id) DO UPDATE SET recurring_publish = EXCLUDED.recurring_publish,
				auto_reply_pm = EXCLUDED.auto_reply_pm, auto_comment = EXCLUDED.auto_comment,
				show_add_friend = EXCLUDED.show_add_friend, updated_at = EXCLUDED.updated_at`, agentID,
			payload.SecurityBoundary.RecurringPublish, payload.SecurityBoundary.AutoReplyPM,
			payload.SecurityBoundary.AutoComment, payload.SecurityBoundary.ShowAddFriend, now).Error
	}
	return nil
}

func compileAndActivateContext(tx *gorm.DB, agentID, now int64) (json.RawMessage, int64, error) {
	var head struct {
		CurrentRevision int64 `gorm:"column:current_revision"`
	}
	if err := tx.Raw(`SELECT current_revision FROM agent_context_heads WHERE agent_id = ? FOR UPDATE`, agentID).Scan(&head).Error; err != nil {
		return nil, 0, err
	}
	var goal struct {
		GoalID   int64  `gorm:"column:goal_id" json:"goal_id,string"`
		GoalText string `gorm:"column:goal_text" json:"text"`
		Source   string `gorm:"column:source" json:"source"`
		Status   string `gorm:"column:status" json:"status"`
	}
	if err := tx.Raw(`SELECT goal_id, goal_text, source, status FROM agent_network_goals
		WHERE agent_id = ? AND status = 'active'`, agentID).Scan(&goal).Error; err != nil {
		return nil, 0, err
	}
	var intents []struct {
		IntentID          int64  `gorm:"column:intent_id" json:"intent_id,string"`
		WatchFor          string `gorm:"column:watch_for" json:"watch_for"`
		TriggerWhen       string `gorm:"column:trigger_when" json:"trigger_when"`
		ActionInstruction string `gorm:"column:action_instruction" json:"then"`
		ActionPolicy      string `gorm:"column:action_policy" json:"action_policy"`
		Source            string `gorm:"column:source" json:"source"`
		Status            string `gorm:"column:status" json:"status"`
	}
	if err := tx.Raw(`SELECT intent_id, watch_for, trigger_when, action_instruction,
			action_policy, source, status FROM agent_intent_actions
		WHERE agent_id = ? AND status = 'active' ORDER BY priority DESC, intent_id`, agentID).Scan(&intents).Error; err != nil {
		return nil, 0, err
	}
	var boundary struct {
		RecurringPublish bool `gorm:"column:recurring_publish" json:"recurring_publish"`
		AutoReplyPM      bool `gorm:"column:auto_reply_pm" json:"auto_reply_pm"`
		AutoComment      bool `gorm:"column:auto_comment" json:"auto_comment"`
		ShowAddFriend    bool `gorm:"column:show_add_friend" json:"show_add_friend"`
	}
	if err := tx.Raw(`SELECT COALESCE(settings.recurring_publish, true) AS recurring_publish,
			COALESCE(settings.auto_reply_pm, true) AS auto_reply_pm,
			COALESCE(settings.auto_comment, true) AS auto_comment,
			COALESCE(settings.show_add_friend, true) AS show_add_friend
		FROM agents agent LEFT JOIN agent_settings settings ON settings.agent_id = agent.agent_id
		WHERE agent.agent_id = ?`, agentID).Scan(&boundary).Error; err != nil {
		return nil, 0, err
	}
	revision := head.CurrentRevision + 1
	compiled, err := json.Marshal(map[string]interface{}{
		"context_revision":  revision,
		"network_goal":      goal,
		"intent_actions":    intents,
		"security_boundary": boundary,
		"safety":            map[string]string{"external_side_effects": "require_user_confirmation"},
	})
	if err != nil {
		return nil, 0, err
	}
	if err := tx.Exec(`INSERT INTO agent_context_revisions
		(agent_id, revision, compiled_context, schema_version, generated_at)
		VALUES (?, ?, ?::jsonb, 1, ?)`, agentID, revision, string(compiled), now).Error; err != nil {
		return nil, 0, err
	}
	if res := tx.Exec(`UPDATE agent_context_heads SET current_revision = ?, active_revision = ?, updated_at = ?
		WHERE agent_id = ? AND current_revision = ?`, revision, revision, now, agentID, head.CurrentRevision); res.Error != nil || res.RowsAffected != 1 {
		return nil, 0, errConflict
	}
	return compiled, revision, nil
}

func (s *Service) getControlContext(_ context.Context, c *app.RequestContext) {
	id, _ := agentID(c)
	ifNewer := int64(0)
	if raw := strings.TrimSpace(c.Query("if_newer")); raw != "" {
		value, err := strconv.ParseInt(raw, 10, 64)
		if err != nil || value < 0 {
			fail(c, http.StatusBadRequest, "INVALID_REVISION", "if_newer must be a non-negative revision", nil)
			return
		}
		ifNewer = value
	}
	var row struct {
		Revision int64  `gorm:"column:revision"`
		Context  string `gorm:"column:compiled_context"`
	}
	if err := s.db.Raw(`SELECT r.revision, r.compiled_context::text AS compiled_context
		FROM agent_context_heads h JOIN agent_context_revisions r
		  ON r.agent_id = h.agent_id AND r.revision = h.active_revision
		WHERE h.agent_id = ?`, id).Scan(&row).Error; err != nil || row.Revision == 0 {
		fail(c, http.StatusInternalServerError, "CONTEXT_NOT_AVAILABLE", "active control context is not available", nil)
		return
	}
	if ifNewer >= row.Revision {
		reply(c, http.StatusOK, map[string]interface{}{"context_revision": row.Revision, "unchanged": true})
		return
	}
	reply(c, http.StatusOK, map[string]interface{}{
		"context_revision": row.Revision, "unchanged": false,
		"control_context": json.RawMessage(row.Context),
	})
}
