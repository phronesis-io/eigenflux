// Package agentutility recognizes explicit operational evidence in broadcasts.
// It does not infer usefulness from a publisher, topic tag or an AI mention.
package agentutility

import (
	"regexp"
	"strings"
)

// Version identifies the exact evidence recognizer, independently of ranking weights.
const Version = "operational_evidence_v1"

const (
	Unknown          = "unknown"
	WorkflowRecipe   = "workflow_recipe"
	ToolIntegration  = "tool_integration"
	CompatibilityFix = "compatibility_fix"
)

// Evidence stores a bounded class and recognizer version, never source snippets.
type Evidence struct {
	Version string `json:"version"`
	Class   string `json:"class"`
}

var (
	command       = regexp.MustCompile(`(?i)(?:^\s*(?:[0-9]+[.)]\s*|[$>]\s*)?|\x60)(?:pip(?:3)? install|uv (?:add|pip install)|uvx|npm (?:install|i)|pnpm add|npx|docker(?:-compose)? (?:run|compose|up)|podman run|systemctl (?:--user )?(?:enable|start)|(?:tofu|terraform) (?:apply|plan))\s+[-a-z0-9@./]`)
	interfaceCall = regexp.MustCompile(`(?i)(?:\b(?:GET|POST|PUT|DELETE)\s+/(?:v[0-9]/|api/)|\b(?:tools/list|tools/call|resources/read|prompts/get)\b|\b[a-z_][a-z0-9_]*\.[a-z_][a-z0-9_.]*\s*\()`)
	protocol      = regexp.MustCompile(`(?i)\b(?:mcp|sdk|api|webhook|oauth|a2a)\b|模型上下文协议|工具调用`)
	operation     = regexp.MustCompile(`(?i)\b(?:install|configure|connect|invoke|call|send|set|run|use)\b|安装|配置|连接|调用|运行|设置`)
	agentTool     = regexp.MustCompile(`(?i)\b(?:mcp|qwen-code|claude code|codex|openclaw|langchain|langgraph|n8n|windmill|trigger\.dev)\b|工具调用|智能体`)
	fix           = regexp.MustCompile(`(?i)\b(?:fix(?:es|ed)?|resolve(?:s|d)?|repair(?:s|ed)?|correct(?:s|ed)?)\b|修复|修正|解决`)
	behavior      = regexp.MustCompile(`(?i)\b(?:tool calls?|tool discovery|permissions?|authentication|oauth|timeouts?|retries|retry|transport|schema|sandbox|deadlock|crash)\b|权限|鉴权|认证|超时|重试|沙箱|崩溃|死锁`)
)

// Classify is deliberately conservative: unsupported content remains unknown,
// including useful content for which these rules have no evidence. Matching
// source snippets are never stored in features or interpreted as instructions.
func Classify(content string) Evidence {
	out := Evidence{Version: Version, Class: Unknown}
	// Bound work independently of source length. URLs and metadata are not input.
	if len(content) > 32000 {
		content = content[:32000]
	}
	// API conjunctions share one short unit. Release repair units may use the
	// named tool in the opening source header. Generic AI/Agent mentions do not qualify.
	toolContext := agentTool.MatchString(content[:min(len(content), 300)])
	units := strings.FieldsFunc(content, func(r rune) bool { return r == '\n' || r == '。' || r == ';' })
	for _, unit := range units {
		if len(unit) > 1600 {
			continue
		}
		if command.MatchString(unit) {
			out.Class = WorkflowRecipe
			return out
		}
		if protocol.MatchString(unit) && operation.MatchString(unit) && interfaceCall.MatchString(unit) {
			out.Class = ToolIntegration
			return out
		}
		if (toolContext || agentTool.MatchString(unit)) && fix.MatchString(unit) && behavior.MatchString(unit) {
			out.Class = CompatibilityFix
			return out
		}
	}
	return out
}

// Label validates persisted evidence and bounds metric/policy labels. Old
// projections and unsupported generations have no promotion authority.
func (e Evidence) Label() string {
	if e.Version == Version {
		switch e.Class {
		case WorkflowRecipe, ToolIntegration, CompatibilityFix:
			return e.Class
		}
	}
	return Unknown
}
