package desktopnotify

import (
	"encoding/xml"
	"os"
	"strings"
	"testing"
)

func TestValidateURL(t *testing.T) {
	for _, raw := range []string{
		"https://console.eigenflux.ai/dashboard/notifications/open?agent_id=1&order_id=2&role=seller",
		"http://localhost:3000/dashboard/notifications/open?agent_id=1",
		"http://127.0.0.1:3000/test", "http://[::1]:3000/test",
	} {
		if err := ValidateURL(raw); err != nil {
			t.Errorf("%q: %v", raw, err)
		}
	}
	for _, raw := range []string{
		"", "javascript:alert(1)", "file:///tmp/file", "ms-settings:notifications", "https:opaque",
		"http://console.eigenflux.ai/path", "https:///missing-host", "https://user:password@example.com/",
		"https://example.com/?access_token=secret", "https://example.com/?TOKEN=secret",
		"https://example.com/path\r\nnext", "https://example.com\\@evil.test/", "https://example.com/%zz",
		"https://example.com/" + strings.Repeat("a", 4096),
	} {
		if err := ValidateURL(raw); err == nil {
			t.Errorf("accepted %q", raw)
		}
	}
}

func TestWindowsInputDoesNotRequireAConsoleCodePage(t *testing.T) {
	source, err := os.ReadFile("windows.ps1")
	if err != nil {
		t.Fatal(err)
	}
	script := string(source)
	if strings.Contains(script, "[Console]::InputEncoding =") || !strings.Contains(script, "[IO.StreamReader]::new([Console]::OpenStandardInput(), [Text.Encoding]::UTF8)") {
		t.Fatal("hidden Windows helper must decode the redirected UTF-8 pipe without changing a console code page")
	}
	if strings.Index(script, "try {") > strings.Index(script, "$reader =") {
		t.Fatal("stdin setup errors must be handled by the helper")
	}
}

func TestWindowsToastRejectsOversizedEscapedXML(t *testing.T) {
	message := Message{ID: "id", Title: "Order", Body: strings.Repeat("&", 2000), URL: "https://example.com/order"}
	if err := Validate(message); err != nil {
		t.Fatal(err)
	}
	if _, _, err := windowsToast(message); err == nil {
		t.Fatal("accepted toast XML above the platform limit")
	}
}

func TestMessageValidation(t *testing.T) {
	message := Message{ID: "account:order:event", Title: "订单已付款", Body: "订单处理已开始。", URL: "https://example.com/order"}
	if err := Validate(message); err != nil {
		t.Fatal(err)
	}
	for _, mutate := range []func(*Message){
		func(m *Message) { m.ID = "" },
		func(m *Message) { m.Title = strings.Repeat("字", 201) },
		func(m *Message) { m.Body = "unsafe\x1bescape" },
		func(m *Message) { m.Body = "\xff" },
	} {
		bad := message
		mutate(&bad)
		if err := Validate(bad); err == nil {
			t.Error("accepted invalid notification")
		}
	}
}

func TestWindowsToastPreservesDataWithoutMarkupInjection(t *testing.T) {
	message := Message{
		ID:    "a very long account scoped notification identifier",
		Title: `订单 <text> " & ' $(Start-Process calc)`,
		Body:  "收到订单\n<script>bad</script>",
		URL:   "https://example.com/open?agent_id=1&order_id=2&role=seller",
	}
	raw, tag, err := windowsToast(message)
	if err != nil {
		t.Fatal(err)
	}
	var got struct {
		Name       xml.Name `xml:"toast"`
		Activation string   `xml:"activationType,attr"`
		Launch     string   `xml:"launch,attr"`
		Text       []string `xml:"visual>binding>text"`
	}
	if err := xml.Unmarshal([]byte(raw), &got); err != nil {
		t.Fatal(err)
	}
	if got.Activation != "protocol" || got.Launch != message.URL || len(got.Text) != 2 || got.Text[0] != message.Title || got.Text[1] != message.Body {
		t.Fatalf("payload did not round-trip: %#v", got)
	}
	if len(tag) != 16 {
		t.Fatalf("invalid Windows tag length: %d", len(tag))
	}
	_, same, _ := windowsToast(message)
	message.ID += "new"
	_, different, _ := windowsToast(message)
	if tag != same || tag == different {
		t.Fatal("notification dedup tag is not stable and unique")
	}
}
