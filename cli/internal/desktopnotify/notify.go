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
	ErrUnsupported              = errors.New("desktop notifications require macOS or Windows")
	ErrPermission               = errors.New("enable notifications for EigenFlux in system notification settings")
	ErrPermissionRequired       = errors.New("notification permission has not been requested; run eigenflux notify setup")
	ErrHelperMissing            = errors.New("macOS notification helper unavailable; run eigenflux notify setup with the complete CLI bundle")
	ErrPendingFirstNotification = errors.New("Windows sender registered; OS notification setting is unknown until the first notification")
)

type Message struct {
	ID    string `json:"id"`
	Title string `json:"title"`
	Body  string `json:"body"`
	URL   string `json:"url"`
}

type Notifier struct{ observe func(Observation) }

func New() *Notifier { return &Notifier{} }

func NewWithObserver(observer func(Observation)) *Notifier { return &Notifier{observe: observer} }

// Setup installs the per-user integration. macOS requests permission; Windows
// stays silent and reports an unknown setting before the first real notification.
// It should run once during explicit notification setup, never for each event.
func (n *Notifier) Setup(ctx context.Context) error  { return n.request(ctx, "enable", Message{}) }
func (n *Notifier) Enable(ctx context.Context) error { return n.Setup(ctx) }

// Prepare repairs the native integration without requesting macOS permission.
func (n *Notifier) Prepare(ctx context.Context) error { return n.request(ctx, "prepare", Message{}) }

// Check does not display a notification or request permission.
func (n *Notifier) Check(ctx context.Context) error { return n.request(ctx, "check", Message{}) }

// Show confirms OS submission, not that a banner was seen or clicked. Focus and
// notification settings remain under the user's control.
func (n *Notifier) Show(ctx context.Context, message Message) error {
	if err := Validate(message); err != nil {
		return err
	}
	return n.request(ctx, "show", message)
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
