package email

import (
	"bytes"
	"context"
	"eigenflux_server/pkg/json"
	"fmt"
	"net/http"
)

const resendAPIURL = "https://api.resend.com/emails"

type resendSender struct {
	apiKey    string
	fromEmail string
	client    *http.Client
}

// NewResendSender creates a production email sender using the Resend HTTP API.
func NewResendSender(apiKey, fromEmail string) Sender {
	return &resendSender{
		apiKey:    apiKey,
		fromEmail: fromEmail,
		client:    &http.Client{},
	}
}

type resendEmailRequest struct {
	From    string   `json:"from"`
	To      []string `json:"to"`
	Subject string   `json:"subject"`
	HTML    string   `json:"html"`
}

func (s *resendSender) SendLoginVerifyMail(ctx context.Context, to string, otpCode string) error {
	html := buildLoginVerifyHTML(otpCode)

	payload := resendEmailRequest{
		From:    s.fromEmail,
		To:      []string{to},
		Subject: "Your login verification code",
		HTML:    html,
	}

	body, err := json.Marshal(payload)
	if err != nil {
		return fmt.Errorf("email: marshal request: %w", err)
	}

	req, err := http.NewRequestWithContext(ctx, http.MethodPost, resendAPIURL, bytes.NewReader(body))
	if err != nil {
		return fmt.Errorf("email: create request: %w", err)
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Authorization", "Bearer "+s.apiKey)

	resp, err := s.client.Do(req)
	if err != nil {
		return fmt.Errorf("email: send request: %w", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return fmt.Errorf("email: resend API returned status %d", resp.StatusCode)
	}

	return nil
}

func buildLoginVerifyHTML(otpCode string) string {
	return fmt.Sprintf(`<!DOCTYPE html>
<html>
<head>
  <meta charset="UTF-8">
  <title>Login Verification</title>
</head>
<body style="font-family: sans-serif; max-width: 600px; margin: 0 auto; padding: 24px; color: #222;">
  <h2>Login Verification</h2>
  <p>Please enter the following verification code to complete your login. The code expires in <strong>10 minutes</strong>.</p>

  <h3>Verification Code</h3>
  <div style="font-size: 32px; font-weight: bold; letter-spacing: 8px; padding: 16px; background: #f5f5f5; border-radius: 6px; display: inline-block;">
    %s
  </div>

  <p style="font-size: 12px; color: #888;">
    If you did not request this login, please ignore this email. The code will expire automatically.
  </p>
</body>
</html>`, otpCode)
}
