package selfupdate

import (
	"context"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"strings"
	"testing"
	"time"
)

func TestPackageValidationHelperProcess(t *testing.T) {
	if os.Getenv("EIGENFLUX_VALIDATION_HELPER") != "1" {
		return
	}
	switch os.Args[len(os.Args)-1] {
	case "version":
		fmt.Fprintln(os.Stdout, "1.0.2")
	case "denied":
		fmt.Fprintln(os.Stderr, "security service unavailable in this sandbox")
		os.Exit(7)
	case "large":
		fmt.Fprint(os.Stderr, strings.Repeat("x", packageDiagnosticLimit*8)+"DISCARDED_TAIL")
		os.Exit(2)
	case "wait":
		time.Sleep(30 * time.Second)
	}
	os.Exit(0)
}

func TestPackageCommandOutputRetainsBoundedLocalDiagnostics(t *testing.T) {
	t.Setenv("EIGENFLUX_VALIDATION_HELPER", "1")
	executable, err := os.Executable()
	if err != nil {
		t.Fatal(err)
	}
	for _, scenario := range []string{"version", "denied", "large", "wait"} {
		t.Run(scenario, func(t *testing.T) {
			ctx := context.Background()
			if scenario == "wait" {
				var cancel context.CancelFunc
				ctx, cancel = context.WithTimeout(ctx, 100*time.Millisecond)
				defer cancel()
			}
			value, err := packageCommandOutput(ctx, executable, "-test.run=^TestPackageValidationHelperProcess$", "--", scenario)
			switch scenario {
			case "version":
				if err != nil || strings.TrimSpace(value) != "1.0.2" {
					t.Fatalf("version output: %q %v", value, err)
				}
			case "denied":
				var exited *exec.ExitError
				if !errors.As(err, &exited) || exited.ExitCode() != 7 || !strings.Contains(err.Error(), "security service unavailable") {
					t.Fatalf("original error lost: %v", err)
				}
				if code := failureCode("verify", atStep("helper_signature", err)); code != "verify_helper_signature_failed" {
					t.Fatal(code)
				}
			case "large":
				if err == nil || len(err.Error()) > packageDiagnosticLimit+100 || strings.Contains(err.Error(), "DISCARDED_TAIL") {
					t.Fatalf("unbounded tool output: %v", err)
				}
			case "wait":
				if !errors.Is(err, context.DeadlineExceeded) {
					t.Fatalf("deadline lost: %v", err)
				}
			}
		})
	}
}
