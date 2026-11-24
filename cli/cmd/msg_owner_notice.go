package cmd

import (
	"encoding/json"
	"fmt"
	"net/http"
	"regexp"
	"strconv"

	"cli.eigenflux.ai/internal/output"
	"github.com/spf13/cobra"
)

var msgNoticeResultID = regexp.MustCompile(`^[a-f0-9]{64}$`)

func init() {
	list := &cobra.Command{
		Use: "important", Short: "List pending important private-message results", Args: cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			params := map[string]string{"view": "pending"}
			if conversation, _ := cmd.Flags().GetString("conv-id"); conversation != "" {
				if id, err := strconv.ParseInt(conversation, 10, 64); err != nil || id <= 0 {
					return fmt.Errorf("invalid --conv-id")
				}
				params["conversations"] = conversation
			}
			if cursor, _ := cmd.Flags().GetString("cursor"); cursor != "" {
				params["cursor"] = cursor
			}
			c, _, err := newV2ClientForServer(activeServerName(), true)
			if err != nil {
				return err
			}
			resp, err := c.Get("/pm/owner-notices", params)
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
	list.Flags().String("conv-id", "", "Only list results for this conversation")
	list.Flags().String("cursor", "", "Continuation cursor from the previous page")

	mark := &cobra.Command{
		Use: "mark-handled", Short: "Report completion of an important private-message result", Args: cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			resultID, _ := cmd.Flags().GetString("result-id")
			if !msgNoticeResultID.MatchString(resultID) {
				return fmt.Errorf("expected the original important private-message --result-id")
			}
			c, _, err := newV2ClientForServer(activeServerName(), true)
			if err != nil {
				return err
			}
			c.HTTPClient.CheckRedirect = func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }
			resp, err := c.Post("/pm/owner-notices/"+resultID+"/handled", struct{}{})
			if err != nil {
				return err
			}
			var receipt struct {
				ResultID  string `json:"result_id"`
				HandledAt int64  `json:"handled_at"`
			}
			if resp.HTTPStatus != http.StatusOK || resp.Code != 0 || json.Unmarshal(resp.Data, &receipt) != nil ||
				receipt.ResultID != resultID || receipt.HandledAt <= 0 {
				return fmt.Errorf("the server did not confirm the important private-message update")
			}
			output.PrintData(json.RawMessage(resp.Data), resolveFormat())
			return nil
		},
	}
	mark.Flags().String("result-id", "", "Original result_id returned by msg important")
	msgCmd.AddCommand(list, mark)
}
