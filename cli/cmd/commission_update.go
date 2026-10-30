package cmd

import (
	"encoding/json"
	"fmt"
	"strconv"
	"strings"

	"cli.eigenflux.ai/internal/commissionapi"
	"github.com/spf13/cobra"
	"github.com/spf13/pflag"
)

// The service still accepts a complete replacement. Merge only Changed flags
// locally, preserving omitted fields (including future service-owned fields).
func mergeCommissionDraft(cmd *cobra.Command, draft map[string]json.RawMessage) ([]string, error) {
	changed := []string{}
	probe := &cobra.Command{}
	addCommissionInputFlags(probe)
	cmd.Flags().Visit(func(f *pflag.Flag) {
		if probe.Flags().Lookup(f.Name) == nil {
			return
		}
		key := strings.ReplaceAll(f.Name, "-", "_")
		var value any
		switch f.Value.Type() {
		case "bool":
			value, _ = cmd.Flags().GetBool(f.Name)
		case "int64":
			value, _ = cmd.Flags().GetInt64(f.Name)
		default:
			text, _ := cmd.Flags().GetString(f.Name)
			value = text
			if f.Name == "tags" {
				value = commaList(text)
			}
			if f.Name == "fulfillment-skill" {
				value = strings.TrimSpace(text)
			}
		}
		draft[key], _ = json.Marshal(value)
		changed = append(changed, key)
	})
	if len(changed) == 0 {
		return nil, fmt.Errorf("no draft fields supplied; specify at least one field to update")
	}
	raw, err := json.Marshal(draft)
	if err != nil {
		return nil, err
	}
	var input commissionapi.CommissionInput
	if err := json.Unmarshal(raw, &input); err != nil {
		return nil, fmt.Errorf("invalid draft fields: %w", err)
	}
	if err := validateCommissionInput(input); err != nil {
		return nil, fmt.Errorf("invalid merged draft: %w; correct the named fields and retry", err)
	}
	return changed, nil
}

func updateCommissionDraft(cmd *cobra.Command, args []string) error {
	id, err := numericArgument(args, "commission ID")
	if err != nil {
		return err
	}
	version, _ := cmd.Flags().GetInt64("expected-version")
	if version <= 0 {
		return fmt.Errorf("--expected-version must be positive; read commission get and use its draft_version")
	}
	api := newCommissionClient()
	path := "/commissions/" + strconv.FormatInt(id, 10)
	response, err := api.Get(path, nil)
	if err != nil {
		return fmt.Errorf("read draft failed; no update submitted: %w", err)
	}
	if response.Code != 0 {
		return fmt.Errorf("read draft failed; no update submitted: %s", response.Msg)
	}
	var data struct {
		Commission struct {
			Definition struct {
				ID notificationID `json:"commission_id"`
			} `json:"definition"`
			Draft   map[string]json.RawMessage `json:"draft"`
			Version int64                      `json:"draft_version"`
		} `json:"commission"`
	}
	if err := json.Unmarshal(response.Data, &data); err != nil || int64(data.Commission.Definition.ID) != id || len(data.Commission.Draft) == 0 || data.Commission.Version <= 0 {
		return fmt.Errorf("invalid commission draft response; no update submitted; inspect commission get")
	}
	if data.Commission.Version != version {
		return fmt.Errorf("draft version changed (expected %d, current %d); no update submitted; read commission get, check whether the intended change already succeeded, and review the latest draft before retrying", version, data.Commission.Version)
	}
	changed, err := mergeCommissionDraft(cmd, data.Commission.Draft)
	if err != nil {
		return err
	}
	body := map[string]any{"expected_draft_version": version, "input": data.Commission.Draft}
	key, _ := cmd.Flags().GetString("idempotency-key")
	response, err = putMutation(api, path+"/draft", "commission.update."+strconv.FormatInt(id, 10), key, body)
	if err != nil {
		return fmt.Errorf("draft update failed or unconfirmed; inspect commission get before retrying; never automatically overwrite a newer version: %w", err)
	}
	if response.Code != 0 {
		return fmt.Errorf("draft update rejected: %s; read commission get and correct the reported fields or version before retrying", response.Msg)
	}
	// Preserve the service response and add guidance without guessing a version.
	var result map[string]json.RawMessage
	if err := json.Unmarshal(response.Data, &result); err != nil || result == nil {
		return fmt.Errorf("draft update response unreadable; update may have succeeded; inspect commission get before retrying")
	}
	guidance, _ := json.Marshal(map[string]any{"changed_fields": changed, "effect": "draft_only", "next_action": "Read commission get to verify the draft and its version. Publish separately after approval; every publish runs a fresh trial. Existing public revisions and Orders are unchanged."})
	result["update_guidance"] = guidance
	response.Data, err = json.Marshal(result)
	if err != nil {
		return err
	}
	return printResponse(response)
}
