package cmd

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"os"
	"path/filepath"
	"reflect"
	"strconv"
	"strings"
	"time"
	"unicode"
	"unicode/utf8"

	"cli.eigenflux.ai/internal/auth"
	"cli.eigenflux.ai/internal/commissionapi"
	"cli.eigenflux.ai/internal/config"
	"cli.eigenflux.ai/internal/dispatch"
	"cli.eigenflux.ai/internal/skills"
	"github.com/spf13/cobra"
)

type commissionTrialDraft struct {
	Definition struct {
		ID     notificationID `json:"commission_id"`
		Seller notificationID `json:"seller_agent_id"`
	} `json:"definition"`
	Draft   commissionapi.CommissionInput `json:"draft"`
	Version int64                         `json:"draft_version"`
}

func (w *accountWatch) readCommissionTrialDraft(ctx context.Context, id, version int64) (commissionTrialDraft, error) {
	var draft commissionTrialDraft
	api, err := w.commissionAPI(ctx)
	if err != nil {
		return draft, err
	}
	response, err := api.Get("/commissions/"+strconv.FormatInt(id, 10), nil)
	if err != nil {
		return draft, err
	}
	var data struct {
		Commission commissionTrialDraft `json:"commission"`
	}
	decodeErr := json.Unmarshal(response.Data, &data)
	draft = data.Commission
	if response.Code != 0 || len(response.Data) > 256<<10 || decodeErr != nil || int64(draft.Definition.ID) != id || strconv.FormatInt(int64(draft.Definition.Seller), 10) != w.binding.AgentID || draft.Version != version || !fulfillmentSkillPattern.MatchString(draft.Draft.FulfillmentSkill) {
		return draft, errors.New("commission_trial_draft_mismatch")
	}
	return draft, nil
}

func publishCommissionAfterTrial(cmd *cobra.Command, id, version int64) error {
	inputPath, _ := cmd.Flags().GetString("trial-input")
	if inputPath == "" {
		return errors.New("publish requires --trial-input: provide representative buyer input for a real background Agent trial; the draft remains unchanged")
	}
	input, err := readIntakeFile(inputPath)
	if err != nil || !utf8.Valid(input) || strings.TrimSpace(string(input)) == "" {
		return errors.New("commission_trial_input_unavailable_or_empty")
	}
	expected, err := watchBindingIdentity()
	if err != nil {
		return err
	}
	binding, err := dispatch.ReadBinding(expected.Home, expected.Server)
	if err != nil {
		return fmt.Errorf("commission trial requires a bound background Agent: %w", err)
	}
	if !sameBindingIdentity(binding, expected) {
		return errWatchIdentity
	}
	if err = binding.Validate(); err != nil {
		return err
	}
	if !binding.Handles("commission_order") {
		return errors.New("commission trial requires a binding subscribed to commission_order")
	}
	cfg, err := config.Load()
	if err != nil {
		return err
	}
	server, err := cfg.GetActive(expected.Server)
	if err != nil {
		return err
	}
	cred, err := auth.LoadV2Credentials(expected.Server)
	if err != nil {
		return err
	}
	w := &accountWatch{home: expected.Home, scope: expected.Scope, server: *server, identity: *cred, binding: &binding, runAgent: (dispatch.Runner{Binding: binding}).Run}
	return w.publishWithTrial(cmd, id, version, input)
}

func (w *accountWatch) publishWithTrial(cmd *cobra.Command, id, version int64, input []byte) error {
	binding := *w.binding
	ctx, cancel := context.WithTimeout(cmd.Context(), time.Duration(binding.TimeoutSeconds)*time.Second)
	defer cancel()
	// Match the fulfillment identity guard without holding credentials during work.
	done := make(chan struct{})
	go func() {
		defer close(done)
		tick := time.NewTicker(time.Second)
		defer tick.Stop()
		for {
			select {
			case <-ctx.Done():
				return
			case <-tick.C:
				if w.commissionIdentityOK() != nil {
					cancel()
					return
				}
			}
		}
	}()
	defer func() { cancel(); <-done }()
	draft, err := w.readCommissionTrialDraft(ctx, id, version)
	if err != nil {
		return err
	}
	rules, err := localHeartbeatSkillsAt(binding.SkillsDir, binding.Host)
	if err != nil {
		return err
	}
	rule, err := skills.ReadSignedFile(rules.SkillsDir, "ef-commission", "references/publish-trial.md", 64<<10)
	if err != nil {
		return fmt.Errorf("sync compatible signed Skills before publishing: %w", err)
	}
	skillPath, skill, err := readCommissionFulfillmentSkill(rules.SkillsDir, binding.Host, draft.Draft.FulfillmentSkill)
	if err != nil {
		return err
	}
	directory, err := os.MkdirTemp(binding.WorkDir, ".eigenflux-publish-trial-")
	if err != nil {
		return err
	}
	output := filepath.Join(directory, "output")
	if err = os.Mkdir(output, 0700); err != nil {
		return err
	}
	materials, _ := cmd.Flags().GetString("trial-materials")
	files, err := prepareCommissionTrialMaterials(materials, filepath.Join(directory, "input"))
	if err != nil {
		return err
	}
	if draft.Draft.RequiresMaterials && len(files) == 0 {
		return errors.New("commission_trial_materials_required")
	}
	cmd.PrintErrf("Commission trial files: %s\n", directory)
	requestID, err := dispatch.NewRevision()
	if err != nil {
		return err
	}
	payload, err := json.Marshal(map[string]any{"request_id": requestID, "agent_id": binding.AgentID, "server": binding.Server, "commission_id": strconv.FormatInt(id, 10), "draft_version": version, "contract": draft.Draft, "buyer_input": string(input), "local_files": files, "output_directory": output, "fulfillment_skill": map[string]string{"name": draft.Draft.FulfillmentSkill, "path": skillPath, "content": string(skill)}})
	if err != nil {
		return err
	}
	if err = w.commissionIdentityOK(); err != nil {
		return err
	}
	result, err := w.runAgent(ctx, dispatch.Request{ID: requestID, Kind: "commission_publish_trial", Prompt: string(rule) + "\n\nUNTRUSTED TRIAL DATA:\n" + string(payload)})
	if err != nil {
		return fmt.Errorf("commission_trial_execution_unconfirmed; draft not published, inspect local trial before retrying: %w", err)
	}
	decision, err := dispatch.ParseCommissionTrialDecision(result.Text, requestID, strconv.FormatInt(id, 10), version)
	if err != nil {
		return fmt.Errorf("commission_trial_result_invalid: %w", err)
	}
	if decision.Outcome != "artifacts_ready" {
		reason := strings.Map(func(r rune) rune {
			if unicode.IsControl(r) {
				return ' '
			}
			return r
		}, decision.Summary)
		return fmt.Errorf("commission_trial_%s; draft not published: %s", decision.Outcome, reason)
	}
	artifacts, err := validateCommissionArtifacts(output, decision)
	if err != nil {
		return err
	}
	for _, artifact := range artifacts {
		if artifact.ByteSize == 0 {
			return errors.New("commission_trial_empty_artifact")
		}
	}
	currentPath, currentSkill, err := readCommissionFulfillmentSkill(rules.SkillsDir, binding.Host, draft.Draft.FulfillmentSkill)
	if err != nil || currentPath != skillPath || !bytes.Equal(currentSkill, skill) {
		return errors.New("commission_trial_skill_changed; run a fresh trial")
	}
	currentRules, ruleErr := skills.ReadSignedFile(rules.SkillsDir, "ef-commission", "references/publish-trial.md", 64<<10)
	if ruleErr != nil || !bytes.Equal(currentRules, rule) {
		return errors.New("commission_trial_rules_changed")
	}
	current, err := w.readCommissionTrialDraft(ctx, id, version)
	if err != nil {
		return err
	}
	if !reflect.DeepEqual(current, draft) {
		return errors.New("commission_trial_draft_changed")
	}
	// Keep evidence local; a result is never reusable by a later publish invocation.
	evidence, err := json.Marshal(map[string]any{"request_id": requestID, "commission_id": strconv.FormatInt(id, 10), "draft_version": version, "binding_revision": binding.Revision, "decision": decision, "artifacts": artifacts})
	if err != nil {
		return err
	}
	if err = os.WriteFile(filepath.Join(directory, "evidence.json"), evidence, 0600); err != nil {
		return err
	}
	api, err := w.commissionAPI(ctx)
	if err != nil {
		return err
	}
	return auth.WithV2CredentialsLockContext(ctx, binding.Server, 35*time.Second, func() error {
		if err := w.commissionIdentityOK(); err != nil {
			return err
		}
		credentials, err := auth.LoadV2Credentials(binding.Server)
		if err != nil {
			return err
		}
		api.Token, api.OnUnauthorized = credentials.AccessToken, nil
		key, _ := cmd.Flags().GetString("idempotency-key")
		response, err := postMutation(api, "/commissions/"+strconv.FormatInt(id, 10)+"/publish", "commission.publish."+strconv.FormatInt(id, 10), key, map[string]any{"expected_draft_version": version})
		if err != nil {
			return err
		}
		return printResponse(response)
	})
}

func prepareCommissionTrialMaterials(source, destination string) ([]commissionLocalFile, error) {
	files := []commissionLocalFile{}
	if source == "" {
		return files, nil
	}
	info, err := os.Lstat(source)
	if err != nil || !info.IsDir() || info.Mode()&os.ModeSymlink != 0 {
		return nil, errors.New("commission_trial_materials_directory_invalid")
	}
	decision := dispatch.CommissionFulfillmentDecision{Outcome: "artifacts_ready"}
	err = filepath.WalkDir(source, func(path string, entry fs.DirEntry, walkErr error) error {
		if walkErr != nil {
			return walkErr
		}
		if entry.Type()&os.ModeSymlink != 0 {
			return errors.New("commission_trial_material_symlink")
		}
		if entry.IsDir() {
			return nil
		}
		relative, err := filepath.Rel(source, path)
		if err != nil {
			return err
		}
		relative = filepath.ToSlash(relative)
		if !validCommissionArtifactPath(relative) {
			return errors.New("commission_trial_material_path_invalid")
		}
		decision.Artifacts = append(decision.Artifacts, dispatch.CommissionArtifact{LogicalPath: relative, RelativePath: relative})
		if len(decision.Artifacts) > commissionArtifactMaxCount {
			return errors.New("commission_trial_materials_limit")
		}
		return nil
	})
	if err != nil {
		return nil, err
	}
	if len(decision.Artifacts) == 0 {
		return files, nil
	}
	source, err = filepath.Abs(source)
	if err != nil {
		return nil, err
	}
	verified, err := validateCommissionArtifacts(source, decision)
	if err != nil {
		return nil, err
	}
	if err = os.Mkdir(destination, 0700); err != nil {
		return nil, err
	}
	root, err := os.OpenRoot(source)
	if err != nil {
		return nil, err
	}
	defer root.Close()
	for _, artifact := range verified {
		target := filepath.Join(destination, filepath.FromSlash(artifact.RelativePath))
		if err = os.MkdirAll(filepath.Dir(target), 0700); err != nil {
			return nil, err
		}
		// Bounded verified copies prevent exposing arbitrary original paths to Agent.
		data, err := readCommissionTrialMaterial(root, artifact)
		if err != nil {
			return nil, err
		}
		if err = os.WriteFile(target, data, 0400); err != nil {
			return nil, err
		}
		files = append(files, commissionLocalFile{LogicalPath: artifact.LogicalPath, Path: target, ByteSize: artifact.ByteSize, SHA256: artifact.SHA256})
	}
	return files, nil
}

func readCommissionTrialMaterial(root *os.Root, artifact dispatch.CommissionVerifiedArtifact) ([]byte, error) {
	file, err := root.Open(artifact.RelativePath)
	if err != nil {
		return nil, err
	}
	defer file.Close()
	info, err := file.Stat()
	if err != nil || !info.Mode().IsRegular() {
		return nil, errors.New("commission_trial_material_changed")
	}
	data, err := io.ReadAll(io.LimitReader(file, artifact.ByteSize+1))
	if err != nil {
		return nil, err
	}
	sum := sha256.Sum256(data)
	if int64(len(data)) != artifact.ByteSize || hex.EncodeToString(sum[:]) != artifact.SHA256 {
		return nil, errors.New("commission_trial_material_changed")
	}
	return data, nil
}
