package dispatch

import (
	"context"
	"reflect"
	"testing"
)

func TestCommissionModelRouting(t *testing.T) {
	r := Runner{Binding: Binding{Mode: "native", Host: "codex"}}
	for _, tc := range []struct{ kind, skill, effort string }{
		{"commission_publish_trial", "gpt-sol-light-commission", "low"},
		{"commission_fulfillment", "gpt-sol-ultra-commission", "ultra"},
	} {
		args, _, err := r.arguments(Request{Kind: tc.kind, CommissionSkill: tc.skill})
		want := []string{"exec", "--json", "--model", "gpt-6.1-sol", "--config", "model_reasoning_effort=\"" + tc.effort + "\"", "-"}
		if err != nil || !reflect.DeepEqual(args, want) {
			t.Fatalf("got %v, %v; want %v", args, err, want)
		}
	}
	args, _, err := r.arguments(Request{Kind: "pm_push", CommissionSkill: "gpt-sol-light-commission"})
	if err != nil || !reflect.DeepEqual(args, []string{"exec", "--json", "-"}) {
		t.Fatalf("unrelated request changed: %v %v", args, err)
	}
	r.Binding.Host = "hermes"
	if _, _, err := r.arguments(Request{Kind: "commission_fulfillment", CommissionSkill: "gpt-sol-light-commission"}); err == nil {
		t.Fatal("unsupported host must fail closed")
	}
	r.Binding.Mode = "acp"
	if _, err := r.Run(context.Background(), Request{Kind: "commission_fulfillment", CommissionSkill: "gpt-sol-light-commission"}); err == nil || err.Error() != "commission_model_routing_requires_codex" {
		t.Fatalf("ACP must fail before execution: %v", err)
	}
}
