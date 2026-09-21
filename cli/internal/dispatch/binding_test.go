package dispatch

import (
	"encoding/json"
	"errors"
	"io"
	"os"
	"path/filepath"
	"strings"
	"testing"

	watchstate "cli.eigenflux.ai/internal/watch"
)

func validTestBinding(t *testing.T) Binding {
	t.Helper()
	executable, err := os.Executable()
	if err != nil {
		t.Fatal(err)
	}
	home := t.TempDir()
	return Binding{Version: 1, Home: home, Server: "test", Endpoint: "https://api.example.test", AgentID: "agent", PrincipalID: "principal", Scope: watchstate.Scope(home, "test", "agent", "principal"), Revision: strings.Repeat("ab", 16), Mode: "command", Command: []string{executable}, WorkDir: t.TempDir()}
}

func TestDecodeBindingStrictAndSize(t *testing.T) {
	valid := `{"mode":"command","command":["/agent"],"env":{"MODEL":"test"}}`
	for _, raw := range []string{valid, valid + strings.Repeat(" ", maxBindingBytes-len(valid))} {
		if _, err := DecodeBinding(strings.NewReader(raw)); err != nil {
			t.Fatalf("valid config rejected: %v", err)
		}
	}
	for _, raw := range []string{
		"null", "[]", "{} {}", `{"unexpected":1}`, `{"mode":"native","mode":"command"}`,
		`{"env":{"EIGENFLUX_HOME":"first","EIGENFLUX_HOME":"second"}}`,
		valid + strings.Repeat(" ", maxBindingBytes-len(valid)) + `{}`,
		valid + strings.Repeat(" ", maxBindingBytes+1-len(valid)),
		string([]byte{'{', '"', 'm', 'o', 'd', 'e', '"', ':', '"', 0xff, '"', '}'}),
	} {
		if _, err := DecodeBinding(strings.NewReader(raw)); err == nil {
			t.Fatalf("invalid config accepted: %.150q", raw)
		}
	}
	if _, err := DecodeBinding(io.MultiReader(strings.NewReader(valid), bindingErrorReader{})); err == nil {
		t.Fatal("reader failure discarded")
	}
}

type bindingErrorReader struct{}

func (bindingErrorReader) Read([]byte) (int, error) { return 0, errors.New("read failed") }

func TestBindingValidateIdentityAndDefaults(t *testing.T) {
	b := validTestBinding(t)
	if err := b.Validate(); err != nil {
		t.Fatal(err)
	}
	if b.TimeoutSeconds != 600 || b.Host != "custom" || !filepath.IsAbs(b.Command[0]) || !b.Handles("pm_push") || b.Handles("maintenance_due") {
		t.Fatalf("bad defaults: %+v", b)
	}
	if err := WriteJSON(BindingPath(b.Home, b.Server), b); err != nil {
		t.Fatal(err)
	}
	loaded, err := ReadBinding(b.Home, b.Server)
	if err != nil || loaded.Revision != b.Revision {
		t.Fatalf("read persisted binding: %+v %v", loaded, err)
	}
	revision, err := NewRevision()
	if err != nil || len(revision) != 32 {
		t.Fatalf("bad generated revision: %q %v", revision, err)
	}
	other, err := NewRevision()
	if err != nil || revision == other {
		t.Fatal("revision not unique")
	}
}

func TestBindingValidateRejectsUnsafeConfiguration(t *testing.T) {
	cases := []struct {
		name  string
		alter func(*Binding)
	}{
		{"scope", func(b *Binding) { b.Scope = "different" }},
		{"version", func(b *Binding) { b.Version = 2 }},
		{"relative home", func(b *Binding) {
			b.Home = "relative"
			b.Scope = watchstate.Scope(b.Home, b.Server, b.AgentID, b.PrincipalID)
		}},
		{"empty principal", func(b *Binding) {
			b.PrincipalID = " "
			b.Scope = watchstate.Scope(b.Home, b.Server, b.AgentID, b.PrincipalID)
		}},
		{"NUL identity", func(b *Binding) {
			b.AgentID = "agent\x00other"
			b.Scope = watchstate.Scope(b.Home, b.Server, b.AgentID, b.PrincipalID)
		}},
		{"revision traversal", func(b *Binding) { b.Revision = "../../unexpected" }},
		{"revision length", func(b *Binding) { b.Revision = "ab" }},
		{"revision encoding", func(b *Binding) { b.Revision = strings.Repeat("z", 32) }},
		{"relative endpoint", func(b *Binding) { b.Endpoint = "/api" }},
		{"endpoint credentials", func(b *Binding) { b.Endpoint = "https://user:secret@example.test" }},
		{"endpoint query", func(b *Binding) { b.Endpoint = "https://example.test?token=secret" }},
		{"relative workdir", func(b *Binding) { b.WorkDir = "." }},
		{"missing workdir", func(b *Binding) { b.WorkDir = filepath.Join(b.Home, "missing") }},
		{"relative skillsdir", func(b *Binding) { b.SkillsDir = "skills" }},
		{"negative timeout", func(b *Binding) { b.TimeoutSeconds = -1 }},
		{"oversized timeout", func(b *Binding) { b.TimeoutSeconds = 3601 }},
		{"no command", func(b *Binding) { b.Command = nil }},
		{"blank command", func(b *Binding) { b.Command = []string{""} }},
		{"NUL command", func(b *Binding) { b.Command[0] += "\x00" }},
		{"NUL args", func(b *Binding) { b.Args = []string{"arg\x00"} }},
		{"unknown native adapter", func(b *Binding) { b.Mode = "native"; b.Host = "unverified" }},
		{"missing openclaw agent", func(b *Binding) { b.Mode = "native"; b.Host = "openclaw" }},
		{"option host agent", func(b *Binding) { b.HostAgent = "--deliver" }},
		{"NUL host agent", func(b *Binding) { b.HostAgent = "agent\x00" }},
		{"unsupported event", func(b *Binding) { b.Events = []string{"commission"} }},
		{"duplicate events", func(b *Binding) { b.Events = []string{"pm_push", "pm_push"} }},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			b := validTestBinding(t)
			tc.alter(&b)
			if err := b.Validate(); err == nil {
				t.Fatal("invalid configuration accepted")
			}
		})
	}
}

func TestBindingRoutingEnvironment(t *testing.T) {
	for _, key := range []string{"EIGENFLUX_HOME", "eigenflux_server", "EigenFlux_Token", "EIGENFLUX_SKILLS_DIR", "", "A=B", "A\x00B"} {
		t.Run(key, func(t *testing.T) {
			b := validTestBinding(t)
			b.Env = map[string]string{key: "value"}
			if err := b.Validate(); err == nil {
				t.Fatalf("unsafe env key accepted: %q", key)
			}
		})
	}
	b := validTestBinding(t)
	b.Env = map[string]string{"MODEL": "unsafe\x00value"}
	if err := b.Validate(); err == nil {
		t.Fatal("NUL env value accepted")
	}
	b = validTestBinding(t)
	b.Env = map[string]string{"MODEL": "chosen-model", "CUSTOM_TOKEN": "local-token"}
	if err := b.Validate(); err != nil {
		t.Fatalf("non-routing env rejected: %v", err)
	}
}

func TestReadBindingRejectsCorruptIdentity(t *testing.T) {
	b := validTestBinding(t)
	path := BindingPath(b.Home, b.Server)
	for _, alter := range []func(*Binding){
		func(b *Binding) { b.Revision = "../escape" },
		func(b *Binding) {
			b.Home = t.TempDir()
			b.Scope = watchstate.Scope(b.Home, b.Server, b.AgentID, b.PrincipalID)
		},
		func(b *Binding) {
			b.Server = "other"
			b.Scope = watchstate.Scope(b.Home, b.Server, b.AgentID, b.PrincipalID)
		},
		func(b *Binding) { b.Scope = "different" },
	} {
		corrupted := b
		alter(&corrupted)
		if err := WriteJSON(path, corrupted); err != nil {
			t.Fatal(err)
		}
		if _, err := ReadBinding(b.Home, b.Server); err == nil {
			t.Fatal("corrupt persisted identity accepted")
		}
	}
	raw, _ := json.Marshal(b)
	if err := os.WriteFile(path, append(raw, []byte(strings.Repeat(" ", maxBindingBytes))...), 0600); err != nil {
		t.Fatal(err)
	}
	if _, err := ReadBinding(b.Home, b.Server); err == nil {
		t.Fatal("oversized persisted binding accepted")
	}
}
