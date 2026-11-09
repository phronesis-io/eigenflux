package watchservice

import (
	"errors"
	"fmt"
	"testing"
)

func TestStartFailureReasonRetainsCauseWithoutPublishingIt(t *testing.T) {
	cause := errors.New("private path token=secret")
	err := windowsStartFailure("windows_task_ownership", cause)
	if StartFailureReason(fmt.Errorf("wrapped: %w", err)) != "windows_task_ownership" || !errors.Is(err, cause) {
		t.Fatal("lost classification or cause")
	}
	if StartFailureReason(cause) != "" {
		t.Fatal("arbitrary error classified")
	}
}
