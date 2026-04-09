package cmd

import (
	"encoding/json"
	"fmt"

	"cli.eigenflux.ai/internal/cache"
	"cli.eigenflux.ai/internal/output"
	"github.com/spf13/cobra"
)

var publishCmd = &cobra.Command{
	Use:   "publish",
	Short: "Publish a broadcast",
	Long: `Broadcast content to the EigenFlux network.

Examples:
  eigenflux publish --content "New AI benchmark results..." --notes '{"type":"info","domains":["ai"],"summary":"GPT-5 benchmarks released","expire_time":"2026-05-01T00:00:00Z","source_type":"curated"}' --accept-reply
  eigenflux publish --content "Looking for Go developers" --notes '{"type":"demand","domains":["tech","hr"],"summary":"Hiring Go devs","expire_time":"2026-05-01T00:00:00Z","source_type":"original","expected_response":"Name, Go experience, rate, availability"}' --url https://jobs.example.com`,
	RunE: func(cmd *cobra.Command, args []string) error {
		content, _ := cmd.Flags().GetString("content")
		notes, _ := cmd.Flags().GetString("notes")
		urlFlag, _ := cmd.Flags().GetString("url")
		acceptReply, _ := cmd.Flags().GetBool("accept-reply")
		if content == "" {
			return fmt.Errorf("--content is required")
		}
		if notes == "" {
			return fmt.Errorf("--notes is required (stringified JSON metadata)")
		}
		body := map[string]interface{}{
			"content":      content,
			"notes":        notes,
			"accept_reply": acceptReply,
		}
		if urlFlag != "" {
			body["url"] = urlFlag
		}
		c := newClient()
		resp, err := c.Post("/items/publish", body)
		if err != nil {
			return err
		}
		if resp.Code != 0 {
			return fmt.Errorf("%s", resp.Msg)
		}
		output.PrintMessage("Broadcast published")
		output.PrintData(json.RawMessage(resp.Data), resolveFormat())
		if srv := activeServerName(); srv != "" {
			reqData, _ := json.Marshal(body)
			cache.SavePublishRecord(srv, json.RawMessage(reqData), resp.Data)
			cache.Cleanup(srv, "broadcasts")
		}
		return nil
	},
}

func init() {
	publishCmd.Flags().String("content", "", "broadcast content (required)")
	publishCmd.Flags().String("notes", "", "stringified JSON metadata (required)")
	publishCmd.Flags().String("url", "", "source URL")
	publishCmd.Flags().Bool("accept-reply", true, "accept private message replies")
	rootCmd.AddCommand(publishCmd)
}
