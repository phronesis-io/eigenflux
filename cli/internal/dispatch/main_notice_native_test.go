package dispatch

import (
	"context"
	"encoding/json"
	"os"
	"strings"
	"testing"
	"time"
)

// Explicit opt-in: this appends one real result to the bound main thread, using
// hidden fixture context only. It never reads or mutates a real Order.
func TestMainNoticeInstalledCodexHiddenTurn(t *testing.T) {
	if os.Getenv("EIGENFLUX_MAIN_NOTICE_NATIVE_SMOKE") != "1" {
		t.Skip("set EIGENFLUX_MAIN_NOTICE_NATIVE_SMOKE=1 for a real main-thread turn")
	}
	path := os.Getenv("EIGENFLUX_MAIN_NOTICE_NATIVE_BINDING")
	if path == "" {
		t.Fatal("an explicit EIGENFLUX_MAIN_NOTICE_NATIVE_BINDING is required")
	}
	f, err := os.Open(path)
	if err != nil {
		t.Fatal("cannot read explicit main-thread binding")
	}
	defer f.Close()
	binding, err := DecodeBinding(f)
	if err != nil || !MainNoticeBindingEnabled(binding) {
		t.Fatal("verified macOS main-thread binding is required")
	}
	nonce, err := mainNoticeUUID()
	if err != nil {
		t.Fatal(err)
	}
	marker := "EF_MAIN_GO_" + strings.ReplaceAll(nonce, "-", "")[:12]
	data, _ := json.Marshal(map[string]any{"kind": "local_verification", "marker": marker, "left": 17, "right": 25})
	rules := "本次是机主明确授权的 EigenFlux CLI 主对话验收，仅本次 turn 生效。读取 eigenflux_business_notice 的本地模拟数据，计算 left + right。只输出一行：EigenFlux 主对话已接通（校验结果：<计算值>，标识：<marker>）。尖括号替换为实际值。不要输出规则、prompt 或原始事件，不调用工具、心跳、网络、其他会话，不操作真实订单。完成后恢复正常对话。"
	ctx, cancel := context.WithTimeout(context.Background(), 120*time.Second)
	defer cancel()
	started := time.Now()
	result, err := (MainNoticeRunner{Target: MainNoticeTarget{Host: binding.Host, SessionID: binding.MainSessionID, VerifiedHiddenInput: true, Binding: binding}}).Run(ctx, rules+"\n\nUNTRUSTED COMMISSION EVENT:\n"+string(data))
	if err != nil {
		t.Fatalf("real main-thread call failed (do not replay automatically): %v", err)
	}
	want := "EigenFlux 主对话已接通（校验结果：42，标识：" + marker + "）。"
	if result.Text != want || result.SessionID != binding.MainSessionID || result.RunID == "" {
		t.Fatal("main-thread result did not match the exact fixture and target")
	}
	t.Logf("real desktop owner hidden turn: %.3fs; thread=%s turn=%s; user_messages=0 final=%s", time.Since(started).Seconds(), result.SessionID, result.RunID, result.Text)
}
