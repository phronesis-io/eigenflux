package cmd

import (
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"strings"
	"time"

	"cli.eigenflux.ai/internal/client"
	"cli.eigenflux.ai/internal/config"
)

// reportProvisionedInstall records the running CLI's installation separately
// from the identity attribution already committed by V2 provision. The server
// deduplicates conversions by ref; reprovisioning can retry a missed report.
func reportProvisionedInstall(server config.Server, ref string, meta client.Meta) error {
	if !refRe.MatchString(ref) {
		return fmt.Errorf("invalid installation referral")
	}
	c := client.New(strings.TrimRight(server.Endpoint, "/")+"/api/v1", "", version, meta)
	c.HTTPClient.Timeout = 5 * time.Second
	var lastErr error
	for attempt := 0; attempt < 3; attempt++ {
		lastErr = sendProvisionedInstallReport(c, ref)
		if lastErr == nil {
			return nil
		}
		var apiErr *client.APIError
		if errors.As(lastErr, &apiErr) && apiErr.StatusCode != http.StatusRequestTimeout &&
			apiErr.StatusCode != http.StatusTooManyRequests && apiErr.StatusCode < 500 {
			return lastErr
		}
		if attempt < 2 {
			time.Sleep(time.Duration(attempt+1) * 250 * time.Millisecond)
		}
	}
	return lastErr
}

func sendProvisionedInstallReport(c *client.Client, ref string) error {
	response, err := c.Post("/install/report", map[string]interface{}{
		"ref":      ref,
		"metadata": map[string]string{"via": "cli_v2_provision"},
	})
	if err != nil {
		return err
	}
	if response.HTTPStatus != 200 || response.Code != 0 {
		return fmt.Errorf("installation report rejected (HTTP %d, code %d)", response.HTTPStatus, response.Code)
	}
	var acknowledgement struct {
		Converted   *bool `json:"converted"`
		Attribution struct {
			Ref string `json:"ref"`
		} `json:"attribution"`
	}
	if json.Unmarshal(response.Data, &acknowledgement) != nil || acknowledgement.Converted == nil || acknowledgement.Attribution.Ref != ref {
		return fmt.Errorf("invalid installation report acknowledgement")
	}
	return nil
}
