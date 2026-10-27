package cmd

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strconv"
	"strings"
	"time"
	"unicode/utf8"

	"cli.eigenflux.ai/internal/auth"
	"cli.eigenflux.ai/internal/client"
	"cli.eigenflux.ai/internal/config"
	"cli.eigenflux.ai/internal/dispatch"
	"cli.eigenflux.ai/internal/skills"
)

const commissionIntakeTimeout = 180 * time.Second

var fulfillmentSkillName = regexp.MustCompile(`^[a-z0-9][a-z0-9-]{0,127}$`)

var (
	errCommissionSkillMissing    = errors.New("commission_skill_missing")
	errCommissionSkillUnreadable = errors.New("commission_skill_unreadable")
	errCommissionSkillInvalid    = errors.New("commission_skill_invalid")
	errCommissionContractInvalid = errors.New("commission_contract_invalid")
)

type commissionIntakeOrder struct {
	OrderID           notificationID          `json:"order_id"`
	BuyerID           notificationID          `json:"buyer_agent_id"`
	SellerID          notificationID          `json:"seller_agent_id"`
	Version           int64                   `json:"version"`
	State             string                  `json:"state"`
	SnapshotID        notificationID          `json:"current_snapshot_id"`
	Contract          json.RawMessage         `json:"contract"`
	BuyerInput        string                  `json:"buyer_input"`
	ExecutionRevision int64                   `json:"execution_revision"`
	ExecutionIssue    *struct{}               `json:"execution_issue"`
	InputRequest      *commissionInputRequest `json:"input_request,omitempty"`
}

// A separate worker keeps intake off the PM execution lane. Its idle tick only
// checks the local journal; sharing the PM wake channel would lose wake-ups.
func (w *accountWatch) commissionDispatchLoop(ctx context.Context) error {
	if !w.receivesCommission() {
		return nil
	}
	for ctx.Err() == nil {
		if !w.handoff.begin() {
			return nil
		}
		job, ok, err := w.journal.NextKind("commission_order")
		if err != nil {
			w.handoff.end()
			return err
		}
		if !ok {
			w.handoff.end()
			if !watchPause(ctx, time.Second) {
				break
			}
			continue
		}
		err = w.dispatchCommissionIntake(ctx, job)
		w.handoff.end()
		if err != nil {
			return err
		}
	}
	return ctx.Err()
}

func (w *accountWatch) commissionIdentityOK() error {
	if err := w.dispatchIdentityOK(); err != nil {
		return err
	}
	cfg, err := config.Load()
	if err != nil {
		return err
	}
	current, err := cfg.GetActive(w.server.Name)
	if err != nil || current.CommissionEndpoint != w.server.CommissionEndpoint {
		return errWatchConfiguration
	}
	return nil
}

func (w *accountWatch) commissionAPI(ctx context.Context) (*client.Client, error) {
	if err := w.commissionIdentityOK(); err != nil {
		return nil, err
	}
	base, err := w.server.CommissionBaseURL()
	if err != nil {
		return nil, err
	}
	credentials, err := w.credentials(ctx, false)
	if err != nil {
		return nil, err
	}
	api := client.New(strings.TrimRight(base, "/")+"/api/v1", credentials.AccessToken, version, clientMetaForServer(&w.server))
	api.HTTPClient = &http.Client{Timeout: 10 * time.Second, Transport: watchTransport{ctx}}
	api.OnUnauthorized = func() (string, error) {
		if err := w.commissionIdentityOK(); err != nil {
			return "", err
		}
		credentials, err := w.credentials(ctx, true)
		if err != nil {
			return "", err
		}
		return credentials.AccessToken, nil
	}
	return api, nil
}

func (w *accountWatch) readCommissionOrder(ctx context.Context, id string) (commissionIntakeOrder, error) {
	var data struct {
		Order commissionIntakeOrder `json:"order"`
	}
	api, err := w.commissionAPI(ctx)
	if err != nil {
		return data.Order, err
	}
	response, err := api.Get("/orders/"+id, nil)
	if err != nil {
		return data.Order, err
	}
	if response.Code != 0 || len(response.Data) > 256<<10 || json.Unmarshal(response.Data, &data) != nil {
		return data.Order, errors.New("commission_order_unavailable")
	}
	order := data.Order
	if strconv.FormatInt(int64(order.OrderID), 10) != id || order.Version <= 0 || order.SnapshotID <= 0 || order.BuyerID <= 0 || order.SellerID <= 0 || order.State == "" {
		return order, errors.New("invalid_commission_order")
	}
	if strconv.FormatInt(int64(order.SellerID), 10) != w.binding.AgentID {
		return order, errors.New("commission_seller_mismatch")
	}
	return order, nil
}

func (w *accountWatch) dispatchCommissionIntake(parent context.Context, job dispatch.Job) error {
	if err := w.commissionIdentityOK(); err != nil {
		if saveErr := w.finishDispatch(job, "failed", "identity_changed", "", ""); saveErr != nil {
			return saveErr
		}
		return err
	}
	var notification struct {
		Type    string `json:"type"`
		Payload struct {
			OrderID   notificationID `json:"order_id"`
			Version   int64          `json:"order_version"`
			Role      string         `json:"recipient_role"`
			EventKind string         `json:"event_kind"`
		} `json:"payload"`
	}
	if json.Unmarshal(job.Data, &notification) != nil || notification.Payload.OrderID <= 0 || notification.Payload.Version <= 0 {
		return w.finishDispatch(job, "failed", "invalid_commission_notification", "", "")
	}
	if commissionResultNotificationText("en", notification.Payload.EventKind) != "" || commissionResultNotificationText("en", notification.Type) != "" {
		return w.finishDispatch(job, "completed", "commission_result_notification_recorded", "", "")
	}
	if notification.Payload.Role != "seller" {
		if notification.Type != "order.inputs.requested.v1" && notification.Payload.EventKind != "order.inputs.requested.v1" {
			return w.finishDispatch(job, "completed", "commission_buyer_notification_recorded", "", "")
		}
		return w.queueBuyerCommissionClarification(parent, job, strconv.FormatInt(int64(notification.Payload.OrderID), 10))
	}
	ctx, cancel := context.WithTimeout(parent, commissionIntakeTimeout)
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
				if w.commissionIdentityOK() != nil {
					cancel()
					return
				}
			}
		}
	}()
	defer func() { cancel(); <-guardDone }()
	id := strconv.FormatInt(int64(notification.Payload.OrderID), 10)
	order, err := w.readCommissionOrder(ctx, id)
	if err != nil || order.Version < notification.Payload.Version {
		return w.finishDispatch(job, "failed", "commission_order_unavailable", "", "")
	}
	switch order.State {
	case "awaiting_seller", "pending_payment", "in_progress":
	default:
		if err := w.journal.RetireCommissionReadiness(id); err != nil {
			return err
		}
		return w.finishDispatch(job, "completed", "commission_order_not_actionable", "", "")
	}
	if order.State == "awaiting_seller" && order.InputRequest != nil && order.InputRequest.Status == "waiting_buyer" {
		if !validCommissionInputRequest(order.InputRequest) {
			return w.finishDispatch(job, "failed", "commission_clarification_invalid", "", "")
		}
		return w.finishDispatch(job, "completed", "commission_clarification_waiting_buyer", "", "")
	}
	if err := w.journal.SetCommissionExecution(job.ID, id, order.Version, order.ExecutionRevision, order.ExecutionIssue != nil); err != nil {
		return err
	}
	if order.State == "pending_payment" {
		return w.finishDispatch(job, "completed", "commission_waiting_for_payment", "", "")
	}
	if order.State == "in_progress" {
		return w.queueCommissionFromCheckedInputs(ctx, job, order)
	}
	// Repeated creation notices reuse an actual decision for the same current
	// version; explicit operator retries may repeat this pre-acceptance inspection.
	if job.Code != "operator_retry" {
		for _, prior := range w.journal.Snapshot() {
			result := prior.CommissionResult
			if prior.ID != job.ID && result != nil && prior.Code != "operator_verified" && result.Outcome != "failed" && result.OrderID == id && result.OrderVersion == order.Version {
				return w.finishDispatch(job, "completed", "commission_intake_already_checked", "", "")
			}
		}
	}
	prompt, requiresMaterials, files, cleanup, err := w.commissionIntakePrompt(ctx, job, order)
	if err != nil {
		if errors.Is(err, errCommissionMaterialsMissing) && order.State == "awaiting_seller" {
			return w.requestCommissionInputs(ctx, job, order, "The required buyer input files are missing. Clarify the available authorized materials needed by the frozen contract.", "", "required_input_files")
		}
		status, code := "failed", "commission_intake_prompt_unavailable"
		switch {
		case errors.Is(err, errCommissionMaterialsLimit):
			status, code = "needs_user", "commission_materials_limit"
		case errors.Is(err, errCommissionMaterialsMissing):
			status, code = "needs_user", "commission_materials_missing"
		case errors.Is(err, errCommissionMaterialsInvalid):
			code = "commission_materials_invalid"
		case errors.Is(err, errCommissionMaterialsUnavailable):
			code = "commission_materials_unavailable"
		case errors.Is(err, dispatch.ErrNeedsUser):
			status, code = "needs_user", commissionSkillFailureCode(err)
		}
		return w.finishDispatch(job, status, code, "", "")
	}
	defer cleanup()
	if err := w.commissionIdentityOK(); err != nil {
		if saveErr := w.finishDispatch(job, "failed", "identity_changed", "", ""); saveErr != nil {
			return saveErr
		}
		return err
	}
	if ctx.Err() != nil {
		return w.finishDispatch(job, "failed", "commission_intake_cancelled_before_execution", "", "")
	}
	result, runErr := w.runAgent(ctx, prompt)
	if runErr != nil {
		status, code := "unknown", "commission_intake_unconfirmed"
		if errors.Is(runErr, dispatch.ErrNeedsUser) {
			status, code = "needs_user", "commission_intake_permission_required"
		}
		if errors.Is(runErr, dispatch.ErrCommandLineTooLong) {
			status, code = "needs_user", "windows_command_line_too_long"
		}
		if errors.Is(runErr, dispatch.ErrCommissionSkillReadScopeInvalid) {
			status, code = "needs_user", "commission_skill_read_scope_invalid"
		}
		return w.finishDispatch(job, status, code, result.SessionID, "")
	}
	decision, err := dispatch.ParseCommissionIntakeDecision(result.Text, job.ID, id, order.Version)
	if err != nil || !validCommissionInspectedFiles(decision, files, requiresMaterials) {
		return w.finishDispatch(job, "failed", "commission_intake_result_invalid", result.SessionID, "")
	}
	if err := w.commissionIdentityOK(); err != nil {
		if saveErr := w.finishDispatch(job, "failed", "identity_changed", result.SessionID, ""); saveErr != nil {
			return saveErr
		}
		return err
	}
	current, err := w.readCommissionOrder(ctx, id)
	if err != nil || !sameCommissionIntakeOrder(order, current) {
		return w.finishDispatch(job, "failed", "commission_order_changed", result.SessionID, "")
	}
	if order.State == "awaiting_seller" && decision.Outcome == "needs_input" {
		return w.requestCommissionInputs(ctx, job, order, decision.Summary, result.SessionID)
	}
	if order.State == "awaiting_seller" && decision.Outcome == "ready" {
		fingerprint, err := commissionInputFingerprint(order, files)
		if err != nil {
			return w.finishDispatch(job, "failed", "commission_materials_invalid", result.SessionID, "")
		}
		if err := w.journal.SaveCommissionReadiness(job.ID, dispatch.CommissionReadiness{Result: decision, InputFingerprint: fingerprint}); err != nil {
			if errors.Is(err, dispatch.ErrCommissionReadinessCapacity) {
				return w.finishDispatch(job, "needs_user", "commission_readiness_capacity_exceeded", "", "")
			}
			return err
		}
		if err := w.acceptInspectedCommission(ctx, order); err != nil {
			return w.finishDispatch(job, "unknown", "commission_accept_unconfirmed", result.SessionID, "")
		}
	}
	return auth.WithV2CredentialsLockContext(ctx, w.server.Name, 35*time.Second, func() error {
		if err := w.commissionIdentityOK(); err != nil {
			if saveErr := w.finishDispatch(job, "failed", "identity_changed", result.SessionID, ""); saveErr != nil {
				return saveErr
			}
			return err
		}
		if err := w.journal.CompleteCommissionIntake(job.ID, decision, result.SessionID); err != nil {
			return err
		}
		return w.emit("dispatch_status", map[string]string{"job_id": job.ID, "kind": job.Kind, "order_id": id, "code": "commission_intake_" + decision.Outcome, "outcome": decision.Outcome})
	})
}

// A paid state change advances the order version, but never substitutes a new
// model inspection for the actual check completed before acceptance.
func (w *accountWatch) queueCommissionFromCheckedInputs(ctx context.Context, job dispatch.Job, order commissionIntakeOrder) error {
	id := strconv.FormatInt(int64(order.OrderID), 10)
	if w.journal.HasCommissionFulfillment(id) {
		return w.finishDispatch(job, "completed", "commission_fulfillment_already_queued", "", "")
	}
	var source dispatch.Job
	for _, prior := range w.journal.Snapshot() {
		readiness := prior.CommissionReadiness
		if readiness != nil && readiness.Result.Outcome == "ready" && readiness.Result.OrderID == id && readiness.Result.OrderVersion < order.Version && (source.CommissionReadiness == nil || source.CommissionReadiness.Result.OrderVersion < readiness.Result.OrderVersion) {
			source = prior
		}
	}
	if source.CommissionReadiness == nil {
		return w.finishDispatch(job, "needs_user", "commission_intake_evidence_missing", "", "")
	}
	directory, err := os.MkdirTemp(w.binding.WorkDir, ".eigenflux-intake-")
	if err != nil {
		return w.finishDispatch(job, "failed", "commission_materials_unavailable", "", "")
	}
	defer os.RemoveAll(directory)
	files, err := w.prepareCommissionMaterials(ctx, order, directory)
	if err != nil {
		return w.finishDispatch(job, "needs_user", "commission_materials_unavailable", "", "")
	}
	fingerprint, err := commissionInputFingerprint(order, files)
	if err != nil || fingerprint != source.CommissionReadiness.InputFingerprint {
		return w.finishDispatch(job, "needs_user", "commission_fulfillment_inputs_changed", "", "")
	}
	current, err := w.readCommissionOrder(ctx, id)
	if err != nil || !sameCommissionIntakeOrder(order, current) || ctx.Err() != nil {
		return w.finishDispatch(job, "needs_user", "commission_fulfillment_order_changed", "", "")
	}
	if err := w.commissionIdentityOK(); err != nil {
		return err
	}
	return w.journal.QueueCommissionFromReadiness(job.ID, source.ID, id, order.Version)
}

func commissionInputFingerprint(order commissionIntakeOrder, files []commissionLocalFile) (string, error) {
	var contract any
	decoder := json.NewDecoder(strings.NewReader(string(order.Contract)))
	decoder.UseNumber()
	if err := decoder.Decode(&contract); err != nil {
		return "", err
	}
	if decoder.Decode(new(any)) != io.EOF {
		return "", errCommissionContractInvalid
	}
	type inputFile struct {
		Path     string `json:"path"`
		SHA256   string `json:"sha256"`
		ByteSize int64  `json:"byte_size"`
	}
	inputs := make([]inputFile, 0, len(files))
	for _, file := range files {
		inputs = append(inputs, inputFile{file.LogicalPath, strings.ToLower(file.SHA256), file.ByteSize})
	}
	sort.Slice(inputs, func(i, j int) bool { return inputs[i].Path < inputs[j].Path })
	raw, err := json.Marshal(struct {
		OrderID    notificationID `json:"order_id"`
		Buyer      notificationID `json:"buyer"`
		Seller     notificationID `json:"seller"`
		Contract   any            `json:"contract"`
		BuyerInput string         `json:"buyer_input"`
		Files      []inputFile    `json:"files"`
	}{order.OrderID, order.BuyerID, order.SellerID, contract, order.BuyerInput, inputs})
	if err != nil {
		return "", err
	}
	digest := sha256.Sum256(raw)
	return hex.EncodeToString(digest[:]), nil
}

// Acceptance is a seller-authenticated mutation following an actual Agent
// inspection. The version and deterministic key fence concurrent notifications.
func (w *accountWatch) acceptInspectedCommission(ctx context.Context, order commissionIntakeOrder) error {
	api, err := w.commissionAPI(ctx)
	if err != nil {
		return err
	}
	id := strconv.FormatInt(int64(order.OrderID), 10)
	return auth.WithV2CredentialsLockContext(ctx, w.server.Name, 35*time.Second, func() error {
		if err := w.commissionIdentityOK(); err != nil {
			return err
		}
		credentials, err := auth.LoadV2Credentials(w.server.Name)
		if err != nil {
			return err
		}
		api.Token, api.OnUnauthorized = credentials.AccessToken, nil
		response, err := api.PostWithHeaders("/orders/"+id+"/accept", map[string]any{"expected_version": order.Version}, map[string]string{idempotencyHeader: "watch-accept-" + id + "-" + strconv.FormatInt(order.Version, 10)})
		if err != nil {
			return err
		}
		if response.Code != 0 {
			return errors.New("commission_accept_rejected")
		}
		var data struct {
			Order commissionIntakeOrder `json:"order"`
		}
		if json.Unmarshal(response.Data, &data) != nil || data.Order.OrderID != order.OrderID || data.Order.SellerID != order.SellerID || data.Order.BuyerID != order.BuyerID || data.Order.Version != order.Version+1 || data.Order.State != "pending_payment" {
			return errors.New("commission_accept_receipt_invalid")
		}
		return w.emit("dispatch_status", map[string]string{"kind": "commission_order", "order_id": id, "code": "commission_accept_confirmed"})
	})
}

func sameCommissionIntakeOrder(a, b commissionIntakeOrder) bool {
	// Canonical encoding avoids treating harmless JSON whitespace as a change.
	var aContract, bContract any
	aDecoder, bDecoder := json.NewDecoder(strings.NewReader(string(a.Contract))), json.NewDecoder(strings.NewReader(string(b.Contract)))
	aDecoder.UseNumber()
	bDecoder.UseNumber()
	if aDecoder.Decode(&aContract) != nil || bDecoder.Decode(&bContract) != nil {
		return false
	}
	aJSON, _ := json.Marshal(aContract)
	bJSON, _ := json.Marshal(bContract)
	return a.OrderID == b.OrderID && a.BuyerID == b.BuyerID && a.SellerID == b.SellerID && a.Version == b.Version && a.State == b.State && a.SnapshotID == b.SnapshotID && a.BuyerInput == b.BuyerInput && string(aJSON) == string(bJSON)
}

func (w *accountWatch) commissionIntakePrompt(ctx context.Context, job dispatch.Job, order commissionIntakeOrder) (dispatch.Request, bool, []commissionLocalFile, func(), error) {
	return w.commissionAgentPrompt(ctx, job, order, nil)
}

func (w *accountWatch) commissionAgentPrompt(ctx context.Context, job dispatch.Job, order commissionIntakeOrder, extra map[string]any) (dispatch.Request, bool, []commissionLocalFile, func(), error) {
	var contract struct {
		Skill             string `json:"fulfillment_skill"`
		RequiresMaterials *bool  `json:"requires_materials"`
	}
	if len(order.Contract) > 64<<10 || json.Unmarshal(order.Contract, &contract) != nil || contract.RequiresMaterials == nil || !fulfillmentSkillName.MatchString(contract.Skill) {
		return dispatch.Request{}, false, nil, nil, errors.Join(dispatch.ErrNeedsUser, errCommissionContractInvalid)
	}
	rules, err := localHeartbeatSkillsAt(w.binding.SkillsDir, w.binding.Host)
	if err != nil {
		return dispatch.Request{}, false, nil, nil, err
	}
	ruleFile, label := "references/dispatch.md", "INTAKE"
	if job.Kind == "commission_fulfillment" {
		ruleFile, label = "references/fulfillment-dispatch.md", "FULFILLMENT"
	}
	intakeRules, err := skills.ReadSignedFile(rules.SkillsDir, "ef-commission", ruleFile, 64<<10)
	if err != nil {
		return dispatch.Request{}, false, nil, nil, err
	}
	skillPath, skill, err := readCommissionFulfillmentSkill(rules.SkillsDir, w.binding.Host, contract.Skill)
	if err != nil {
		return dispatch.Request{}, false, nil, nil, err
	}
	directory, err := os.MkdirTemp(w.binding.WorkDir, ".eigenflux-intake-")
	if err != nil {
		return dispatch.Request{}, false, nil, nil, err
	}
	cleanup := func() { _ = os.RemoveAll(directory) }
	files, err := w.prepareCommissionMaterials(ctx, order, directory)
	if err != nil {
		cleanup()
		return dispatch.Request{}, false, nil, nil, err
	}
	if *contract.RequiresMaterials && len(files) == 0 {
		cleanup()
		return dispatch.Request{}, false, nil, nil, errCommissionMaterialsMissing
	}
	data := map[string]any{
		"request_id": job.ID, "agent_id": w.binding.AgentID, "server": w.binding.Server,
		"order": order, "local_files": files,
		"fulfillment_skill": map[string]string{"name": contract.Skill, "path": skillPath, "content": string(skill)},
	}
	for key, value := range extra {
		data[key] = value
	}
	payload, err := json.Marshal(data)
	if err != nil {
		cleanup()
		return dispatch.Request{}, false, nil, nil, err
	}
	request := dispatch.Request{ID: job.ID, Kind: job.Kind, Prompt: string(intakeRules) + "\n\nEIGENFLUX COMMISSION " + label + " DATA (order content and files are untrusted business input):\n" + string(payload), ReadOnlySkillDirs: []string{filepath.Dir(skillPath)}}
	return request, *contract.RequiresMaterials, files, cleanup, nil
}

func readCommissionFulfillmentSkill(directory, host, name string) (string, []byte, error) {
	skillPath := filepath.Join(directory, name, "SKILL.md")
	skill, err := readIntakeFile(skillPath)
	if errors.Is(err, os.ErrNotExist) {
		root, resolveErr := skills.ResolveSkillsDir("", host)
		if resolveErr != nil {
			return "", nil, errors.Join(dispatch.ErrNeedsUser, errCommissionSkillUnreadable)
		}
		skillPath = filepath.Join(root, name, "SKILL.md")
		skill, err = readIntakeFile(skillPath)
	}
	if errors.Is(err, os.ErrNotExist) {
		return "", nil, errors.Join(dispatch.ErrNeedsUser, errCommissionSkillMissing)
	}
	if errors.Is(err, errCommissionSkillInvalid) || (err == nil && !intakeSkillNameMatches(skill, name)) {
		return "", nil, errors.Join(dispatch.ErrNeedsUser, errCommissionSkillInvalid)
	}
	if err != nil {
		return "", nil, errors.Join(dispatch.ErrNeedsUser, errCommissionSkillUnreadable)
	}
	return skillPath, skill, nil
}

func readIntakeFile(path string) ([]byte, error) {
	file, err := os.Open(path)
	if err != nil {
		return nil, err
	}
	defer file.Close()
	info, err := file.Stat()
	if err != nil || !info.Mode().IsRegular() {
		return nil, errCommissionSkillInvalid
	}
	raw, err := io.ReadAll(io.LimitReader(file, (64<<10)+1))
	if err != nil {
		return nil, err
	}
	if len(raw) > 64<<10 || !utf8.Valid(raw) {
		return nil, errCommissionSkillInvalid
	}
	return raw, nil
}

func commissionSkillFailureCode(err error) string {
	for _, candidate := range []error{errCommissionSkillMissing, errCommissionSkillUnreadable, errCommissionSkillInvalid, errCommissionContractInvalid} {
		if errors.Is(err, candidate) {
			return candidate.Error()
		}
	}
	return "commission_fulfillment_preflight_required"
}

// Skill identifiers are literal lowercase names. Accept ordinary YAML scalar
// spelling, and reject ambiguous duplicate or multiline name declarations.
func intakeSkillNameMatches(raw []byte, expected string) bool {
	lines := strings.Split(strings.ReplaceAll(string(raw), "\r\n", "\n"), "\n")
	if len(lines) < 3 || lines[0] != "---" {
		return false
	}
	name, found := "", false
	for _, line := range lines[1:] {
		if line == "---" {
			return found && name == expected
		}
		value, ok := strings.CutPrefix(line, "name:")
		if !ok {
			continue
		}
		if found {
			return false
		}
		found = true
		value = strings.TrimSpace(value)
		if strings.HasPrefix(value, `"`) {
			decoded, err := strconv.Unquote(value)
			if err != nil {
				return false
			}
			name = decoded
		} else if strings.HasPrefix(value, "'") && strings.HasSuffix(value, "'") && len(value) >= 2 {
			name = value[1 : len(value)-1]
		} else {
			name = strings.TrimSpace(strings.SplitN(value, " #", 2)[0])
		}
	}
	return false
}

// The Agent may only attest to authoritative files already verified by the CLI.
// Ready requires complete coverage, even for optional materials that were sent.
func validCommissionInspectedFiles(decision dispatch.CommissionIntakeDecision, files []commissionLocalFile, required bool) bool {
	allowed := make(map[string]bool, len(files))
	for _, file := range files {
		allowed[file.LogicalPath] = true
	}
	for _, name := range decision.InspectedFiles {
		if !allowed[name] {
			return false
		}
		delete(allowed, name)
	}
	return decision.Outcome != "ready" || (len(allowed) == 0 && (!required || len(files) > 0))
}
