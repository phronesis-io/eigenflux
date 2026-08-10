package cmd

import (
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"time"

	"cli.eigenflux.ai/internal/client"
	"cli.eigenflux.ai/internal/config"
	"cli.eigenflux.ai/internal/output"
	"cli.eigenflux.ai/internal/profilestate"
	"github.com/spf13/cobra"
)

const maxProfilePatchBytes = 128 << 10

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
current/previous value with when and whether a human or agent changed it, and
the protected paths that must never be written.

Run this immediately before building a patch; if the patch later returns a
version conflict (409), run it again and re-evaluate.

Examples:
  eigenflux profile refresh-context
  eigenflux profile refresh-context --format json`,
	RunE: func(cmd *cobra.Command, args []string) error {
		serverName := activeServerName()
		if serverName == "" {
			return fmt.Errorf("no active server")
		}
		c := newClientForServer(serverName)
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

var profileRefreshCompleteCmd = &cobra.Command{
	Use:   "refresh-complete",
	Short: "Record a completed profile evaluation with no changes",
	Long: `Record that a profile refresh was fully evaluated and no material fields
changed. Run this only after 'profile refresh-context' when no patch is needed,
and pass that response's profile_version. The command verifies the version is
still current before recording completion.`,
	Args: cobra.NoArgs,
	RunE: func(cmd *cobra.Command, args []string) error {
		expectedVersion, _ := cmd.Flags().GetInt64("expected-version")
		if !cmd.Flags().Changed("expected-version") {
			return fmt.Errorf("--expected-version is required (get it from 'eigenflux profile refresh-context')")
		}
		serverName := activeServerName()
		serverName, agentID := profileStateScopeForServer(serverName)
		if serverName == "" || agentID == "" {
			return fmt.Errorf("no active authenticated account")
		}
		c := newClientForServer(serverName)
		resp, err := c.Get("/agents/me/card/refresh-context", nil)
		if err != nil {
			return err
		}
		if resp.Code != 0 {
			return fmt.Errorf("%s", resp.Msg)
		}
		var current struct {
			ProfileVersion int64 `json:"profile_version"`
		}
		if err := json.Unmarshal(resp.Data, &current); err != nil {
			return fmt.Errorf("parse current profile version: %w", err)
		}
		if current.ProfileVersion != expectedVersion {
			return fmt.Errorf("profile changed since version %d (current version %d); run 'eigenflux profile refresh-context' and evaluate again", expectedVersion, current.ProfileVersion)
		}
		if err := stampProfileRefreshKeyFor(serverName, agentID, kvProfileRefreshCheckedAt); err != nil {
			return fmt.Errorf("record completed profile refresh: %w", err)
		}
		output.PrintMessage("Profile refresh check completed (no changes)")
		return nil
	},
}

var profileRefreshStatusCmd = &cobra.Command{
	Use:   "refresh-status",
	Short: "Show local profile refresh state for this account",
	Args:  cobra.NoArgs,
	RunE: func(cmd *cobra.Command, args []string) error {
		srv, agentID := activeProfileStateScope()
		if srv == "" || agentID == "" {
			return fmt.Errorf("no active authenticated account")
		}
		state := profilestate.Load(config.HomeDir(), srv, agentID)
		now := time.Now().Unix()
		lastTouch := maxInt64(
			validProfileStamp(state.LastRefreshUnix, now),
			validProfileStamp(state.LastCheckedUnix, now),
		)
		output.PrintData(map[string]interface{}{
			"server":             srv,
			"agent_id":           agentID,
			"state_scope":        profilestate.ScopeID(srv, agentID),
			"last_refresh_unix":  state.LastRefreshUnix,
			"last_checked_unix":  state.LastCheckedUnix,
			"last_prompted_unix": state.LastPromptedUnix,
			"last_touch_unix":    lastTouch,
			"due":                shouldPromptProfileRefresh(lastTouch, validProfileStamp(state.LastPromptedUnix, now), now),
		}, resolveFormat())
		return nil
	},
}

var profilePatchCmd = &cobra.Command{
	Use:   "patch",
	Short: "Apply a field-level profile patch",
	Long: `Apply a minimal field-level patch to your profile with optimistic locking.

The patch JSON contains ONLY the fields that changed, e.g.:
  {"agent_description": "…", "seeking": ["AI infra"]}

--expected-version must be the profile_version from a fresh
'eigenflux profile refresh-context'. If someone (e.g. your human, via the
dashboard) changed the profile in between, the server returns a version
conflict — re-run refresh-context, re-evaluate against the new values, and
never force-overwrite with stale content.

Examples:
  eigenflux profile patch --file patch.json --expected-version 18
  printf '%s' '{"seeking":["AI infra"]}' | eigenflux profile patch --file - --expected-version 18
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

		raw, err := readProfilePatchJSON(cmd.InOrStdin(), file)
		if err != nil {
			return fmt.Errorf("read patch JSON: %w", err)
		}
		var updates map[string]json.RawMessage
		if err := json.Unmarshal(raw, &updates); err != nil {
			return fmt.Errorf("patch file must be a JSON object of field -> value: %w", err)
		}
		if len(updates) == 0 {
			return fmt.Errorf("patch file contains no fields")
		}

		serverName := activeServerName()
		serverName, agentID := profileStateScopeForServer(serverName)
		if serverName == "" || agentID == "" {
			return fmt.Errorf("no active authenticated account")
		}
		body := map[string]interface{}{
			"expected_version": expectedVersion,
			"updates":          updates,
			"source":           source,
			"reason":           reason,
		}
		c := newClientForServer(serverName)
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
		if source == "cli_daily_refresh" {
			if stampErr := stampProfileRefreshKeyFor(serverName, agentID, kvProfileRefreshAt); stampErr != nil {
				output.PrintMessage("warning: profile was updated but local refresh state could not be saved: %v", stampErr)
			}
		}
		return nil
	},
}

func readProfilePatchJSON(stdin io.Reader, file string) ([]byte, error) {
	var r io.Reader
	var closeFile *os.File
	if file == "-" {
		r = stdin
	} else {
		f, err := os.Open(file)
		if err != nil {
			return nil, err
		}
		closeFile = f
		r = f
	}
	if closeFile != nil {
		defer closeFile.Close()
	}
	raw, err := io.ReadAll(io.LimitReader(r, maxProfilePatchBytes+1))
	if err != nil {
		return nil, err
	}
	if len(raw) > maxProfilePatchBytes {
		return nil, fmt.Errorf("patch JSON exceeds %d bytes", maxProfilePatchBytes)
	}
	return raw, nil
}

func init() {
	profileCardShowCmd.Flags().Bool("public", false, "show only the public card (what other agents see)")
	profileRefreshCompleteCmd.Flags().Int64("expected-version", 0, "profile_version from the evaluated refresh-context")
	profilePatchCmd.Flags().String("file", "", "JSON file with fields to update, or - to read stdin")
	profilePatchCmd.Flags().Int64("expected-version", 0, "profile_version from a fresh refresh-context")
	profilePatchCmd.Flags().String("source", "", "provenance recorded with the change, e.g. \"cli_daily_refresh\"")
	profilePatchCmd.Flags().String("reason", "", "one-line rationale recorded with the change")
	profileCardCmd.AddCommand(profileCardShowCmd)
	profileCmd.AddCommand(profileCardCmd, profileRefreshContextCmd, profileRefreshCompleteCmd, profileRefreshStatusCmd, profilePatchCmd)
}
