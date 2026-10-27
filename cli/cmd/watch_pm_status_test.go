package cmd

import (
	"reflect"
	"testing"

	"cli.eigenflux.ai/internal/dispatch"
)

func TestPMReceptionStatusDoesNotGrantSubscription(t *testing.T) {
	for _, host := range []string{"workbuddy", "codex"} {
		for _, subscribed := range []bool{false, true} {
			b := dispatch.Binding{Host: host, Events: []string{"commission_order"}}
			if subscribed {
				b.Events = append(b.Events, "pm_push")
			}
			before := append([]string(nil), b.Events...)
			status := pmReceptionStatus(b)
			if status["subscribed"] != subscribed || status["heartbeat_owns_communication"] != !subscribed {
				t.Fatalf("incorrect reception ownership: %+v", status)
			}
			if status["automatic_reply"] != "requires_fresh_owner_permission" || !reflect.DeepEqual(before, b.Events) {
				t.Fatal("diagnostic changed authorization")
			}
		}
	}
}
