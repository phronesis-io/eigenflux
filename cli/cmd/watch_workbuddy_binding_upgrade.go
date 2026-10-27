package cmd

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"os"
	"reflect"
	"runtime"
	"slices"
	"time"

	"cli.eigenflux.ai/internal/auth"
	"cli.eigenflux.ai/internal/dispatch"
	"cli.eigenflux.ai/internal/skills"
)

// Recognize only the original macOS Commission adapter, not arbitrary owner
// event selections. Modern bindings and explicit custom adapters are preserved.
func legacyWorkBuddyPMBinding(b dispatch.Binding, platform string) bool {
	if platform != "darwin" || b.Host != "workbuddy" || b.Mode != "acp" ||
		!slices.Equal(b.Events, []string{"commission_order"}) || b.TimeoutSeconds != 300 || len(b.Command) < 3 ||
		b.Command[1] != "watch" || b.Command[2] != "workbuddy-acp" {
		return false
	}
	var tools []string
	for i := 3; i+1 < len(b.Command); i++ {
		if b.Command[i] == "--tool" {
			tools = append(tools, b.Command[i+1])
			i++
		}
	}
	return slices.Equal(tools, []string{"Read", "Write", "Edit"})
}

// Called only during watch startup, while its account ownership lock is held.
// Keep the journal revision and adapter capabilities; never change auto_reply_pm.
func (w *accountWatch) upgradeLegacyWorkBuddyPM(b dispatch.Binding) (dispatch.Binding, error) {
	if !legacyWorkBuddyPMBinding(b, runtime.GOOS) {
		return b, nil
	}
	return upgradeLegacyWorkBuddyPMBinding(b, func() error {
		current, err := watchBindingIdentity()
		if err != nil {
			return err
		}
		if !sameBindingIdentity(b, current) {
			return errWatchIdentity
		}
		return nil
	})
}

func upgradeLegacyWorkBuddyPMBinding(b dispatch.Binding, identityOK func() error) (dispatch.Binding, error) {
	if _, err := skills.ReadSignedFile(b.SkillsDir, "ef-communication", "references/dispatch.md", 64<<10); err != nil {
		return b, errors.New("legacy WorkBuddy PM migration requires signed Communication dispatch rules")
	}
	path := dispatch.BindingPath(b.Home, b.Server)
	before, err := os.ReadFile(path)
	if err != nil {
		return b, err
	}
	var saved dispatch.Binding
	if err := json.Unmarshal(before, &saved); err != nil || !reflect.DeepEqual(saved, b) {
		return b, errors.New("binding changed before WorkBuddy PM migration; retry startup")
	}
	updated := b
	updated.Events = []string{"pm_push", "commission_order"}
	err = auth.WithV2CredentialsLockContext(context.Background(), b.Server, 35*time.Second, func() error {
		if err := identityOK(); err != nil {
			return err
		}
		current, err := os.ReadFile(path)
		if err != nil {
			return err
		}
		if !bytes.Equal(before, current) {
			return errors.New("binding changed during WorkBuddy PM migration; retry startup")
		}
		return dispatch.WriteJSON(path, updated)
	})
	if err != nil {
		return b, err
	}
	return updated, nil
}
