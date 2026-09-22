//go:build windows

package dispatch

import (
	"context"
	"errors"
	"os/exec"
	"strings"
	"testing"
)

func TestWindowsLongNativePromptNeedsUserBeforeStart(t *testing.T) {
	for _, host := range []string{"openclaw", "hermes"} {
		t.Run(host, func(t *testing.T) {
			b := fixtureBinding(t, "native", "exit")
			b.Host = host
			b.HostAgent = "bound"
			_, err := (Runner{Binding: b}).Run(context.Background(), Request{Prompt: strings.Repeat("x", 40000)})
			if !errors.Is(err, ErrNeedsUser) || !errors.Is(err, ErrCommandLineTooLong) {
				t.Fatalf("valid long prompt must be classified before process start: %v", err)
			}
		})
	}
}

func TestWindowsCommandLineLimit(t *testing.T) {
	for _, tc := range []struct {
		name    string
		arg     string
		tooLong bool
	}{
		{"short", "a message with spaces", false},
		{"boundary", strings.Repeat("x", 32760), false},
		{"over-boundary", strings.Repeat("x", 32761), true},
		{"quoted", strings.Repeat(`"`, 20000), true},
		{"utf16", strings.Repeat("😀", 18000), true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			err := validateCommandLine(exec.Command("agent", tc.arg))
			if errors.Is(err, ErrNeedsUser) != tc.tooLong {
				t.Fatalf("tooLong=%v error=%v", tc.tooLong, err)
			}
			if errors.Is(err, ErrCommandLineTooLong) != tc.tooLong {
				t.Fatalf("missing precise command-line diagnosis: %v", err)
			}
		})
	}
}
