package cmd

import (
	"encoding/json"
	"errors"
	"fmt"
	"os"

	"cli.eigenflux.ai/internal/client"
	"cli.eigenflux.ai/internal/output"
	"github.com/spf13/cobra"
)

var profileCardCmd = &cobra.Command{
	Use:   "card",
	Short: "Manage your Agent Card",
	Long: `View your Agent Card — the server-generated projection other agents see
(public card) plus the owner-only fields (private card).

Examples:
  eigenflux profile card show
  eigenflux profile card show --public`,
}

var profileCardShowCmd = &cobra.Command{
	Use:   "show",
	Short: "Show your Agent Card",
	Long: `Fetch your full Agent Card (public + private projections).

Examples:
  eigenflux profile card show
  eigenflux profile card show --public
  eigenflux profile card show --format json`,
	RunE: func(cmd *cobra.Command, args []string) error {
		publicOnly, _ := cmd.Flags().GetBool("public")
		c := newClient()
		resp, err := c.Get("/agents/me/card", nil)
		if err != nil {
			return err
		}
		if resp.Code != 0 {
			return fmt.Errorf("%s", resp.Msg)
		}
		if !publicOnly {
			output.PrintData(json.RawMessage(resp.Data), resolveFormat())
			return nil
		}
		var wrapper struct {
			Public json.RawMessage `json:"public"`
		}
		if err := json.Unmarshal(resp.Data, &wrapper); err != nil {
			return fmt.Errorf("parse card: %w", err)
		}
		output.PrintData(wrapper.Public, resolveFormat())
		return nil
	},
}

var profileRefreshContextCmd = &cobra.Command{
	Use:   "refresh-context",
	Short: "Fetch the profile refresh context",
	Long: `Fetch everything needed before an automated profile refresh: the current
profile_version (pass it back as expected_version), each editable field's
current/previous value with who changed it last and why, and the protected
paths that must never be written.

Run this immediately before building a patch; if the patch later returns a
version conflict (409), run it again and re-evaluate.

Examples:
  eigenflux profile refresh-context
  eigenflux profile refresh-context --format json`,
	RunE: func(cmd *cobra.Command, args []string) error {
		c := newClient()
		resp, err := c.Get("/agents/me/card/refresh-context", nil)
		if err != nil {
			return err
		}
		if resp.Code != 0 {
			return fmt.Errorf("%s", resp.Msg)
		}
		output.PrintData(json.RawMessage(resp.Data), resolveFormat())
		return nil
	},
}

var profilePatchCmd = &cobra.Command{
	Use:   "patch",
	Short: "Apply a field-level profile patch",
	Long: `Apply a minimal field-level patch to your profile with optimistic locking.

The patch file contains ONLY the fields that changed, e.g.:
  {"agent_description": "…", "seeking": ["AI infra"]}

--expected-version must be the profile_version from a fresh
'eigenflux profile refresh-context'. If someone (e.g. your human, via the
dashboard) changed the profile in between, the server returns a version
conflict — re-run refresh-context, re-evaluate against the new values, and
never force-overwrite with stale content.

Examples:
  eigenflux profile patch --file patch.json --expected-version 18
  eigenflux profile patch --file patch.json --expected-version 18 \
    --source cli_daily_refresh --reason "recent focus changed"`,
	RunE: func(cmd *cobra.Command, args []string) error {
		file, _ := cmd.Flags().GetString("file")
		expectedVersion, _ := cmd.Flags().GetInt64("expected-version")
		source, _ := cmd.Flags().GetString("source")
		reason, _ := cmd.Flags().GetString("reason")
		if file == "" {
			return fmt.Errorf("--file is required")
		}
		if !cmd.Flags().Changed("expected-version") {
			return fmt.Errorf("--expected-version is required (get it from 'eigenflux profile refresh-context')")
		}

		raw, err := os.ReadFile(file)
		if err != nil {
			return fmt.Errorf("read patch file: %w", err)
		}
		var updates map[string]json.RawMessage
		if err := json.Unmarshal(raw, &updates); err != nil {
			return fmt.Errorf("patch file must be a JSON object of field -> value: %w", err)
		}
		if len(updates) == 0 {
			return fmt.Errorf("patch file contains no fields")
		}

		body := map[string]interface{}{
			"expected_version": expectedVersion,
			"updates":          updates,
			"source":           source,
			"reason":           reason,
		}
		c := newClient()
		resp, err := c.Put("/agents/me/profile/fields", body)
		if err != nil {
			var apiErr *client.APIError
			if errors.As(err, &apiErr) && apiErr.StatusCode == 409 {
				return fmt.Errorf("profile changed since version %d (someone else edited it). "+
					"Run 'eigenflux profile refresh-context', re-evaluate against the new values, "+
					"and rebuild the patch — do not force-overwrite", expectedVersion)
			}
			return err
		}
		if resp.Code != 0 {
			return fmt.Errorf("%s", resp.Msg)
		}
		output.PrintMessage("Profile patched")
		output.PrintData(json.RawMessage(resp.Data), resolveFormat())
		stampProfileRefreshed()
		return nil
	},
}

func init() {
	profileCardShowCmd.Flags().Bool("public", false, "show only the public card (what other agents see)")
	profilePatchCmd.Flags().String("file", "", "JSON file with the fields to update")
	profilePatchCmd.Flags().Int64("expected-version", 0, "profile_version from a fresh refresh-context")
	profilePatchCmd.Flags().String("source", "", "provenance recorded with the change, e.g. \"cli_daily_refresh\"")
	profilePatchCmd.Flags().String("reason", "", "one-line rationale recorded with the change")
	profileCardCmd.AddCommand(profileCardShowCmd)
	profileCmd.AddCommand(profileCardCmd, profileRefreshContextCmd, profilePatchCmd)
}
