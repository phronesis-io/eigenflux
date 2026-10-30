package uninstallscript

import (
	"encoding/json"
	"os"
	"os/exec"
	"strings"
	"testing"
)

func TestTemporaryReporterEnvironmentNative(t *testing.T) {
	keys := []string{"EIGENFLUX_INSTALL_ROOT", "EIGENFLUX_INSTALL_CHILD", "EIGENFLUX_INSTALL_WATCH_LAUNCHER"}
	if os.Getenv("EF_UNINSTALL_REPORTER_FIXTURE") == "1" {
		result := map[string]string{}
		for _, key := range keys {
			result[key] = os.Getenv(key)
		}
		_ = json.NewEncoder(os.Stdout).Encode(result)
		os.Exit(0)
	}
	path, err := os.Executable()
	if err != nil {
		t.Fatal(err)
	}
	fixture := "'" + strings.ReplaceAll(path, "'", "''") + "'"
	script := strings.SplitN(string(Windows), "$resolved =", 2)[0]
	// Retain the process helper, omitting the wrapper's mandatory CLI parameters.
	script = script[strings.Index(script, "$ErrorActionPreference"):]
	script += "\n$normal = Invoke-EigenFluxJson " + fixture + " @('-test.run=^TestTemporaryReporterEnvironmentNative$')\n"
	script += "$copied = Invoke-EigenFluxJson " + fixture + " @('-test.run=^TestTemporaryReporterEnvironmentNative$') -TemporaryReporter\n"
	script += "@{ normal = ($normal.Output | ConvertFrom-Json); copied = ($copied.Output | ConvertFrom-Json) } | ConvertTo-Json -Depth 5\n"
	command := exec.Command("powershell.exe", "-NoProfile", "-NonInteractive", "-Command", script)
	command.Env = append(os.Environ(), "EF_UNINSTALL_REPORTER_FIXTURE=1")
	for _, key := range keys {
		command.Env = append(command.Env, key+"=preserved")
	}
	output, err := command.CombinedOutput()
	if err != nil {
		t.Fatalf("reporter environment: %s %v", output, err)
	}
	var result struct{ Normal, Copied map[string]string }
	if err := json.Unmarshal(output, &result); err != nil {
		t.Fatalf("reporter result: %s %v", output, err)
	}
	for _, key := range keys {
		if result.Normal[key] != "preserved" || result.Copied[key] != "" {
			t.Fatalf("%s normal=%q copied=%q", key, result.Normal[key], result.Copied[key])
		}
	}
}
