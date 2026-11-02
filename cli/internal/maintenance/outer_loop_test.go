package maintenance

import (
	"bytes"
	"encoding/json"
	"errors"
	"os"
	"strings"
	"testing"
	"time"
)

func TestOuterLoopWireContract(t *testing.T) {
	b, err := os.ReadFile("../../../contracts/outer_loop_observations.json")
	if err != nil {
		t.Fatal(err)
	}
	var cases []struct {
		Name  string
		Valid bool
		Event json.RawMessage
	}
	if err = json.Unmarshal(b, &cases); err != nil {
		t.Fatal(err)
	}
	for _, tc := range cases {
		t.Run(tc.Name, func(t *testing.T) {
			var event Event
			d := json.NewDecoder(bytes.NewReader(tc.Event))
			d.DisallowUnknownFields()
			err := d.Decode(&event)
			if err == nil {
				err = Validate(event)
			}
			if (err == nil) != tc.Valid {
				t.Fatalf("valid=%t err=%v", tc.Valid, err)
			}
		})
	}
}

func TestRuntimeEnvironmentDetection(t *testing.T) {
	for _, tc := range []struct {
		name, goos, kernel, group, marker, container, wsl string
		wslEnv, unreadable                                bool
	}{
		{name: "plain Linux", goos: "linux", kernel: "6.8.0", group: "0::/", container: "not_detected", wsl: "not_detected"},
		{name: "Docker on WSL", goos: "linux", kernel: "5.15.0-microsoft-standard-WSL2", marker: "/.dockerenv", container: "detected", wsl: "detected"},
		{name: "container cgroup", goos: "linux", kernel: "6.8.0", group: "0::/kubepods/private-container-id", container: "detected", wsl: "not_detected"},
		{name: "WSL environment", goos: "linux", kernel: "6.8.0", wslEnv: true, container: "not_detected", wsl: "detected"},
		{name: "denied probes", goos: "linux", unreadable: true, container: "unknown", wsl: "unknown"},
		{name: "Windows", goos: "windows", container: "unknown", wsl: "not_applicable"},
		{name: "macOS", goos: "darwin", container: "unknown", wsl: "not_applicable"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			read := func(path string) ([]byte, error) {
				if tc.unreadable {
					return nil, os.ErrPermission
				}
				if path == "/proc/self/cgroup" {
					return []byte(tc.group), nil
				}
				return []byte(tc.kernel), nil
			}
			stat := func(path string) (os.FileInfo, error) {
				if tc.unreadable {
					return nil, os.ErrPermission
				}
				if path == tc.marker {
					return nil, nil
				}
				return nil, os.ErrNotExist
			}
			env := detectRuntimeEnvironment(tc.goos, "amd64", read, stat, func(key string) (string, bool) { return "private-distro-name", tc.wslEnv && key == "WSL_DISTRO_NAME" })
			if env.Container != tc.container || env.WSL != tc.wsl {
				t.Fatalf("%+v", env)
			}
			b, _ := json.Marshal(env)
			for _, raw := range []string{"private-distro-name", "private-container-id", "microsoft", "6.8.0"} {
				if strings.Contains(string(b), raw) {
					t.Fatalf("raw probe leaked: %s", b)
				}
			}
		})
	}
}

func TestOuterLoopFailureQueueSurvivesOffline(t *testing.T) {
	s := Scope{Home: t.TempDir(), Server: "test", AgentID: "42"}
	e := NewEvent(NewID(), "watch", "manual", "preflight", "blocked")
	e.ErrorCode = "unsupported_platform"
	e.OuterLoop = &OuterLoopObservation{Environment: RuntimeEnvironment{OS: "linux", Arch: "amd64", Container: "detected", WSL: "unknown"}}
	if err := Record(s, e); err != nil {
		t.Fatal(err)
	}
	if err := Flush(s, time.Now(), func([]Event) error { return errors.New("offline") }); err == nil {
		t.Fatal("expected offline error")
	}
	st, err := Snapshot(s)
	if err != nil || len(st.Events) != 1 || st.Events[0].OuterLoop.Environment.Container != "detected" {
		t.Fatalf("%+v %v", st, err)
	}
}
