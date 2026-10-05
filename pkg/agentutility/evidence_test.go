package agentutility

import (
	"github.com/stretchr/testify/require"
	"strings"
	"testing"
)

func TestOperationalEvidence(t *testing.T) {
	for _, tt := range []struct{ name, text, want string }{
		{"podman recipe", "Enable and start the Podman socket\nTo enable it, run this command:\nsystemctl --user enable --now podman.socket", WorkflowRecipe},
		{"package recipe", "Install the MCP server: `uvx mcp-server-git --repository .`", WorkflowRecipe},
		{"docker recipe", "docker run --rm agent-worker:1", WorkflowRecipe},
		{"API recipe", "Call the SDK with client.tools.call(\"search\", args).", ToolIntegration},
		{"MCP protocol", "Use the MCP tools/call method to invoke the search tool.", ToolIntegration},
		{"Chinese protocol", "通过工具调用接口配置 SDK，调用 client.tools.call(args)。", ToolIntegration},
		{"release repair", "qwen-code Release v0.25.0\n- fix(permissions): honor approved cross-directory tool calls", CompatibilityFix},
		{"Chinese repair", "本次 MCP SDK 修复了 OAuth 认证超时。", CompatibilityFix},
		{"AI funding", "An AI agent startup raised $50m to transform enterprise workflows.", Unknown},
		{"bare release", "MCP Python SDK v2.0 released today. Read more.", Unknown},
		{"SDK teaser", "A new SDK makes it easy to install and connect AI agents.", Unknown},
		{"tax agents", "Agents can sign up tax clients via GOV.UK.", Unknown},
		{"non software fix", "The agent resolved a transport funding dispute.", Unknown},
		{"unrelated blocks", "MCP news\nCall your representative\nThe article quotes client.foo().", Unknown},
		{"command discussion", "The report mentions npm install and developer tooling without a recipe.", Unknown},
		{"command word", "npm install", Unknown},
		{"empty", "", Unknown},
		{"evidence outside bound", strings.Repeat("x", 32000) + "\nuvx mcp-server-git", Unknown},
	} {
		t.Run(tt.name, func(t *testing.T) {
			got := Classify(tt.text)
			require.Equal(t, Version, got.Version)
			require.Equal(t, tt.want, got.Class)
		})
	}
}
func TestEvidenceLabelRejectsUnknownVersionsAndClasses(t *testing.T) {
	for _, e := range []Evidence{{}, {Version: "future", Class: WorkflowRecipe}, {Version: Version, Class: "arbitrary"}, {Version: Version, Class: Unknown}} {
		require.Equal(t, Unknown, e.Label())
	}
	require.Equal(t, WorkflowRecipe, (Evidence{Version: Version, Class: WorkflowRecipe}).Label())
}
