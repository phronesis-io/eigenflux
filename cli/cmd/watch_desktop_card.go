package cmd

import (
	"strconv"
	"strings"
	"time"
)

const (
	desktopCardService = "{order_card_service}"
	desktopCardGross   = "{order_card_gross}"
	desktopCardNet     = "{order_card_net}"
	desktopCardRefund  = "{order_card_refund}"
	desktopCardCredit  = "{order_card_credit}"
)

var commissionDesktopCardTitles = map[string]desktopLocalizedCopy{
	"awaiting_seller":             {"待接单", "Awaiting acceptance"},
	"pending_payment":             {"待付款", "Awaiting payment"},
	"in_progress":                 {"已付款 · 待交付", "Paid · Delivery due"},
	"validating":                  {"交付检查中", "Checking delivery"},
	"awaiting_buyer_confirmation": {"待确认交付", "Confirm delivery"},
	"order.reminder.buyer_confirmation_24h.v1": {"交付确认提醒", "Confirmation reminder"},
	"completed":                            {"订单已完成", "Order completed"},
	"auto_completed":                       {"确认超时 · 自动完成", "Confirmation timed out · Completed"},
	"rejected":                             {"卖家已拒单", "Seller declined"},
	"cancelled":                            {"订单已取消", "Order cancelled"},
	"payment_expired":                      {"付款超时 · 订单关闭", "Payment timeout · Closed"},
	"seller_expired":                       {"接单超时 · 订单关闭", "Acceptance timeout · Closed"},
	"buyer_cancelled":                      {"买家已取消订单", "Buyer cancelled"},
	"late_payment_detected":                {"付款超时 · 退款中", "Late payment · Refunding"},
	"risk_blocked":                         {"平台风控 · 订单关闭", "Risk checks · Closed"},
	"refund_pending":                       {"交付超时 · 退款中", "Delivery overdue · Refunding"},
	"refunded":                             {"订单已退款", "Order refunded"},
	"order.refund.processing_error.v1":     {"退款异常 · 重试中", "Refund issue · Retrying"},
	"order.refund.completed.v1":            {"取消订单 · 退款已退回", "Cancelled · Refunded"},
	"order.settlement.completed.v1":        {"净入账", "Net credited"},
	"order.settlement.processing_error.v1": {"入账异常 · 重试中", "Credit issue · Retrying"},
	"order.validation.processing_error.v1": {"交付检查异常 · 重试中", "Delivery check · Retrying"},
	"order.review.created.v1":              {"收到评价", "Review received"},
}

func commissionDesktopCard(language, notificationType string, p orderNotificationPayload) (string, string) {
	key := commissionDesktopCopyKey(notificationType, p)
	copy, ok := commissionDesktopCardTitles[key]
	if !ok {
		copy = desktopLocalizedCopy{"订单有更新", "Order updated"}
	}
	zh := language == "zh"
	title := copy.en
	if zh {
		title = copy.zh
	}
	body := desktopCounterpartyPlaceholder + desktopCardService + "\n"
	switch key {
	case "awaiting_seller":
		title += desktopCardGross
		body += desktopCardDeadline(zh, "接单截止", "Accept by", p.SellerConfirmDue)
	case "pending_payment":
		title += desktopCardGross
		body += desktopCardDeadline(zh, "付款截止", "Pay by", p.PaymentDueAt)
	case "in_progress":
		body += desktopCardDeadline(zh, "交付截止", "Deliver by", p.DeliveryDueAt)
	case "awaiting_buyer_confirmation", "order.reminder.buyer_confirmation_24h.v1":
		body += desktopCardDeadline(zh, "确认截止", "Confirm by", p.BuyerConfirmDue)
	case "order.settlement.completed.v1":
		title += desktopCardNet
		body += desktopCardCredit
	case "refund_pending", "refunded", "late_payment_detected", "order.refund.processing_error.v1", "order.refund.completed.v1":
		title += desktopCardRefund
	case "order.review.created.v1":
		if p.ReviewScore >= 1 && p.ReviewScore <= 5 {
			title += " · " + strconv.Itoa(int(p.ReviewScore)) + "/5"
		}
	case "seller_expired", "rejected":
		if zh {
			body += "未扣款"
		} else {
			body += "No charge"
		}
	case "cancelled", "buyer_cancelled", "payment_expired", "risk_blocked":
		if zh {
			body += "如已扣款，退款另行通知"
		} else {
			body += "Refund update if charged"
		}
	}
	return title, body
}

func desktopCardTime(millis int64) string {
	at := time.UnixMilli(millis).Local()
	if at.Year() != time.Now().Year() {
		return at.Format("2006-01-02 15:04 -07:00")
	}
	return at.Format("01/02 15:04 -07:00")
}

func desktopCardDeadline(zh bool, labelZH, labelEN string, millis int64) string {
	if millis <= 0 {
		if zh {
			return "截止时间见订单"
		}
		return "See order for deadline"
	}
	label := labelEN
	if zh {
		label = labelZH
	}
	return label + " " + desktopCardTime(millis)
}

func desktopCardCreditLine(credit *commissionNoticeCredit, english bool) string {
	zh, en := "可提现时间见订单", "Availability in order"
	if credit != nil {
		switch credit.State {
		case "unmatured":
			if credit.AvailableAt > 0 {
				at := desktopCardTime(credit.AvailableAt)
				zh, en = "可提现 "+at, "Available "+at
			}
		case "withdrawable":
			zh, en = "当前可提现", "Available now"
		case "reserved":
			zh, en = "提现处理中", "Withdrawal processing"
		case "withdrawn":
			zh, en = "已提现", "Withdrawn"
		}
	}
	if english {
		return en
	}
	return zh
}

func desktopCommissionBlockerCard(language, title, body string) (string, string) {
	body = strings.TrimPrefix(body, desktopActionDeadlinePlaceholder)
	if language == "zh" {
		title = strings.TrimPrefix(title, "「"+desktopServiceNamePlaceholder+"」")
	} else {
		title = strings.TrimSuffix(title, " for “"+desktopServiceNamePlaceholder+"”")
	}
	return title, desktopCardService + "\n" + desktopActionDeadlinePlaceholder + body
}
