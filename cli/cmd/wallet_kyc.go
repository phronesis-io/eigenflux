package cmd

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"strconv"
	"strings"
	"unicode"
	"unicode/utf8"

	"cli.eigenflux.ai/internal/client"
	"github.com/spf13/cobra"
)

const walletKYCInputLimit = 4096

func readWalletKYCInput(cmd *cobra.Command, target any) (string, error) {
	stdin, _ := cmd.Flags().GetBool("stdin")
	key, _ := cmd.Flags().GetString("idempotency-key")
	if !stdin || key == "" || len(key) > 64 || strings.TrimSpace(key) != key || strings.ContainsAny(key, "\r\n") {
		return "", fmt.Errorf("--stdin and a stable --idempotency-key (1–64 bytes) are required")
	}
	data, err := io.ReadAll(io.LimitReader(cmd.InOrStdin(), walletKYCInputLimit+1))
	if err != nil || len(data) > walletKYCInputLimit {
		return "", fmt.Errorf("cannot read KYC input: expected at most 4096 bytes")
	}
	decoder := json.NewDecoder(bytes.NewReader(data))
	decoder.DisallowUnknownFields()
	if decoder.Decode(target) != nil || decoder.Decode(new(any)) != io.EOF {
		return "", fmt.Errorf("invalid KYC JSON: provide exactly one object with the documented fields")
	}
	return key, nil
}

func validKYCNameAndID(name, id string) bool {
	if name == "" || strings.TrimSpace(name) != name || !utf8.ValidString(name) || utf8.RuneCountInString(name) > 128 || len(id) != 18 {
		return false
	}
	for _, ch := range name {
		if unicode.IsControl(ch) {
			return false
		}
	}
	for i, ch := range id {
		if (ch < '0' || ch > '9') && !(i == 17 && ch == 'X') {
			return false
		}
	}
	return true
}

func printWalletKYC(resp *client.APIResponse, err error, includeVerifyID bool) error {
	if err != nil {
		var apiErr *client.APIError
		if errors.As(err, &apiErr) {
			return &client.APIError{StatusCode: apiErr.StatusCode, Code: apiErr.Code, ErrorCode: apiErr.ErrorCode,
				Msg: http.StatusText(apiErr.StatusCode), RetryAfterSeconds: apiErr.RetryAfterSeconds}
		}
		return fmt.Errorf("KYC request failed; check connectivity and query status before retrying")
	}
	if resp == nil || resp.Code != 0 {
		return fmt.Errorf("KYC request was not accepted")
	}
	var data struct {
		Verification *struct {
			ID         string `json:"verification_id"`
			BindingID  string `json:"binding_id"`
			State      string `json:"state"`
			ExpiresAt  int64  `json:"expires_at"`
			VerifiedAt int64  `json:"verified_at"`
			VerifyID   string `json:"verify_id,omitempty"`
		} `json:"verification"`
	}
	if json.Unmarshal(resp.Data, &data) != nil || data.Verification == nil || data.Verification.BindingID == "" || data.Verification.State == "" {
		return fmt.Errorf("invalid KYC response")
	}
	switch data.Verification.State {
	case "not_assessed", "preparing", "pending", "verified", "rejected", "failed", "expired":
	default:
		return fmt.Errorf("invalid KYC response")
	}
	bindingID, bindingErr := strconv.ParseInt(data.Verification.BindingID, 10, 64)
	verificationID, verificationErr := strconv.ParseInt(data.Verification.ID, 10, 64)
	if bindingErr != nil || bindingID <= 0 || verificationErr != nil || verificationID < 0 || (verificationID == 0 && data.Verification.State != "not_assessed") {
		return fmt.Errorf("invalid KYC response")
	}
	if includeVerifyID && data.Verification.State == "pending" && data.Verification.VerifyID == "" {
		return fmt.Errorf("invalid KYC response")
	}
	if !includeVerifyID || data.Verification.State != "pending" {
		data.Verification.VerifyID = ""
	}
	safe, err := json.Marshal(data)
	if err != nil {
		return fmt.Errorf("invalid KYC response")
	}
	return printResponse(&client.APIResponse{Data: safe})
}

func newWalletKYCCmd() *cobra.Command {
	root := &cobra.Command{Use: "kyc", Short: "Verify identity for the currently bound Alipay account"}
	get := &cobra.Command{Use: "get", Short: "Get current binding KYC status", Args: cobra.NoArgs, RunE: func(_ *cobra.Command, _ []string) error {
		resp, err := newCommissionClient().Get("/wallet/kyc", nil)
		return printWalletKYC(resp, err, false)
	}}
	start := &cobra.Command{Use: "start --stdin --idempotency-key <key>", Short: "Read name and ID from stdin; begin KYC for the bound account", Args: cobra.NoArgs, RunE: func(cmd *cobra.Command, _ []string) error {
		var input struct {
			Name string `json:"user_name"`
			ID   string `json:"cert_no"`
		}
		key, err := readWalletKYCInput(cmd, &input)
		if err != nil {
			return err
		}
		if !validKYCNameAndID(input.Name, input.ID) {
			return fmt.Errorf("KYC requires user_name and an 18-character mainland cert_no")
		}
		resp, err := postMutation(newCommissionClient(), "/wallet/kyc", "wallet.kyc.start", key, input)
		return printWalletKYC(resp, err, true)
	}}
	complete := &cobra.Command{Use: "complete <verification-id> --stdin --idempotency-key <key>", Short: "Complete KYC using a fresh id_verify auth_code supplied as authorization on stdin", Args: cobra.ExactArgs(1), RunE: func(cmd *cobra.Command, args []string) error {
		id, err := numericArgument(args, "verification ID")
		if err != nil {
			return fmt.Errorf("verification ID must be a positive integer")
		}
		var input struct {
			Authorization string `json:"authorization"`
		}
		key, err := readWalletKYCInput(cmd, &input)
		if err != nil {
			return err
		}
		if input.Authorization == "" || len(input.Authorization) > 512 || strings.TrimSpace(input.Authorization) != input.Authorization {
			return fmt.Errorf("KYC requires a fresh authorization value of at most 512 bytes")
		}
		body := map[string]string{"verification_id": strconv.FormatInt(id, 10), "authorization": input.Authorization}
		resp, err := postMutation(newCommissionClient(), "/wallet/kyc/complete", "wallet.kyc.complete", key, body)
		return printWalletKYC(resp, err, false)
	}}
	for _, command := range []*cobra.Command{start, complete} {
		command.Flags().Bool("stdin", false, "read private JSON from stdin; never put identity or authorization in command arguments")
		command.Flags().String("idempotency-key", "", "explicit retry key; use a new key for a new verification attempt")
	}
	root.AddCommand(get, start, complete)
	return root
}

func init() { walletCmd.AddCommand(newWalletKYCCmd()) }
