package cmd

import "cli.eigenflux.ai/internal/dispatch"

func desktopCommissionBlockerText(language, status, code string) (string, string) {
	category := dispatch.CommissionExecutionCode(code, status)
	zh := map[string]string{
		"skill_missing":         "本地缺少履约技能。请安装所需技能，再让 Agent 核对订单并继续处理。",
		"skill_unreadable":      "无法读取本地履约技能。请检查访问权限，再让 Agent 核对订单并继续处理。",
		"skill_invalid":         "本地履约技能的名称或内容无效。请修复技能，再让 Agent 核对订单并继续处理。",
		"contract_invalid":      "无法读取约定的履约要求。请让 Agent 核对订单合同后处理。",
		"permission_required":   "本地执行需要授权。请和 Agent 继续对话，核对需要授予的权限。",
		"materials_missing":     "所需输入材料尚未齐全。请和 Agent 继续对话补齐材料。",
		"materials_invalid":     "输入材料未通过校验。请让 Agent 核对订单材料后处理。",
		"materials_unavailable": "暂时无法读取输入材料。请让 Agent 核对材料是否可用。",
		"inputs_changed":        "输入材料发生变化。请让 Agent 重新核对材料后处理。",
		"order_changed":         "订单状态发生变化。请先核对当前订单，再决定后续操作。",
		"output_unavailable":    "无法创建本地交付文件。请检查目录访问权限和可用空间。",
		"execution_failed":      "本地自动处理失败。请和 Agent 继续对话，核对原因后处理。",
		"execution_unconfirmed": "无法确认本地处理是否完成。请先核对已有结果，避免重复执行。",
		"execution_blocked":     "本地自动处理需要你介入。请和 Agent 继续对话，核对订单后处理。",
	}
	en := map[string]string{
		"skill_missing":         "The local fulfillment skill is missing. Install it, then ask your Agent to check the order and continue.",
		"skill_unreadable":      "The local fulfillment skill cannot be read. Check access permissions, then ask your Agent to check the order.",
		"skill_invalid":         "The local fulfillment skill name or content is invalid. Fix the skill, then ask your Agent to check the order.",
		"contract_invalid":      "The agreed fulfillment requirements could not be read. Ask your Agent to check the order contract.",
		"permission_required":   "Local execution needs permission. Continue with your Agent to review the required access.",
		"materials_missing":     "Required inputs are missing. Continue with your Agent to supply them.",
		"materials_invalid":     "The input files failed verification. Ask your Agent to check the order inputs.",
		"materials_unavailable": "The input files cannot currently be read. Ask your Agent to check their availability.",
		"inputs_changed":        "The inputs have changed. Ask your Agent to check them again before continuing.",
		"order_changed":         "The order state has changed. Check the current order before taking further action.",
		"output_unavailable":    "Local output files could not be created. Check directory permissions and available space.",
		"execution_failed":      "Local automatic processing failed. Continue with your Agent to check the cause.",
		"execution_unconfirmed": "Local completion is unconfirmed. Check existing results before repeating any work.",
		"execution_blocked":     "Local automatic processing needs your attention. Continue with your Agent to check the order.",
	}
	if language == "zh" {
		return "「{service_name}」自动处理受阻", zh[category] + " 订单时限仍以订单页为准。"
	}
	return "Automatic processing blocked for “{service_name}”", en[category] + " Order deadlines still apply."
}
