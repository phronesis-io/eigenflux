package selfupdate

import (
	"context"
	"fmt"
	"os/exec"
	"strings"
)

const packageDiagnosticLimit = 2048

// Keep tool diagnostics local and bounded even if a tool writes large output.
type packageCommandBuffer struct{ data []byte }

func (b *packageCommandBuffer) Write(p []byte) (int, error) {
	if remaining := packageDiagnosticLimit - len(b.data); remaining > 0 {
		b.data = append(b.data, p[:min(len(p), remaining)]...)
	}
	return len(p), nil
}

func packageCommandOutput(ctx context.Context, executable string, args ...string) (string, error) {
	command := exec.CommandContext(ctx, executable, args...)
	var stdout, stderr packageCommandBuffer
	command.Stdout, command.Stderr = &stdout, &stderr
	if err := command.Run(); err != nil {
		if ctx.Err() != nil {
			err = ctx.Err()
		}
		if detail := strings.TrimSpace(string(stderr.data)); detail != "" {
			return "", fmt.Errorf("%w: %s", err, detail)
		}
		return "", err
	}
	return string(stdout.data), nil
}
