package cmd

import (
	"strings"
	"testing"
)

func TestCommissionUnconfirmedCopyIdentifiesStageAndSafeNextStep(t *testing.T) {
	for _, tt := range []struct {
		code, zhTitle, enTitle, zhReason, enReason, zhAction, enAction string
	}{
		{"commission_intake_unconfirmed", "材料检查未完成", "Input inspection unconfirmed", "调用后台 Agent 检查材料时出错", "background Agent call for input inspection returned an error", "核对后继续", "check before continuing"},
		{"commission_fulfillment_unconfirmed", "交付执行结果待核对", "Fulfillment result needs checking", "调用后台 Agent 执行交付时出错", "background Agent call for fulfillment returned an error", "核对已有产出", "Check existing outputs"},
		{"commission_accept_unconfirmed", "接单结果待核对", "Order acceptance needs checking", "接单请求未取得确认", "acceptance request was not confirmed", "避免重复接单", "before attempting to accept it again"},
		{"commission_delivery_unconfirmed", "交付提交结果待核对", "Delivery submission needs checking", "交付提交未取得确认", "delivery submission was not confirmed", "避免重复提交", "before submitting again"},
		{"interrupted_execution", "处理被中断", "Processing interrupted", "本地处理被中断", "Local processing was interrupted", "避免重复执行", "before repeating any work"},
	} {
		t.Run(tt.code, func(t *testing.T) {
			for _, language := range []string{"zh", "en", "fr", ""} {
				title, body := desktopCommissionBlockerText(language, "unknown", tt.code)
				wantTitle, reason, action, deadline := tt.enTitle, tt.enReason, tt.enAction, "Order deadlines still apply."
				if language == "zh" {
					wantTitle, reason, action, deadline = tt.zhTitle, tt.zhReason, tt.zhAction, "订单时限仍以订单页为准。"
				}
				for _, want := range []string{reason, action, deadline} {
					if !strings.Contains(body, want) {
						t.Fatalf("%s body omits %q: %s", language, want, body)
					}
				}
				if !strings.Contains(title, wantTitle) || !strings.Contains(title, desktopServiceNamePlaceholder) {
					t.Fatalf("%s title loses stage or service: %s", language, title)
				}
				for _, unproven := range []string{"timeout", "timed out", "restart", "超时", "重启", "订单失败"} {
					if strings.Contains(title+body, unproven) {
						t.Fatalf("%s inferred an unproven cause %q: %s / %s", language, unproven, title, body)
					}
				}
			}
		})
	}
}

func TestCommissionUnrecognizedCodeNeverAppearsInNotification(t *testing.T) {
	for _, language := range []string{"zh", "en"} {
		for _, status := range []string{"unknown", "failed", "needs_user"} {
			title, body := desktopCommissionBlockerText(language, status, "stderr /private/path secret-token")
			if strings.Contains(title+body, "stderr") || strings.Contains(title+body, "private") || strings.Contains(title+body, "secret-token") {
				t.Fatalf("provider data leaked: %s / %s", title, body)
			}
		}
	}
}
