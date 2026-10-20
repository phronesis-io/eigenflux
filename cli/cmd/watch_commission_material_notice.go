package cmd

import (
	"context"
	"errors"
	"strconv"
	"strings"

	"cli.eigenflux.ai/internal/desktopnotify"
	"cli.eigenflux.ai/internal/dispatch"
)

// Persist both reminders before completing the single automatic answer attempt.
// No missing fact or file is fabricated and no ambient file is selected.
func (w *accountWatch) finishBuyerMaterialWaiting(ctx context.Context, job dispatch.Job, data dispatch.CommissionClarificationJob, code, session string) error {
	if err := w.commissionIdentityOK(); err != nil {
		return err
	}
	order, err := w.readBuyerClarificationOrder(ctx, data.OrderID)
	if err != nil {
		return w.finishDispatch(job, "failed", "commission_order_unavailable", session, "")
	}
	if !clarificationRequestMatchesOrder(data, order) {
		return w.finishDispatch(job, "completed", "commission_clarification_order_changed", session, "")
	}
	if err := w.commissionIdentityOK(); err != nil {
		return err
	}
	if w.desktop != nil {
		target, err := orderDesktopURL(w.server.Endpoint, w.binding.AgentID, data.OrderID, "buyer")
		if err != nil {
			return err
		}
		title, body := buyerMaterialNoticeCopy(desktopnotify.SystemLanguage(), order.InputRequest)
		message := desktopnotify.Message{ID: buyerMaterialDesktopID(w.scope, data), Title: title, Body: body, URL: target}
		if err := w.desktop.Enqueue(message); err != nil {
			return err
		}
	}
	queue, err := w.mainNoticeQueue()
	if err != nil {
		return err
	}
	if queue != nil {
		err = queue.Add(dispatch.BuyerInputMainNotice(w.scope, data.OrderID, data.OrderVersion, data.RequestID))
		if errors.Is(err, dispatch.ErrMainNoticeQueueFull) {
			_ = w.emit("commission_main_notice_status", map[string]string{"order_id": data.OrderID, "status": "skipped", "code": "main_notice_queue_full"})
		} else if err != nil {
			return err
		}
	}
	return w.finishDispatch(job, "completed", code, session, "")
}

func buyerMaterialDesktopID(scope string, data dispatch.CommissionClarificationJob) string {
	return "inputs:" + scope + ":" + data.OrderID + ":" + strconv.FormatInt(data.OrderVersion, 10) + ":" + data.RequestID
}

func buyerMaterialNoticeCopy(language string, request *commissionInputRequest) (string, string) {
	question := desktopServiceName(request.Question)
	missing := desktopServiceName(strings.Join(request.MissingInputs, ", "))
	if missing == "required_input_files" {
		missing = "files required by the order"
		if language == "zh" {
			missing = "订单要求的材料文件"
		}
	}
	if language == "zh" {
		body := "请打开订单补充资料，提交后卖方 Agent 会自动重新检查。"
		if question != "" {
			body += "\n" + question
		}
		if missing != "" {
			body += "\n待补文件：" + missing
		}
		return "「{service_name}」需要你补充资料", body
	}
	body := "Open the order to add the requested information. The seller's Agent will check it again after submission."
	if question != "" {
		body += "\n" + question
	}
	if missing != "" {
		body += "\nFiles needed: " + missing
	}
	return "“{service_name}” needs information from you", body
}

func (w *accountWatch) buyerInputNoticeCurrent(ctx context.Context, item dispatch.MainNotice) bool {
	order, err := w.readBuyerClarificationOrder(ctx, item.OrderID)
	return err == nil && w.commissionIdentityOK() == nil && order.State == "awaiting_seller" && order.Version == item.Version && validCommissionInputRequest(order.InputRequest) && order.InputRequest.Status == "waiting_buyer" && order.InputRequest.RequestID == item.InputRequestID
}

// Recheck before an OS retry: a delayed permission grant must not surface an
// already answered, superseded or closed request.
func (w *accountWatch) buyerMaterialDesktopCurrent(ctx context.Context, id string) (bool, error) {
	if !strings.HasPrefix(id, "inputs:") {
		return true, nil
	}
	parts := strings.Split(id, ":")
	if len(parts) != 5 || parts[1] != w.scope {
		return false, nil
	}
	version, err := strconv.ParseInt(parts[3], 10, 64)
	if err != nil || strconv.FormatInt(version, 10) != parts[3] {
		return false, nil
	}
	item := dispatch.BuyerInputMainNotice(w.scope, parts[2], version, parts[4])
	order, err := w.readBuyerClarificationOrder(ctx, item.OrderID)
	if err != nil {
		return false, err
	}
	return order.State == "awaiting_seller" && order.Version == item.Version && validCommissionInputRequest(order.InputRequest) && order.InputRequest.Status == "waiting_buyer" && order.InputRequest.RequestID == item.InputRequestID, nil
}
