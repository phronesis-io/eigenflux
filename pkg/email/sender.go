package email

import "context"

type contextKey string

const ChallengeIDKey contextKey = "challenge_id"

// Sender defines the interface for sending authentication emails.
type Sender interface {
	// SendLoginVerifyMail sends an OTP verification email.
	SendLoginVerifyMail(ctx context.Context, to string, otpCode string) error
}

// AccountRecoveryNotifier is optional so authentication senders that only
// support OTP delivery remain source-compatible.
type AccountRecoveryNotifier interface {
	SendAccountRecoveryMail(ctx context.Context, to, agentName string) error
}
