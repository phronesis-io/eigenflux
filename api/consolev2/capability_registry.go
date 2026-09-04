package consolev2

import (
	"context"
	"crypto/sha256"
	"encoding/json"
	"fmt"
	"net/http"
	"sort"
	"strings"

	"github.com/cloudwego/hertz/pkg/app"

	"eigenflux_server/pkg/agentcard"
)

const agentCapabilitySchemaVersion = 1

type capabilityText struct {
	Label         string   `json:"label"`
	Description   string   `json:"description"`
	SemanticHints []string `json:"semantic_hints,omitempty"`
}

type capabilityOperation struct {
	OperationID            string                    `json:"operation_id"`
	CLI                    string                    `json:"cli"`
	Category               string                    `json:"category"`
	Access                 string                    `json:"access"`
	Risk                   string                    `json:"risk"`
	Confirmation           string                    `json:"confirmation"`
	Availability           string                    `json:"availability"`
	MinCLIVersion          string                    `json:"min_cli_version"`
	IdentityRoute          string                    `json:"identity_route"`
	RequiresConsoleHandoff bool                      `json:"requires_console_handoff"`
	SameAccountBehavior    string                    `json:"same_account_behavior,omitempty"`
	Localized              map[string]capabilityText `json:"localized"`
	Label                  string                    `json:"label"`
	Description            string                    `json:"description"`
	SemanticHints          []string                  `json:"semantic_hints,omitempty"`
	AllowedValues          []string                  `json:"allowed_values,omitempty"`
}

type capabilityField struct {
	Key           string                    `json:"key"`
	Kind          string                    `json:"kind"`
	Public        bool                      `json:"public"`
	MaxCharacters int                       `json:"max_characters,omitempty"`
	MaxItems      int                       `json:"max_items,omitempty"`
	Localized     map[string]capabilityText `json:"localized"`
	Label         string                    `json:"label"`
	Description   string                    `json:"description"`
}

type capabilitySeed struct {
	id, cli, category, access, risk, confirmation, availability, minCLI string
	identityRoute, sameAccountBehavior                                  string
	requiresConsoleHandoff                                              bool
	zh, en                                                              capabilityText
}

func capability(id, cli, category, access, zhLabel, enLabel string) capabilitySeed {
	confirmation := "explicit_user_instruction"
	if access == "read" {
		confirmation = "none"
	}
	return capabilitySeed{
		id: id, cli: cli, category: category, access: access,
		risk: "normal", confirmation: confirmation, availability: "completed", minCLI: "0.0.37", identityRoute: "none",
		zh: capabilityText{Label: zhLabel, Description: zhLabel},
		en: capabilityText{Label: enLabel, Description: enLabel},
	}
}

func capabilitySeeds() []capabilitySeed {
	seeds := []capabilitySeed{
		capability("capabilities.read", "eigenflux capabilities", "discovery", "read", "读取 Agent 能力注册表", "Read the Agent capability registry"),
		capability("identity.initialize", "eigenflux agent init", "identity", "write", "初始化本地 Agent 身份", "Initialize local Agent identity"),
		capability("identity.provision", "eigenflux agent provision", "identity", "write", "创建或认领 Agent", "Provision or claim an Agent"),
		capability("identity.recover_account", "eigenflux agent provision --recover-account", "identity", "write", "恢复历史 Agent", "Recover a historical Agent"),
		capability("identity.switch_account", "eigenflux agent switch-account", "identity", "write", "切换 CLI 登录账号", "Switch the CLI account"),
		capability("identity.legacy_login", "eigenflux auth login", "identity", "write", "旧版邮箱登录", "Legacy email login"),
		capability("identity.legacy_verify", "eigenflux auth verify", "identity", "write", "验证旧版邮箱登录", "Verify legacy email login"),
		capability("identity.logout", "eigenflux auth logout", "identity", "write", "退出旧版登录", "Log out of legacy authentication"),
		capability("profile.read", "eigenflux profile show", "profile", "read", "查看 Agent 资料", "View Agent profile"),
		capability("profile.update", "eigenflux profile patch", "profile", "write", "修改 Agent Card", "Update Agent Card"),
		capability("profile.legacy_update", "eigenflux profile update", "profile", "write", "兼容更新 Agent 资料", "Update the Agent profile through the compatibility command"),
		capability("profile.items", "eigenflux profile items", "profile", "read", "查看已发布内容", "List published items"),
		capability("profile.card.read", "eigenflux profile card show", "profile", "read", "查看公开 Agent Card", "View the public Agent Card"),
		capability("profile.refresh", "eigenflux profile refresh-context", "profile", "read", "读取资料刷新上下文", "Read profile refresh context"),
		capability("profile.refresh.complete", "eigenflux profile refresh-complete", "profile", "write", "完成资料刷新", "Complete a profile refresh"),
		capability("profile.refresh.status", "eigenflux profile refresh-status", "profile", "read", "查看资料刷新状态", "View profile refresh status"),
		capability("profile.refresh.prompt", "eigenflux profile refresh-prompt", "profile", "write", "记录资料刷新提醒", "Record a profile refresh prompt"),
		capability("profile.status.prompt", "eigenflux profile status-prompt", "profile", "write", "记录近期状态提醒", "Record a recent-status prompt"),
		capability("context.read", "eigenflux context pull", "context", "read", "读取控制上下文", "Read control context"),
		capability("context.goal.update", "eigenflux context goal set", "context", "write", "修改网络活动目标", "Update network goal"),
		capability("context.intent.list", "eigenflux context intent list", "context", "read", "查看意图与行动", "List intents and actions"),
		capability("context.intent.create", "eigenflux context intent add", "context", "write", "新增意图与行动", "Add an intent and action"),
		capability("context.intent.update", "eigenflux context intent update", "context", "write", "修改意图与行动", "Update an intent and action"),
		capability("context.intent.delete", "eigenflux context intent delete", "context", "write", "删除意图与行动", "Delete an intent and action"),
		capability("context.security.update", "eigenflux context security set", "context", "write", "修改安全边界", "Update security boundaries"),
		capability("settings.sync", "eigenflux settings sync", "settings", "read_write", "同步 Agent 设置", "Synchronize Agent settings"),
		capability("settings.recurring_publish.update", "eigenflux context security set --recurring-publish", "settings", "write", "修改自动广播权限", "Update automatic broadcast permission"),
		capability("settings.auto_reply_pm.update", "eigenflux context security set --auto-reply-pm", "settings", "write", "修改自动私信回复权限", "Update automatic direct-message reply permission"),
		capability("settings.auto_comment.update", "eigenflux context security set --auto-comment", "settings", "write", "修改自动评论权限", "Update automatic comment permission"),
		capability("settings.show_add_friend.update", "eigenflux context security set --show-add-friend", "settings", "write", "修改添加好友入口", "Update add-friend visibility"),
		capability("settings.feed_poll_interval.update", "eigenflux config set --key feed_poll_interval", "settings", "write", "修改 Feed 拉取间隔", "Update the Feed polling interval"),
		capability("settings.official_pm_optout.update", "eigenflux config set --key official_pm_optout", "settings", "write", "修改官方私信退出设置", "Update official-message opt-out"),
		capability("settings.feed_delivery_preference.update", "eigenflux config set --key feed_delivery_preference", "settings", "write", "修改 Feed 投递偏好", "Update the Feed delivery preference"),
		capability("settings.language.update", "eigenflux config set --key lang", "settings", "write", "修改账号语言偏好", "Update the account language preference"),
		capability("settings.report", "eigenflux settings push", "settings", "write", "上报运行环境设置", "Report runtime settings"),
		capability("config.read", "eigenflux config show", "local", "read", "查看本地配置", "View local configuration"),
		capability("config.value.read", "eigenflux config get", "local", "read", "读取本地配置项", "Read a local configuration value"),
		capability("config.value.update", "eigenflux config set", "local", "write", "修改本地配置项", "Update a local configuration value"),
		capability("feed.read", "eigenflux feed poll", "feed", "read", "获取 Feed", "Pull the Feed"),
		capability("feed.item.read", "eigenflux feed get", "feed", "read", "查看 Feed 条目", "View a Feed item"),
		capability("feed.feedback", "eigenflux feed feedback", "feed", "write", "提交 Feed 反馈", "Submit Feed feedback"),
		capability("feed.event.record", "eigenflux feed event record", "feed", "write", "记录 Feed 行为事件", "Record a Feed interaction event"),
		capability("feed.event.push", "eigenflux feed event push", "feed", "write", "立即提交 Feed 行为事件", "Push Feed interaction events immediately"),
		capability("feed.event.flush", "eigenflux feed event flush", "feed", "write", "提交缓存的 Feed 行为事件", "Flush buffered Feed interaction events"),
		capability("broadcast.publish", "eigenflux publish", "broadcast", "write", "发布广播", "Publish a broadcast"),
		capability("broadcast.delete", "eigenflux feed delete", "broadcast", "write", "撤回广播", "Retract a broadcast"),
		capability("attention.publish", "eigenflux attention publish", "attention", "write", "发布 Attention", "Publish Attention"),
		capability("attention.prefill", "eigenflux attention prefill", "attention", "write", "上传 onboarding Attention 预填", "Upload onboarding Attention prefill"),
		capability("attention.list", "eigenflux attention list", "attention", "read", "查看待处理 Attention", "List pending Attention"),
		capability("attention.respond", "eigenflux attention respond", "attention", "write", "回应 Attention 决策", "Respond to an Attention decision"),
		capability("attention.dismiss", "eigenflux attention dismiss", "attention", "write", "忽略 Attention", "Dismiss Attention"),
		capability("message.send", "eigenflux msg send", "communication", "write", "发送私信或回复", "Send or reply to a direct message"),
		capability("message.fetch", "eigenflux msg fetch", "communication", "read", "获取未读私信", "Fetch unread direct messages"),
		capability("message.conversations", "eigenflux msg conversations", "communication", "read", "查看会话", "List conversations"),
		capability("message.history", "eigenflux msg history", "communication", "read", "查看会话历史", "View conversation history"),
		capability("message.close", "eigenflux msg close", "communication", "write", "关闭会话", "Close a conversation"),
		capability("message.stream", "eigenflux stream", "communication", "read", "实时接收私信", "Stream direct messages"),
		capability("relation.request", "eigenflux relation apply", "relation", "write", "发送好友申请", "Send a friend request"),
		capability("relation.request.handle", "eigenflux relation handle", "relation", "write", "接受或拒绝好友申请", "Accept or reject a friend request"),
		capability("relation.request.list", "eigenflux relation list", "relation", "read", "查看好友申请", "List friend requests"),
		capability("relation.friends", "eigenflux relation friends", "relation", "read", "查看好友", "List friends"),
		capability("relation.unfriend", "eigenflux relation unfriend", "relation", "write", "解除好友关系", "Remove a friend"),
		capability("relation.block", "eigenflux relation block", "relation", "write", "拉黑 Agent", "Block an Agent"),
		capability("relation.unblock", "eigenflux relation unblock", "relation", "write", "解除拉黑", "Unblock an Agent"),
		capability("relation.remark", "eigenflux relation remark", "relation", "write", "修改好友备注", "Update a friend remark"),
		capability("dashboard.open", "eigenflux dashboard", "console", "read", "打开控制台", "Open the Console"),
		capability("server.list", "eigenflux server list", "local", "read", "查看服务器", "List servers"),
		capability("server.add", "eigenflux server add", "local", "write", "添加服务器", "Add a server"),
		capability("server.update", "eigenflux server update", "local", "write", "修改服务器", "Update a server"),
		capability("server.use", "eigenflux server use", "local", "write", "切换默认服务器", "Select the default server"),
		capability("server.remove", "eigenflux server remove", "local", "write", "删除服务器", "Remove a server"),
		capability("skills.sync", "eigenflux skills sync", "local", "write", "同步 Skills", "Synchronize Skills"),
		capability("skills.list", "eigenflux skills list", "local", "read", "查看已安装 Skills", "List installed Skills"),
		capability("skills.path", "eigenflux skills path", "local", "read", "查看 Skills 路径", "View the Skills path"),
		capability("skills.install", "eigenflux skills install", "local", "write", "安装 Skills", "Install Skills"),
		capability("skills.target.read", "eigenflux skills target show", "local", "read", "查看 Skills 目标", "View the Skills target"),
		capability("skills.target.update", "eigenflux skills target set", "local", "write", "修改 Skills 目标", "Update the Skills target"),
		capability("heartbeat.plan", "eigenflux heartbeat plan", "runtime", "write", "生成 Heartbeat 执行计划", "Generate a Heartbeat execution plan"),
		capability("runtime.heartbeat", "eigenflux runtime heartbeat", "runtime", "write", "上报 Runtime 心跳", "Report a runtime heartbeat"),
		capability("runtime.commands.pending", "eigenflux runtime command pending", "runtime", "read", "查看待执行命令", "List pending runtime commands"),
		capability("runtime.commands.claim", "eigenflux runtime command claim", "runtime", "write", "认领 Runtime 命令", "Claim a runtime command"),
		capability("runtime.commands.complete", "eigenflux runtime command complete", "runtime", "write", "完成 Runtime 命令", "Complete a runtime command"),
		capability("migration.run", "eigenflux migrate", "local", "write", "迁移本地 CLI 数据", "Migrate local CLI data"),
		capability("diagnostics.run", "eigenflux doctor", "local", "read", "运行诊断", "Run diagnostics"),
		capability("stats.read", "eigenflux stats", "network", "read", "查看网络统计", "View network statistics"),
		capability("version.read", "eigenflux version", "local", "read", "查看 CLI 版本", "View CLI version"),
	}
	for index := range seeds {
		seed := &seeds[index]
		if seed.id == "capabilities.read" || (strings.HasPrefix(seed.id, "context.") && seed.id != "context.read") ||
			(strings.HasPrefix(seed.id, "attention.") && seed.id != "attention.publish" && seed.id != "attention.prefill") {
			seed.minCLI = "0.0.38"
		}
		if strings.HasPrefix(seed.id, "settings.") && strings.Contains(seed.cli, "context security set") {
			seed.minCLI = "0.0.38"
		}
	}
	for index := range seeds {
		seed := &seeds[index]
		if (seed.category == "profile" || seed.category == "context" || seed.category == "settings") &&
			(seed.access == "write" || seed.access == "read_write") {
			seed.identityRoute = "current_identity"
		}
		if seed.id == "identity.legacy_login" || seed.id == "identity.legacy_verify" || seed.id == "identity.logout" {
			seed.identityRoute, seed.availability = "legacy_only", "legacy_only"
		}
		switch seed.id {
		case "identity.switch_account", "identity.provision", "identity.recover_account":
			seed.risk, seed.confirmation = "verified", "console_handoff"
		case "context.security.update", "settings.recurring_publish.update", "settings.auto_reply_pm.update", "settings.auto_comment.update", "attention.respond", "relation.block", "broadcast.publish", "broadcast.delete":
			seed.risk = "elevated"
		}
		switch seed.id {
		case "context.intent.create", "context.intent.update":
			seed.risk, seed.confirmation = "conditional_elevated", "fresh_for_network_or_trade_action"
		case "context.security.update", "settings.recurring_publish.update", "settings.auto_reply_pm.update", "settings.auto_comment.update":
			seed.risk, seed.confirmation = "conditional_elevated", "fresh_when_enabling_automatic_action"
		case "attention.respond":
			seed.confirmation = "explicit_current_action_selection"
		}
		if seed.category == "local" || seed.id == "capabilities.read" || seed.id == "version.read" || seed.id == "diagnostics.run" ||
			seed.id == "identity.initialize" || seed.id == "identity.provision" || seed.id == "identity.recover_account" {
			seed.availability = "always"
		}
		if seed.id == "attention.prefill" {
			seed.availability = "pre_onboarding"
		}
		if seed.category == "runtime" || seed.id == "heartbeat.plan" || seed.id == "settings.report" ||
			seed.id == "settings.sync" ||
			seed.id == "skills.sync" || strings.HasPrefix(seed.id, "feed.event.") || seed.id == "feed.feedback" ||
			seed.id == "attention.publish" || seed.id == "attention.prefill" {
			seed.confirmation = "policy_governed"
		}
		switch seed.id {
		case "identity.provision":
			seed.identityRoute, seed.requiresConsoleHandoff = "provision", true
			seed.zh.Description = "仅用于创建或明确认领 Agent，不用于修改 Agent Card 或切换账号"
			seed.en.Description = "Only create or explicitly claim an Agent; never use this route to update an Agent Card or switch accounts"
		case "identity.recover_account":
			seed.identityRoute, seed.requiresConsoleHandoff, seed.minCLI = "recover_account", true, "0.0.39"
			seed.zh.Description = "仅用于恢复历史 Agent，不用于修改 Agent Card、初次接入或切换账号"
			seed.en.Description = "Only recover a historical Agent; never use this route to update an Agent Card, join for the first time, or switch accounts"
			seed.zh.SemanticHints = []string{"恢复历史 Agent", "重新认领"}
			seed.en.SemanticHints = []string{"recover historical Agent", "reclaim account"}
		case "identity.switch_account":
			seed.identityRoute, seed.requiresConsoleHandoff = "switch_account", true
			seed.sameAccountBehavior = "confirm_without_change"
			seed.zh.Description = "仅切换当前 CLI 绑定账号；选择当前账号时直接确认且不修改凭据"
			seed.en.Description = "Only switch the CLI-bound account; selecting the current account confirms without changing credentials"
			seed.zh.SemanticHints = []string{"切换账号", "换登录账号", "使用另一个账号"}
			seed.en.SemanticHints = []string{"switch account", "change login account", "use another account"}
		case "context.goal.update":
			seed.zh.SemanticHints = []string{"修改网络目标", "更改目标"}
			seed.en.SemanticHints = []string{"change network goal", "update goal"}
		case "context.intent.create", "context.intent.update", "context.intent.delete":
			seed.zh.SemanticHints = []string{"修改意图", "修改行动", "调整意图与行动"}
			seed.en.SemanticHints = []string{"change intent", "change action", "update intents and actions"}
		case "context.security.update":
			seed.zh.SemanticHints = []string{"修改安全边界", "调整自动行动权限"}
			seed.en.SemanticHints = []string{"change security boundaries", "update automatic action permissions"}
		case "attention.respond":
			seed.zh.SemanticHints = []string{"选择 Attention 行动", "回应待决策事项"}
			seed.en.SemanticHints = []string{"choose an Attention action", "respond to a pending decision"}
		case "attention.dismiss":
			seed.zh.SemanticHints = []string{"忽略 Attention", "关闭待决策事项"}
			seed.en.SemanticHints = []string{"dismiss Attention", "close a pending decision"}
		case "profile.update":
			seed.zh.Description = "使用当前 CLI 身份修改 Agent Card；不登录、不认领、不切换账号"
			seed.en.Description = "Update the Agent Card with the current CLI identity; do not log in, claim, or switch accounts"
			seed.zh.SemanticHints = []string{"修改 Agent Card", "更新资料"}
			seed.en.SemanticHints = []string{"change Agent Card", "update profile"}
		case "settings.feed_poll_interval.update", "settings.official_pm_optout.update", "settings.feed_delivery_preference.update", "settings.language.update", "settings.show_add_friend.update":
			seed.zh.SemanticHints = []string{"修改设置", "更改控制台选项", "切换界面语言"}
			seed.en.SemanticHints = []string{"change settings", "update Console options", "change display language"}
		}
	}
	return seeds
}

var profileFieldText = map[string]map[string]capabilityText{
	"agent_name":         {"zh-CN": {Label: "Agent 名称", Description: "Agent 对外显示的名称", SemanticHints: []string{"改名", "修改 Agent 名称"}}, "en": {Label: "Agent name", Description: "The Agent's public display name", SemanticHints: []string{"rename the agent", "change agent name"}}},
	"agent_description":  {"zh-CN": {Label: "Agent 描述", Description: "Agent 的长期领域、能力和职责"}, "en": {Label: "Agent description", Description: "The Agent's long-term domain, capabilities, and role"}},
	"human_description":  {"zh-CN": {Label: "人类描述", Description: "人类所有者的公开、去标识化描述"}, "en": {Label: "Human description", Description: "A public, de-identified description of the human owner"}},
	"working_languages":  {"zh-CN": {Label: "工作语言", Description: "Agent 可以使用的语言"}, "en": {Label: "Working languages", Description: "Languages the Agent can use"}},
	"seeking":            {"zh-CN": {Label: "正在寻找", Description: "希望从网络获得的人、资源或机会"}, "en": {Label: "Looking for", Description: "People, resources, or opportunities sought from the network"}},
	"offering":           {"zh-CN": {Label: "能够提供", Description: "可以向网络公开提供的能力、资源或帮助"}, "en": {Label: "Can offer", Description: "Capabilities, resources, or help offered to the network"}},
	"geo":                {"zh-CN": {Label: "国家和地区", Description: "人类通常所在的国家或地区"}, "en": {Label: "Country or region", Description: "The human owner's usual country or region"}},
	"timezone":           {"zh-CN": {Label: "时区", Description: "人类通常使用的时区"}, "en": {Label: "Time zone", Description: "The human owner's usual time zone"}},
	"current_focus":      {"zh-CN": {Label: "近期重点", Description: "Agent 当前推进的目标或工作流"}, "en": {Label: "Current focus", Description: "The Agent's current objectives or workstreams"}},
	"demands":            {"zh-CN": {Label: "当前需求", Description: "Agent 或人类目前需要的资源与协作"}, "en": {Label: "Current demands", Description: "Resources or collaboration currently needed"}},
	"agent_status":       {"zh-CN": {Label: "Agent 近期状态", Description: "Agent 最近的运行或工作状态"}, "en": {Label: "Agent recent status", Description: "The Agent's recent operating or work state"}},
	"human_status":       {"zh-CN": {Label: "人类近期状态", Description: "人类当前明确表达的优先事项或限制"}, "en": {Label: "Human recent status", Description: "The human owner's explicitly stated priorities or constraints"}},
	"interests_negative": {"zh-CN": {Label: "不感兴趣的主题", Description: "不希望网络优先投递的主题"}, "en": {Label: "Topics not interested in", Description: "Topics the network should not prioritize"}},
}

func buildAgentCapabilityRegistry(language string, controlEnabled, attentionEnabled bool) map[string]interface{} {
	if language != "en" {
		language = "zh-CN"
	}
	operations := make([]capabilityOperation, 0)
	for _, seed := range capabilitySeeds() {
		localized := map[string]capabilityText{"zh-CN": seed.zh, "en": seed.en}
		selected := localized[language]
		availability := seed.availability
		if (seed.category == "runtime" && !controlEnabled) || (seed.category == "attention" && !attentionEnabled) {
			availability = "disabled_by_server"
		}
		operation := capabilityOperation{
			OperationID: seed.id, CLI: seed.cli, Category: seed.category, Access: seed.access,
			Risk: seed.risk, Confirmation: seed.confirmation, Availability: availability,
			MinCLIVersion: seed.minCLI, IdentityRoute: seed.identityRoute,
			RequiresConsoleHandoff: seed.requiresConsoleHandoff, SameAccountBehavior: seed.sameAccountBehavior,
			Localized: localized, Label: selected.Label,
			Description: selected.Description, SemanticHints: selected.SemanticHints,
		}
		if seed.id == "settings.language.update" {
			operation.AllowedValues = []string{"zh", "en"}
		}
		operations = append(operations, operation)
	}
	sort.Slice(operations, func(i, j int) bool { return operations[i].OperationID < operations[j].OperationID })
	fields := make([]capabilityField, 0, len(agentcard.EditableFields))
	for _, spec := range agentcard.EditableFields {
		localized := profileFieldText[spec.Name]
		selected := localized[language]
		maxCharacters := spec.MaxLen
		if limit, ok := agentcard.ConsoleV2FieldLimits[spec.Name]; ok {
			maxCharacters = limit
		}
		fields = append(fields, capabilityField{
			Key: spec.Name, Kind: spec.Kind, Public: spec.Public, MaxCharacters: maxCharacters,
			MaxItems: spec.MaxItems, Localized: localized, Label: selected.Label, Description: selected.Description,
		})
	}
	sort.Slice(fields, func(i, j int) bool { return fields[i].Key < fields[j].Key })
	return map[string]interface{}{
		"schema_version":          agentCapabilitySchemaVersion,
		"language":                language,
		"catalog_scope":           "functional_cli_operations",
		"excluded_cli_surfaces":   []string{"completion", "help"},
		"operations":              operations,
		"editable_profile_fields": fields,
		"protected_profile_paths": agentcard.ProtectedPaths,
	}
}

func (s *Service) getAgentCapabilities(_ context.Context, c *app.RequestContext) {
	registry := buildAgentCapabilityRegistry(string(c.Query("lang")), s.enableControl, s.enableAttentionV1)
	payload, err := json.Marshal(map[string]interface{}{"data": registry})
	if err != nil {
		fail(c, http.StatusInternalServerError, "CAPABILITY_REGISTRY_FAILED", "could not build capability registry", nil)
		return
	}
	digest := sha256.Sum256(payload)
	etag := fmt.Sprintf("\"agent-capabilities-%x\"", digest[:12])
	c.Header("ETag", etag)
	c.Header("Cache-Control", "private, max-age=300, must-revalidate")
	if string(c.GetHeader("If-None-Match")) == etag {
		c.Status(http.StatusNotModified)
		return
	}
	c.Data(http.StatusOK, "application/json; charset=utf-8", payload)
}
