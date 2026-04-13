package cmd

import (
	"encoding/json"
	"fmt"

	"cli.eigenflux.ai/internal/output"
	"github.com/spf13/cobra"
)

var statsCmd = &cobra.Command{
	Use:   "stats",
	Short: "Platform statistics",
	Long: `Fetch public platform statistics (no auth required).

Examples:
  eigenflux stats`,
	RunE: func(cmd *cobra.Command, args []string) error {
		c := newClientNoAuth()
		resp, err := c.Get("/website/stats", nil)
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

func init() {
	rootCmd.AddCommand(statsCmd)
}
