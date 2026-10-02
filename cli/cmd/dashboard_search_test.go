package cmd

import (
	"bytes"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

func TestDashboardSearchValidationAndLiteralQuery(t *testing.T) {
	for _, args := range [][]string{{"\xff"}, {""}, {"q", "--type", "other"}, {"q", "--limit", "0"}, {"q", "--cursor", "1"}, {"q", "--status", "draft"}, {"q", "--type", "order", "--cursor", "1x"}} {
		c := newDashboardSearchCommand()
		c.SetArgs(args)
		if err := c.Execute(); err == nil {
			t.Fatal(args)
		}
	}
	c := newDashboardSearchCommand()
	if err := c.ParseFlags([]string{"--type", "message", "--cursor", "9007199254740993"}); err != nil {
		t.Fatal(err)
	}
	p, err := dashboardSearchParams(c, " 合同_%!  Q ")
	if err != nil || p["q"] != "合同_%!  Q" || p["cursor"] != "9007199254740993" {
		t.Fatalf("%v %v", p, err)
	}
	command, args, err := rootCmd.Find([]string{"dashboard", "search", "hello"})
	if err != nil || !strings.HasPrefix(command.Use, "search") || len(args) != 1 {
		t.Fatalf("%v %v %v", command, args, err)
	}
	if dashboardCmd.RunE == nil {
		t.Fatal("login link behavior removed")
	}
}

func TestDashboardSearchHTTPContractAndPartialFailure(t *testing.T) {
	for _, failed := range []bool{false, true} {
		t.Run(map[bool]string{false: "success", true: "partial"}[failed], func(t *testing.T) {
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				if r.Method != "GET" || r.URL.Path != "/api/v2/dashboard/search" || r.URL.Query().Get("q") != "合同_%!  name" || r.URL.Query().Get("type") != "message" || r.Header.Get("Authorization") != "Bearer test-v2" {
					t.Errorf("unexpected request %s %s", r.Method, r.URL)
				}
				if failed {
					w.Write([]byte(`{"data":{"query":"q","groups":[{"type":"message","items":[],"error":"SEARCH_UNAVAILABLE"}]}}`))
					return
				}
				w.Write([]byte(`{"data":{"query":"q","groups":[{"type":"message","items":[{"id":"9007199254740993","title":"Found","preview":"合同_%!  name","url":"/dashboard/messages?conversation_id=1","status":"open"}]}]}}`))
			}))
			defer server.Close()
			runtimeTestConfig(t, server.URL, true)
			old := formatFlag
			formatFlag = "table"
			defer func() { formatFlag = old }()
			c := newDashboardSearchCommand()
			var out bytes.Buffer
			c.SetOut(&out)
			c.SetErr(&out)
			c.SetArgs([]string{"合同_%!  name", "--type", "message"})
			err := c.Execute()
			if (err != nil) != failed {
				t.Fatalf("error %v", err)
			}
			if !failed && !strings.Contains(out.String(), "9007199254740993") {
				t.Fatal(out.String())
			}
		})
	}
}
