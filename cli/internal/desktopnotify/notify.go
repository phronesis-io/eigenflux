// Package desktopnotify delivers clickable, per-user OS notifications.
package desktopnotify

import (
	"context"
	"errors"
	"fmt"
	"net/url"
	"strings"
	"unicode"
	"unicode/utf8"
)

var (
	ErrUnsupported   = errors.New("desktop notifications require macOS or Windows")
	ErrPermission    = errors.New("enable notifications for EigenFlux in system notification settings")
	ErrHelperMissing = errors.New("macOS notification helper missing; install the complete CLI bundle")
)

type Message struct {
	ID    string `json:"id"`
	Title string `json:"title"`
	Body  string `json:"body"`
	URL   string `json:"url"`
}

type Notifier struct{}

func New() *Notifier { return &Notifier{} }

// Setup installs the per-user integration and requests notification permission.
// It should run once during explicit notification setup, never for each event.
func (*Notifier) Setup(ctx context.Context) error    { return platformRequest(ctx, "enable", Message{}) }
func (n *Notifier) Enable(ctx context.Context) error { return n.Setup(ctx) }

// Check does not display a notification or request permission.
func (*Notifier) Check(ctx context.Context) error { return platformRequest(ctx, "check", Message{}) }

// Show confirms OS submission, not that a banner was seen or clicked. Focus and
// notification settings remain under the user's control.
func (*Notifier) Show(ctx context.Context, message Message) error {
	if err := Validate(message); err != nil {
		return err
	}
	return platformRequest(ctx, "show", message)
}

func Validate(message Message) error {
	for _, field := range []struct {
		name, value string
		max         int
	}{
		{"id", message.ID, 256}, {"title", message.Title, 200}, {"body", message.Body, 2000},
	} {
		if !utf8.ValidString(field.value) || strings.TrimSpace(field.value) == "" || utf8.RuneCountInString(field.value) > field.max {
			return fmt.Errorf("invalid notification %s", field.name)
		}
		for _, r := range field.value {
			if unicode.IsControl(r) && r != '\n' {
				return fmt.Errorf("invalid notification %s", field.name)
			}
		}
	}
	return ValidateURL(message.URL)
}

// ValidateURL rejects command protocols, credentials and non-local cleartext.
func ValidateURL(raw string) error {
	if len(raw) > 4096 || strings.ContainsAny(raw, "\r\n\x00\\") {
		return errors.New("invalid notification URL")
	}
	u, err := url.Parse(raw)
	if err != nil || u.Hostname() == "" || u.User != nil || u.Opaque != "" {
		return errors.New("invalid notification URL")
	}
	local := strings.EqualFold(u.Hostname(), "localhost") || u.Hostname() == "127.0.0.1" || u.Hostname() == "::1"
	if u.Scheme != "https" && !(u.Scheme == "http" && local) {
		return errors.New("notification URL must use HTTPS")
	}
	for key := range u.Query() {
		switch strings.ToLower(key) {
		case "token", "access_token", "refresh_token", "api_key", "password", "secret":
			return errors.New("notification URL must not contain credentials")
		}
	}
	return nil
}
