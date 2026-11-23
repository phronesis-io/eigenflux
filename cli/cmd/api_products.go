package cmd

import (
	"bytes"
	"crypto/ed25519"
	"crypto/rand"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/url"
	"strconv"
	"time"

	"cli.eigenflux.ai/internal/auth"
	"cli.eigenflux.ai/internal/config"
	"cli.eigenflux.ai/internal/gatewaysign"
	"github.com/spf13/cobra"
)

func newAPIProductsCmd() *cobra.Command {
	group := &cobra.Command{Use: "api", Short: "Discover and call platform-managed API products"}
	products := &cobra.Command{Use: "products", Short: "List active products and their fixed price per call", Args: cobra.NoArgs, RunE: func(cmd *cobra.Command, _ []string) error {
		return gatewayRequest(cmd, http.MethodGet, "/v1/products", "", nil)
	}}
	call := &cobra.Command{Use: "call PRODUCT_ID", Short: "Execute a signed call with JSON input on stdin", Args: cobra.ExactArgs(1), RunE: func(cmd *cobra.Command, args []string) error {
		id, err := numericArgument(args, "product ID")
		if err != nil {
			return err
		}
		key, _ := cmd.Flags().GetString("idempotency-key")
		if len(key) == 0 || len(key) > 64 {
			return fmt.Errorf("supply a 1–64 byte --idempotency-key and reuse it for retries")
		}
		body, err := io.ReadAll(io.LimitReader(cmd.InOrStdin(), 1<<20+1))
		if err != nil {
			return err
		}
		if len(body) > 1<<20 || !json.Valid(body) {
			return fmt.Errorf("stdin must contain valid JSON of at most 1 MiB")
		}
		return gatewayRequest(cmd, http.MethodPost, "/v1/call/"+strconv.FormatInt(id, 10), key, body)
	}}
	addIdempotencyFlag(call)
	group.PersistentFlags().String("gateway", "", "override the API product gateway origin")
	group.AddCommand(products, call)
	return group
}

func gatewayRequest(cmd *cobra.Command, method, path, key string, body []byte) error {
	cfg, err := config.Load()
	if err != nil {
		return err
	}
	profile, err := cfg.GetActive(serverFlag)
	if err != nil {
		return err
	}
	origin, _ := cmd.Flags().GetString("gateway")
	if origin == "" {
		origin = profile.Endpoint
	}
	endpoint, err := url.Parse(origin)
	if err != nil || endpoint.Hostname() == "" || endpoint.User != nil || endpoint.RawQuery != "" || endpoint.Fragment != "" || (endpoint.Path != "" && endpoint.Path != "/") {
		return fmt.Errorf("invalid gateway origin")
	}
	ip := net.ParseIP(endpoint.Hostname())
	if endpoint.Scheme != "https" && !(endpoint.Scheme == "http" && (endpoint.Hostname() == "localhost" || ip.IsLoopback())) {
		return fmt.Errorf("gateway requires HTTPS or loopback HTTP")
	}
	endpoint.Path = path
	req, err := http.NewRequestWithContext(cmd.Context(), method, endpoint.String(), bytes.NewReader(body))
	if err != nil {
		return err
	}
	if method == http.MethodPost {
		public, private, err := auth.LoadIdentity(profile.Name)
		if err != nil {
			return err
		}
		var entropy [16]byte
		if _, err := rand.Read(entropy[:]); err != nil {
			return err
		}
		nonce := base64.RawURLEncoding.EncodeToString(entropy[:])
		timestamp := strconv.FormatInt(time.Now().UnixMilli(), 10)
		transcript, err := gatewaysign.Canonical(method, endpoint.Host, endpoint.EscapedPath(), endpoint.RawQuery, timestamp, nonce, key, body)
		if err != nil {
			return err
		}
		req.Header.Set("Content-Type", "application/json")
		req.Header.Set("Idempotency-Key", key)
		req.Header.Set("EF-Key", auth.IdentityFingerprint(public))
		req.Header.Set("EF-Nonce", nonce)
		req.Header.Set("EF-Timestamp", timestamp)
		req.Header.Set("EF-Signature", base64.RawURLEncoding.EncodeToString(ed25519.Sign(private, transcript)))
	}
	client := http.Client{Timeout: 2 * time.Minute, CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }}
	resp, err := client.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		var result struct {
			ErrorCode string `json:"error_code"`
		}
		_ = json.NewDecoder(io.LimitReader(resp.Body, 8192)).Decode(&result)
		return fmt.Errorf("gateway HTTP %d %s", resp.StatusCode, result.ErrorCode)
	}
	_, err = io.Copy(cmd.OutOrStdout(), resp.Body)
	return err
}

func init() { rootCmd.AddCommand(newAPIProductsCmd()) }
