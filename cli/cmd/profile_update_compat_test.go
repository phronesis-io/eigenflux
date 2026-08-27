package cmd

import (
	"encoding/json"
	"errors"
	"strings"
	"testing"

	"cli.eigenflux.ai/internal/client"
)

type profileFieldsClientStub struct {
	getPaths []string
	putPaths []string
	putBody  map[string]interface{}
	putErr   error
}

func (stub *profileFieldsClientStub) Get(path string, _ map[string]string) (*client.APIResponse, error) {
	stub.getPaths = append(stub.getPaths, path)
	return &client.APIResponse{Code: 0, Data: json.RawMessage(`{"profile_version":17}`)}, nil
}

func (stub *profileFieldsClientStub) Put(path string, body interface{}) (*client.APIResponse, error) {
	stub.putPaths = append(stub.putPaths, path)
	stub.putBody, _ = body.(map[string]interface{})
	if stub.putErr != nil {
		return nil, stub.putErr
	}
	return &client.APIResponse{Code: 0, Data: json.RawMessage(`{"profile_version":18,"changed_paths":["agent_name","agent_description"]}`)}, nil
}

func TestProfileUpdateCompatibilityUsesVersionedFieldWriter(t *testing.T) {
	stub := &profileFieldsClientStub{}
	data, err := updateProfileThroughFields(stub, legacyProfileUpdateRoutes, "Atlas", "Research assistant", "plugin_memory", "facts changed")
	if err != nil {
		t.Fatal(err)
	}
	if len(stub.getPaths) != 1 || stub.getPaths[0] != "/agents/me/card/refresh-context" {
		t.Fatalf("unexpected refresh request paths: %#v", stub.getPaths)
	}
	if len(stub.putPaths) != 1 || stub.putPaths[0] != "/agents/me/profile/fields" {
		t.Fatalf("profile update did not use the field writer: %#v", stub.putPaths)
	}
	if stub.putBody["expected_version"] != int64(17) || stub.putBody["source"] != "plugin_memory" || stub.putBody["reason"] != "facts changed" {
		t.Fatalf("unexpected field update envelope: %#v", stub.putBody)
	}
	updates, ok := stub.putBody["updates"].(map[string]interface{})
	if !ok || updates["agent_name"] != "Atlas" || updates["agent_description"] != "Research assistant" {
		t.Fatalf("legacy flags were not mapped to V2 profile fields: %#v", stub.putBody["updates"])
	}
	if strings.Contains(string(data), "agents/profile") {
		t.Fatalf("response unexpectedly references the legacy writer: %s", data)
	}
}

func TestProfileUpdateCompatibilityUsesStableDefaultSource(t *testing.T) {
	stub := &profileFieldsClientStub{}
	if _, err := updateProfileThroughFields(stub, legacyProfileUpdateRoutes, "Atlas", "", "", ""); err != nil {
		t.Fatal(err)
	}
	if stub.putBody["source"] != "cli_profile_update_compat" {
		t.Fatalf("unexpected default source: %#v", stub.putBody["source"])
	}
}

func TestProfileUpdateCompatibilityExplainsVersionConflict(t *testing.T) {
	stub := &profileFieldsClientStub{putErr: &client.APIError{StatusCode: 409, Msg: "conflict"}}
	_, err := updateProfileThroughFields(stub, legacyProfileUpdateRoutes, "Atlas", "", "", "")
	if err == nil || !strings.Contains(err.Error(), "retry the same profile update") {
		t.Fatalf("unexpected conflict error: %v", err)
	}
	var apiErr *client.APIError
	if errors.As(err, &apiErr) {
		t.Fatal("compatibility error should be actionable instead of exposing the raw API error")
	}
}

func TestProfileUpdateV2RoutesUseAgentCredentialNamespace(t *testing.T) {
	stub := &profileFieldsClientStub{}
	if _, err := updateProfileThroughFields(stub, v2ProfileUpdateRoutes, "Atlas", "", "", ""); err != nil {
		t.Fatal(err)
	}
	if len(stub.getPaths) != 1 || stub.getPaths[0] != "/agent-profile/refresh-context" {
		t.Fatalf("unexpected V2 refresh path: %#v", stub.getPaths)
	}
	if len(stub.putPaths) != 1 || stub.putPaths[0] != "/agent-profile/fields" {
		t.Fatalf("unexpected V2 field path: %#v", stub.putPaths)
	}
}
