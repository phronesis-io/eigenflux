package cmd

import (
	"strconv"
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
	"order.review.created.v1": {seller: desktopLocalizedCopy{
		zh: "「{service_name}」收到新评价 · {review_score}/5 分\n买方已评价，点击查看详情。",
		en: "New review for “{service_name}” · {review_score}/5\nThe buyer left a review. Click to view details.",
	}},
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
			zh: "「{service_name}」已交付，平台检查中\n检查通过后可在订单页查看交付结果。",
			en: "“{service_name}” delivered, platform check in progress\nOpen the order to view the delivery once the check passes.",
		},
		seller: desktopLocalizedCopy{
			zh: "「{service_name}」已提交交付，平台检查中\n检查通过后进入售后期。",
			en: "“{service_name}” submitted, platform check in progress\nThe after-sales period starts once the check passes.",
		},
	},
	"awaiting_buyer_confirmation": {
		buyer: desktopLocalizedCopy{
			zh: "「{service_name}」平台已通过交付检查\n请在 {confirm_due} 前查看交付结果。如需售后，请打开订单。",
			en: "“{service_name}” is ready for your review\nView the delivery by {confirm_due}. Open the order for after-sales help.",
		},
		seller: desktopLocalizedCopy{
			zh: "「{service_name}」已通过平台交付检查\n售后期至 {confirm_due}，到期后由平台验收。",
			en: "“{service_name}” delivery passed the platform check\nThe platform accepts delivery after the after-sales deadline at {confirm_due} if there are no pending requests.",
		},
	},
	"order.reminder.buyer_confirmation_24h.v1": {
		buyer: desktopLocalizedCopy{
			zh: "「{service_name}」售后期即将结束\n如需退款或投诉，请在 {confirm_due} 前打开订单提交。",
			en: "“{service_name}” after-sales period ends soon\nOpen the order before {confirm_due} to request a refund or file a complaint.",
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
	"payment_expired": {buyer: desktopLocalizedCopy{
		zh: "「{service_name}」付款超时，订单已关闭\n如有扣款，退款结果会另行通知。",
		en: "Payment timed out for “{service_name}”\nThe order is closed. If a payment was taken, the refund result will follow.",
	}},
	"seller_expired": {buyer: desktopLocalizedCopy{
		zh: "「{service_name}」卖家接单超时\n订单已关闭，未扣款。",
		en: "Seller acceptance timed out for “{service_name}”\nThe order is closed. No payment was taken.",
	}},
	"buyer_cancelled": {buyer: desktopLocalizedCopy{
		zh: "「{service_name}」买家已取消订单\n如有扣款，退款结果会另行通知。",
		en: "The buyer cancelled “{service_name}”\nIf a payment was taken, the refund result will follow.",
	}},
	"late_payment_detected": {buyer: desktopLocalizedCopy{
		zh: "「{service_name}」超时付款，正在退款\n退款结果会另行通知。",
		en: "Late payment for “{service_name}”, refund in progress\nThe refund result will follow.",
	}},
	"risk_blocked": {buyer: desktopLocalizedCopy{
		zh: "「{service_name}」因平台风控关闭\n如有扣款，退款结果会另行通知。",
		en: "“{service_name}” was closed by platform risk checks\nIf a payment was taken, the refund result will follow.",
	}},
	"auto_completed": {
		buyer: desktopLocalizedCopy{
			zh: "「{service_name}」平台已验收\n点击查看交付结果。",
			en: "“{service_name}” accepted by the platform\nClick to view the delivery.",
		},
		seller: desktopLocalizedCopy{
			zh: "「{service_name}」平台已验收\n收益入账后会另行通知你。",
			en: "“{service_name}” accepted by the platform\nYou'll be notified when the earnings are credited.",
		},
	},
	"refund_pending": {
		buyer: desktopLocalizedCopy{
			zh: "「{service_name}」正在退款\n款项将退回你的付款账户，到账后另行通知。",
			en: "“{service_name}” refund started\nYour payment is being returned. You'll be notified when it arrives.",
		},
		seller: desktopLocalizedCopy{
			zh: "「{service_name}」订单正在退款\n本单款项退还买方，不计收益。",
			en: "“{service_name}” refund in progress\nThe payment goes back to the buyer. No earnings for this order.",
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
			zh: "「{service_name}」收益已存入平台钱包\n点击查看订单收益。",
			en: "“{service_name}” earnings added to your wallet\nClick to view the order earnings.",
		},
	},
	"order.validation.processing_error.v1": {
		buyer: desktopLocalizedCopy{
			zh: "「{service_name}」交付检查暂时出错，系统正在重试\n无需操作，检查通过后可在订单页查看。",
			en: "“{service_name}” delivery check hit a problem, retrying\nNothing for you to do. Open the order to view the delivery once it passes.",
		},
		seller: desktopLocalizedCopy{
			zh: "「{service_name}」交付检查暂时出错，系统正在重试\n检查通过后进入售后期。",
			en: "“{service_name}” delivery check hit a problem, retrying\nNothing for you to do. The after-sales period starts once the check passes.",
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
	key := commissionDesktopCopyKey(notificationType, payload)
	title, body := desktopCopyText(language, payload.RecipientRole, key)
	if key == "order.review.created.v1" {
		score := "—"
		if payload.ReviewScore >= 1 && payload.ReviewScore <= 5 {
			score = strconv.Itoa(int(payload.ReviewScore))
		}
		title = strings.ReplaceAll(title, "{review_score}", score)
	}
	body = strings.NewReplacer(
		"{payment_due}", desktopDeadline(language, payload.PaymentDueAt),
		"{delivery_due}", desktopDeadline(language, payload.DeliveryDueAt),
		"{confirm_due}", desktopDeadline(language, payload.BuyerConfirmDue),
	).Replace(body)
	return title, body
}

func commissionDesktopCopyKey(notificationType string, payload orderNotificationPayload) string {
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
	// Report the specific result before the underlying order state.
	event := commissionNoticeResultKind(notificationType, payload)
	if event != "" {
		key = event
	} else if key == "cancelled" {
		switch payload.CommandKind {
		case "reject_order":
			key = "rejected"
		case "payment_expired":
			key = "payment_expired"
		case "seller_expired":
			key = "seller_expired"
		case "cancel":
			key = "buyer_cancelled"
		case "late_payment_detected":
			key = "late_payment_detected"
		case "risk_blocked":
			key = "risk_blocked"
		}
	} else if key == "completed" && payload.CommandKind == "buyer_confirmation_expired" {
		key = "auto_completed"
	}
	return key
}

func commissionNoticeResultKind(notificationType string, payload orderNotificationPayload) string {
	for _, kind := range []string{payload.EventKind, notificationType} {
		if commissionResultNotificationText("en", kind) != "" || kind == "order.reminder.buyer_confirmation_24h.v1" {
			return kind
		}
	}
	return ""
}

func commissionDesktopDetails(language, notificationType string, payload orderNotificationPayload) string {
	details := desktopCounterpartyPlaceholder
	switch commissionNoticeResultKind(notificationType, payload) {
	case "order.settlement.completed.v1":
		return desktopCreditPlaceholder + details
	case "order.refund.completed.v1", "order.refund.processing_error.v1":
		return desktopRefundAmountPlaceholder + details
	}
	switch payload.ToState {
	case "awaiting_seller":
		if payload.RecipientRole == "seller" {
			details += desktopOrderAmountPlaceholder
			if payload.SellerConfirmDue > 0 {
				at := desktopDeadline(language, payload.SellerConfirmDue)
				if language == "zh" {
					details += "接单截止 " + at + "\n"
				} else {
					details += "Accept by " + at + "\n"
				}
			} else {
				details += desktopActionDeadlinePlaceholder
			}
		}
	case "pending_payment", "awaiting_payment":
		details += desktopOrderAmountPlaceholder
	case "refund_pending", "refunding", "refunded":
		details = desktopRefundAmountPlaceholder + details
	case "cancelled":
		if payload.CommandKind == "late_payment_detected" {
			details = desktopRefundAmountPlaceholder + details
		}
	}
	return details
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
