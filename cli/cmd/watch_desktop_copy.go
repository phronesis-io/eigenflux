package cmd

import (
	"strings"
	"time"
)

const desktopServiceNamePlaceholder = "{service_name}"

type desktopLocalizedCopy struct {
	zh, en string
}

type desktopRoleCopy struct {
	buyer, seller desktopLocalizedCopy
}

// Approved title/body pairs from the notification copy document.
// https://pcnlty6lw65j.feishu.cn/wiki/Mw3bwlX2siZlXjkgxx3cF21Pnyc
// Each string contains the title, a newline, then the body.
var desktopOrderCopy = map[string]desktopRoleCopy{
	"awaiting_seller": {
		buyer: desktopLocalizedCopy{
			zh: "「{service_name}」已下单，等待对方接单\n对方 Agent 正在检查你的材料，接单后会通知你付款。",
			en: "“{service_name}” ordered, waiting for the seller\nThe seller's Agent is checking your materials. You'll be asked to pay once it accepts.",
		},
		seller: desktopLocalizedCopy{
			zh: "「{service_name}」有新订单\n你的 Agent 正在检查材料、决定是否接单，无需操作。",
			en: "New order for “{service_name}”\nYour Agent is checking the materials and deciding whether to accept. Nothing for you to do.",
		},
	},
	"pending_payment": {
		buyer: desktopLocalizedCopy{
			zh: "「{service_name}」已接单，请付款\n请在 {payment_due} 前付款，超时订单自动取消。",
			en: "“{service_name}” accepted, payment needed\nPlease pay by {payment_due}, or the order is cancelled automatically.",
		},
		seller: desktopLocalizedCopy{
			zh: "「{service_name}」已接单，等待买方付款\n买方付款后，你的 Agent 会自动开始交付，无需操作。",
			en: "“{service_name}” accepted, waiting for payment\nYour Agent starts delivering once the buyer pays. Nothing for you to do.",
		},
	},
	"in_progress": {
		buyer: desktopLocalizedCopy{
			zh: "「{service_name}」付款成功，等待交付\n预计 {delivery_due} 前交付，逾期未交付将自动退款。",
			en: "“{service_name}” paid, awaiting delivery\nDelivery due by {delivery_due}. If it's late, you're refunded automatically.",
		},
		seller: desktopLocalizedCopy{
			zh: "「{service_name}」买方已付款，等待交付\n需在 {delivery_due} 前交付，逾期将自动退款给买方。",
			en: "“{service_name}” paid, awaiting delivery\nDeliver by {delivery_due}, or the buyer is refunded automatically.",
		},
	},
	"validating": {
		buyer: desktopLocalizedCopy{
			zh: "「{service_name}」已交付，平台检查中\n检查通过后会通知你查看并确认结果。",
			en: "“{service_name}” delivered, platform check in progress\nYou'll be asked to review and confirm once the check passes.",
		},
		seller: desktopLocalizedCopy{
			zh: "「{service_name}」已提交交付，平台检查中\n无需操作，检查通过后会转给买方确认。",
			en: "“{service_name}” submitted, platform check in progress\nNothing for you to do. It goes to the buyer for confirmation once the check passes.",
		},
	},
	"awaiting_buyer_confirmation": {
		buyer: desktopLocalizedCopy{
			zh: "「{service_name}」交付完成，请查看并确认\n请在 {confirm_due} 前确认，逾期将自动确认完成。",
			en: "“{service_name}” is ready for your review\nPlease confirm by {confirm_due}, or it's confirmed automatically.",
		},
		seller: desktopLocalizedCopy{
			zh: "「{service_name}」已交给买方，等待确认\n无需操作。买方 {confirm_due} 前未确认将自动完成。",
			en: "“{service_name}” delivered, waiting for the buyer\nNothing for you to do. If the buyer doesn't confirm by {confirm_due}, it completes automatically.",
		},
	},
	"order.reminder.buyer_confirmation_24h.v1": {
		buyer: desktopLocalizedCopy{
			zh: "「{service_name}」还未确认交付\n请尽快查看结果，{confirm_due} 后将自动确认完成。",
			en: "“{service_name}” still needs your confirmation\nPlease review it soon. It's confirmed automatically after {confirm_due}.",
		},
	},
	"completed": {
		buyer: desktopLocalizedCopy{
			zh: "「{service_name}」订单已完成\n交付结果可随时在订单页查看。",
			en: "“{service_name}” order completed\nYou can view the delivery on the order page anytime.",
		},
		seller: desktopLocalizedCopy{
			zh: "「{service_name}」订单已完成\n收益入账后会另行通知你。",
			en: "“{service_name}” order completed\nYou'll be notified when the earnings are credited.",
		},
	},
	"rejected": {
		buyer: desktopLocalizedCopy{
			zh: "「{service_name}」对方未接单\n订单已关闭，未扣款。可以换个服务，或调整材料后重新下单。",
			en: "“{service_name}” declined by the seller\nThe order is closed and you weren't charged. Try another service, or adjust your materials and order again.",
		},
		seller: desktopLocalizedCopy{
			zh: "「{service_name}」你的 Agent 已拒单\n订单已关闭，无需操作。",
			en: "“{service_name}” declined by your Agent\nThe order is closed. Nothing for you to do.",
		},
	},
	"cancelled": {
		buyer: desktopLocalizedCopy{
			zh: "「{service_name}」订单已取消\n如已付款，退款结果会另行通知。",
			en: "“{service_name}” order cancelled\nIf you already paid, you'll be notified about the refund.",
		},
		seller: desktopLocalizedCopy{
			zh: "「{service_name}」订单已取消\n订单已关闭，无需操作。",
			en: "“{service_name}” order cancelled\nThe order is closed. Nothing for you to do.",
		},
	},
	"refund_pending": {
		buyer: desktopLocalizedCopy{
			zh: "「{service_name}」未按时交付，正在退款\n款项将退回你的付款账户，到账后另行通知。",
			en: "“{service_name}” was not delivered on time, refund started\nYour payment is being returned. You'll be notified when it arrives.",
		},
		seller: desktopLocalizedCopy{
			zh: "「{service_name}」交付超时，订单转为退款\n本单款项退还买方，不计收益。",
			en: "“{service_name}” delivery overdue, order refunded\nThe payment goes back to the buyer. No earnings for this order.",
		},
	},
	"refunded": {
		buyer: desktopLocalizedCopy{
			zh: "「{service_name}」已退款\n款项已退回你的付款账户。",
			en: "“{service_name}” refunded\nYour payment has been returned.",
		},
		seller: desktopLocalizedCopy{
			zh: "「{service_name}」订单已退款\n本单不计收益，无需操作。",
			en: "“{service_name}” order refunded\nNo earnings for this order. Nothing for you to do.",
		},
	},
	"order.refund.processing_error.v1": {
		buyer: desktopLocalizedCopy{
			zh: "「{service_name}」退款遇到问题，系统正在重试\n无需操作，退款到账后会通知你。",
			en: "“{service_name}” refund hit a problem, retrying\nNothing for you to do. You'll be notified when the refund arrives.",
		},
		seller: desktopLocalizedCopy{
			zh: "「{service_name}」退款处理中，系统正在重试\n无需操作。",
			en: "“{service_name}” refund is being retried\nNothing for you to do.",
		},
	},
	"order.refund.completed.v1": {
		buyer: desktopLocalizedCopy{
			zh: "「{service_name}」订单已取消，款项已退回\n款项已退回你的付款账户。",
			en: "“{service_name}” cancelled, payment returned\nYour payment has been returned.",
		},
		seller: desktopLocalizedCopy{
			zh: "「{service_name}」订单已取消，款项已退还买方\n本单不计收益，无需操作。",
			en: "“{service_name}” cancelled, buyer refunded\nNo earnings for this order. Nothing for you to do.",
		},
	},
	"order.settlement.processing_error.v1": {
		buyer: desktopLocalizedCopy{
			zh: "「{service_name}」收益入账遇到问题，系统正在重试\n无需操作，入账成功后会通知你。",
			en: "“{service_name}” earnings credit hit a problem, retrying\nNothing for you to do. You'll be notified once it's credited.",
		},
	},
	"order.settlement.completed.v1": {
		buyer: desktopLocalizedCopy{
			zh: "「{service_name}」收益已存入平台钱包\n可在控制台查看余额或提现。",
			en: "“{service_name}” earnings added to your wallet\nCheck your balance or withdraw in the Console.",
		},
	},
	"order.validation.processing_error.v1": {
		buyer: desktopLocalizedCopy{
			zh: "「{service_name}」交付检查暂时出错，系统正在重试\n无需操作，检查通过后会通知你确认。",
			en: "“{service_name}” delivery check hit a problem, retrying\nNothing for you to do. You'll be asked to confirm once it passes.",
		},
		seller: desktopLocalizedCopy{
			zh: "「{service_name}」交付检查暂时出错，系统正在重试\n无需操作，检查通过后会转给买方确认。",
			en: "“{service_name}” delivery check hit a problem, retrying\nNothing for you to do. It goes to the buyer once the check passes.",
		},
	},
	"local.needs_user": {
		buyer: desktopLocalizedCopy{
			zh: "「{service_name}」需要你处理\n你的 Agent 处理这笔订单时需要你的介入，点击打开订单处理。",
			en: "“{service_name}” needs your attention\nYour Agent requires your help on this order. Click for further details.",
		},
	},
	"local.failed": {
		buyer: desktopLocalizedCopy{
			zh: "「{service_name}」自动处理失败\n点击打开订单查看，必要时手动处理，以免订单超时。",
			en: "“{service_name}” couldn't be handled automatically\nClick to open the order and handle it manually if needed, before it times out.",
		},
	},
	"local.unknown": {
		buyer: desktopLocalizedCopy{
			zh: "「{service_name}」处理结果待核对\n无法确认你的 Agent 是否已完成，点击打开订单核对。",
			en: "“{service_name}” result needs checking\nWe can't confirm whether your Agent finished. Click to open the order and check.",
		},
	},
	"preparing_materials": {
		buyer: desktopLocalizedCopy{
			zh: "「{service_name}」订单状态更新中\n系统正在自动处理，无需操作。",
			en: "“{service_name}” order is being updated\nThe system is handling it. Nothing for you to do.",
		},
	},
	"expired": {
		buyer: desktopLocalizedCopy{
			zh: "「{service_name}」订单已过期\n未在时限内付款，订单已关闭，未扣款。",
			en: "“{service_name}” order expired\nPayment wasn't made in time. The order is closed and you weren't charged.",
		},
		seller: desktopLocalizedCopy{
			zh: "「{service_name}」订单已过期\n买方未在时限内付款，订单已关闭，无需操作。",
			en: "“{service_name}” order expired\nThe buyer didn't pay in time. The order is closed. Nothing for you to do.",
		},
	},
	"fallback": {
		buyer: desktopLocalizedCopy{
			zh: "「{service_name}」订单有新进展\n点击查看订单详情。",
			en: "“{service_name}” has an update\nClick to see the order details.",
		},
	},
}

func commissionDesktopCopy(language, notificationType string, payload orderNotificationPayload) (string, string) {
	key := payload.ToState
	switch key {
	case "awaiting_payment":
		key = "pending_payment"
	case "awaiting_materials":
		key = "preparing_materials"
	case "delivered":
		key = "awaiting_buyer_confirmation"
	case "refunding":
		key = "refund_pending"
	}
	if payload.CommandKind == "reject_order" && payload.ToState == "cancelled" {
		key = "rejected"
	}
	// Match the existing terminal resolver's result/reminder precedence.
	for _, kind := range []string{payload.EventKind, notificationType} {
		if commissionResultNotificationText("en", kind) != "" || kind == "order.reminder.buyer_confirmation_24h.v1" {
			key = kind
			break
		}
	}
	title, body := desktopCopyText(language, payload.RecipientRole, key)
	body = strings.NewReplacer(
		"{payment_due}", desktopDeadline(language, payload.PaymentDueAt),
		"{delivery_due}", desktopDeadline(language, payload.DeliveryDueAt),
		"{confirm_due}", desktopDeadline(language, payload.BuyerConfirmDue),
	).Replace(body)
	return title, body
}

func desktopCopyText(language, role, key string) (string, string) {
	copy, ok := desktopOrderCopy[key]
	if !ok {
		copy = desktopOrderCopy["fallback"]
	}
	text := copy.buyer
	if role == "seller" && copy.seller.en != "" {
		text = copy.seller
	}
	value := text.en
	if language == "zh" {
		value = text.zh
	}
	title, body, _ := strings.Cut(value, "\n")
	return title, body
}

func desktopDeadline(language string, millis int64) string {
	// Older events can omit deadlines. Never invent a date or display Unix zero.
	if millis <= 0 {
		if language == "zh" {
			return "订单页所示时间"
		}
		return "the deadline shown on the order page"
	}
	return time.UnixMilli(millis).Local().Format("2006-01-02 15:04 -07:00")
}
