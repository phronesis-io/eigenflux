package cmd

import (
	"os"
	"strconv"
	"strings"
	"testing"
	"time"

	"cli.eigenflux.ai/internal/config"
	"github.com/spf13/cobra"
)

func commandByPath(t *testing.T, path ...string) *cobra.Command {
	t.Helper()
	command, _, err := rootCmd.Find(path)
	if err != nil {
		t.Fatalf("find %v: %v", path, err)
	}
	if command.Name() != path[len(path)-1] {
		t.Fatalf("resolved %v to %q", path, command.Name())
	}
	return command
}

// Help text carries the limit wherever a user can start work. Identity
// comparison appears only under wallet: it is a payout step a functioning
// Alipay account already satisfies, not a listing prerequisite.
func TestFundingChannelHelpDisclosesAlipayOnly(t *testing.T) {
	for _, testCase := range []struct {
		path     []string
		required []string
	}{
		{[]string{"commission"}, []string{"Alipay (CNY) only", "receive CNY through Alipay"}},
		{[]string{"commission", "create"}, []string{"bound Alipay account in CNY"}},
		{[]string{"commission", "publish"}, []string{"Alipay (CNY) only"}},
		{[]string{"order"}, []string{"Alipay (CNY) only"}},
		{[]string{"order", "create"}, []string{"Alipay (CNY) only"}},
		{[]string{"order", "payment"}, []string{"Alipay (CNY) is currently the only supported payment channel"}},
		{[]string{"wallet"}, []string{"Alipay (CNY) only", "mainland identity verification"}},
		{[]string{"wallet", "bind"}, []string{"only bindable payout account"}},
		{[]string{"wallet", "withdraw"}, []string{"bound Alipay account only", "mainland identity verification"}},
		{[]string{"wallet", "kyc", "start"}, []string{"only supported payout account", "cannot withdraw earnings"}},
	} {
		command := commandByPath(t, testCase.path...)
		for _, required := range testCase.required {
			if !strings.Contains(command.Long, required) {
				t.Errorf("%q help is missing %q", strings.Join(testCase.path, " "), required)
			}
		}
	}
	if !strings.Contains(fundingChannelNotice, "Alipay (CNY) only") {
		t.Errorf("stderr notice does not state the limit: %q", fundingChannelNotice)
	}
}

func TestDisclosedCommandsWarnOnStderrOnceWithoutChangingStdout(t *testing.T) {
	for _, path := range [][]string{
		{"commission", "create"},
		{"commission", "publish"},
		{"order", "create"},
	} {
		if commandByPath(t, path...).RunE == nil {
			t.Fatalf("%q has no RunE", strings.Join(path, " "))
		}
	}

	tempHome(t)
	runs := 0
	command := discloseFundingChannel(&cobra.Command{
		Use: "disclosed",
		RunE: func(*cobra.Command, []string) error {
			runs++
			return nil
		},
	})

	for attempt, wantNotice := range []bool{true, false} {
		captured, err := os.CreateTemp(t.TempDir(), "stderr")
		if err != nil {
			t.Fatal(err)
		}
		oldStderr := os.Stderr
		os.Stderr = captured
		runErr := command.RunE(command, nil)
		os.Stderr = oldStderr
		if err := captured.Close(); err != nil {
			t.Fatal(err)
		}
		if runErr != nil {
			t.Fatalf("attempt %d failed: %v", attempt, runErr)
		}
		emitted, err := os.ReadFile(captured.Name())
		if err != nil {
			t.Fatal(err)
		}
		if got := strings.Contains(string(emitted), "Notice: "+fundingChannelNotice); got != wantNotice {
			t.Fatalf("attempt %d notice emitted = %v, want %v (stderr %q)", attempt, got, wantNotice, emitted)
		}
	}
	if runs != 2 {
		t.Fatalf("command body ran %d times, want 2", runs)
	}
}

// An unresolvable server or an unusable stamp must disclose rather than hide the
// limit: a silently swallowed notice is worse than one extra line.
func TestFundingNoticeClaimFailsOpen(t *testing.T) {
	tempHome(t)
	now := time.Now().Unix()
	cfg, err := config.Load()
	if err != nil {
		t.Fatal(err)
	}
	server, err := cfg.GetActive("")
	if err != nil {
		t.Fatal(err)
	}

	if !claimFundingNotice("", now) {
		t.Error("an unresolved server name suppressed the notice")
	}
	if !claimFundingNotice(server.Name, 0) {
		t.Error("an unusable clock suppressed the notice")
	}
	if !claimFundingNotice(server.Name, now) {
		t.Fatal("first claim did not disclose")
	}
	if claimFundingNotice(server.Name, now) {
		t.Error("second claim repeated the notice")
	}
	for _, stamp := range []string{"0", "-1", "tomorrow", strconv.FormatInt(now+3600, 10)} {
		cfg, err := config.Load()
		if err != nil {
			t.Fatal(err)
		}
		if err := cfg.SetServerKV(server.Name, fundingNoticeStampKey, stamp); err != nil {
			t.Fatal(err)
		}
		if !claimFundingNotice(server.Name, now) {
			t.Errorf("stamp %q suppressed the notice", stamp)
		}
	}
}
