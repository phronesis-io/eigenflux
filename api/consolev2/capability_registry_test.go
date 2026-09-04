package consolev2

import (
	"context"
	"net/http"
	"testing"

	"github.com/cloudwego/hertz/pkg/app"

	"eigenflux_server/pkg/agentcard"
)

func TestAgentCapabilityRegistryIsBilingualAndStable(t *testing.T) {
	registry := buildAgentCapabilityRegistry("en", true, true)
	operations, ok := registry["operations"].([]capabilityOperation)
	if !ok || len(operations) < 50 {
		t.Fatalf("operations = %T/%d, want a populated registry", registry["operations"], len(operations))
	}
	seen := make(map[string]bool, len(operations))
	for _, operation := range operations {
		if operation.OperationID == "" || operation.CLI == "" || seen[operation.OperationID] {
			t.Fatalf("invalid or duplicate operation: %#v", operation)
		}
		seen[operation.OperationID] = true
		for _, language := range []string{"zh-CN", "en"} {
			text := operation.Localized[language]
			if text.Label == "" || text.Description == "" {
				t.Fatalf("operation %q has incomplete %s text", operation.OperationID, language)
			}
		}
	}
	for _, required := range []string{
		"identity.switch_account", "identity.recover_account", "profile.update", "context.goal.update", "context.intent.update",
		"context.security.update", "attention.respond", "message.send", "relation.request", "settings.language.update",
	} {
		if !seen[required] {
			t.Fatalf("registry missing %q", required)
		}
	}
}

func TestAgentCapabilityRegistryIncludesEveryEditableAgentCardField(t *testing.T) {
	registry := buildAgentCapabilityRegistry("zh-CN", true, true)
	fields, ok := registry["editable_profile_fields"].([]capabilityField)
	if !ok || len(fields) != len(agentcard.EditableFields) {
		t.Fatalf("editable fields = %T/%d, want %d", registry["editable_profile_fields"], len(fields), len(agentcard.EditableFields))
	}
	seen := make(map[string]bool, len(fields))
	for _, field := range fields {
		seen[field.Key] = true
		if field.Localized["zh-CN"].Label == "" || field.Localized["en"].Label == "" {
			t.Fatalf("field %q lacks bilingual text", field.Key)
		}
	}
	for _, spec := range agentcard.EditableFields {
		if !seen[spec.Name] {
			t.Fatalf("registry missing editable field %q", spec.Name)
		}
	}
	for _, required := range []string{"current_focus", "demands", "agent_status", "human_status"} {
		if !seen[required] {
			t.Fatalf("registry missing non-Console-summary Agent Card field %q", required)
		}
	}
}

func TestAgentCapabilityRegistryFallsBackToChinese(t *testing.T) {
	registry := buildAgentCapabilityRegistry("fr", true, true)
	if registry["language"] != "zh-CN" {
		t.Fatalf("language = %v, want zh-CN", registry["language"])
	}
}

func TestAgentCapabilityRegistryReflectsFeatureFlags(t *testing.T) {
	registry := buildAgentCapabilityRegistry("en", false, false)
	operations := registry["operations"].([]capabilityOperation)
	for _, operation := range operations {
		if (operation.Category == "runtime" || operation.Category == "attention") && operation.Availability != "disabled_by_server" {
			t.Fatalf("disabled %s operation %q reports %q", operation.Category, operation.OperationID, operation.Availability)
		}
		if operation.OperationID == "capabilities.read" && operation.MinCLIVersion != "0.0.38" {
			t.Fatalf("capabilities min CLI = %q", operation.MinCLIVersion)
		}
		if operation.OperationID == "settings.language.update" && len(operation.AllowedValues) != 2 {
			t.Fatalf("language allowed values = %#v", operation.AllowedValues)
		}
	}
}

func TestAgentCapabilityRegistrySeparatesIdentityRoutes(t *testing.T) {
	registry := buildAgentCapabilityRegistry("zh-CN", true, true)
	operations := registry["operations"].([]capabilityOperation)
	byID := make(map[string]capabilityOperation, len(operations))
	for _, operation := range operations {
		byID[operation.OperationID] = operation
	}

	profile := byID["profile.update"]
	if profile.IdentityRoute != "current_identity" || profile.RequiresConsoleHandoff {
		t.Fatalf("profile.update identity route = %#v", profile)
	}
	for _, operationID := range []string{"context.goal.update", "context.intent.update", "context.security.update", "settings.language.update"} {
		operation := byID[operationID]
		if operation.IdentityRoute != "current_identity" || operation.RequiresConsoleHandoff {
			t.Fatalf("%s identity route = %#v", operationID, operation)
		}
	}
	legacyProfile := byID["profile.legacy_update"]
	if legacyProfile.IdentityRoute != "current_identity" || legacyProfile.Availability != "completed" || legacyProfile.RequiresConsoleHandoff {
		t.Fatalf("profile.legacy_update compatibility route = %#v", legacyProfile)
	}
	for _, operationID := range []string{"identity.legacy_login", "identity.legacy_verify", "identity.logout"} {
		operation := byID[operationID]
		if operation.IdentityRoute != "legacy_only" || operation.Availability != "legacy_only" || operation.RequiresConsoleHandoff {
			t.Fatalf("%s legacy route = %#v", operationID, operation)
		}
	}
	provision := byID["identity.provision"]
	if provision.IdentityRoute != "provision" || !provision.RequiresConsoleHandoff {
		t.Fatalf("identity.provision route = %#v", provision)
	}
	recovery := byID["identity.recover_account"]
	if recovery.IdentityRoute != "recover_account" || !recovery.RequiresConsoleHandoff || recovery.MinCLIVersion != "0.0.39" {
		t.Fatalf("identity.recover_account route = %#v", recovery)
	}
	switchAccount := byID["identity.switch_account"]
	if switchAccount.IdentityRoute != "switch_account" || !switchAccount.RequiresConsoleHandoff ||
		switchAccount.SameAccountBehavior != "confirm_without_change" {
		t.Fatalf("identity.switch_account route = %#v", switchAccount)
	}
}

func TestAgentCapabilityRegistrySupportsETagRevalidation(t *testing.T) {
	service := &Service{enableControl: true, enableAttentionV1: true}
	first := app.NewContext(0)
	first.Request.SetRequestURI("/api/v2/agent-capabilities?lang=en")
	service.getAgentCapabilities(context.Background(), first)
	if first.Response.StatusCode() != http.StatusOK || len(first.Response.Body()) == 0 {
		t.Fatalf("first response = %d %s", first.Response.StatusCode(), first.Response.Body())
	}
	etag := string(first.Response.Header.Peek("ETag"))
	if etag == "" {
		t.Fatal("capability response has no ETag")
	}

	second := app.NewContext(0)
	second.Request.SetRequestURI("/api/v2/agent-capabilities?lang=en")
	second.Request.Header.Set("If-None-Match", etag)
	service.getAgentCapabilities(context.Background(), second)
	if second.Response.StatusCode() != http.StatusNotModified || len(second.Response.Body()) != 0 {
		t.Fatalf("revalidation response = %d %s", second.Response.StatusCode(), second.Response.Body())
	}
}

func TestAgentAttentionDismissRequiresRevisionButConsoleRemainsCompatible(t *testing.T) {
	agentRequest := app.NewContext(0)
	agentRequest.Set("agent_credential_session_id", int64(9))
	if !attentionDismissRevisionRequired(agentRequest) {
		t.Fatal("Agent dismissal must require a revision")
	}
	consoleRequest := app.NewContext(0)
	if attentionDismissRevisionRequired(consoleRequest) {
		t.Fatal("Console dismissal unexpectedly required a revision")
	}
}
