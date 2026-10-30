package cmd

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"cli.eigenflux.ai/internal/auth"
	"cli.eigenflux.ai/internal/client"
	"cli.eigenflux.ai/internal/config"
	"cli.eigenflux.ai/internal/dispatch"
	watchstate "cli.eigenflux.ai/internal/watch"
	"github.com/gorilla/websocket"
)

func commissionTestNotification(id, orderID int64, recipient string) json.RawMessage {
	return json.RawMessage(fmt.Sprintf(`{"notification_id":"%d","source_type":"commission_order","type":"order.state.changed.v1","created_at":1700000000000,"payload":{"order_id":"%d","order_version":3,"recipient_agent_id":"%s","recipient_role":"seller","state":"in_progress","to_state":"in_progress","snapshot_id":"9007199254740995","occurred_at":1700000000000}}`, id, orderID, recipient))
}

func commissionTestPage(notifications ...json.RawMessage) json.RawMessage {
	data, _ := json.Marshal(commissionNotificationPage{Notifications: notifications})
	return data
}

func newCommissionWatch(t *testing.T, endpoint string, events ...string) *accountWatch {
	t.Helper()
	cfg, name := runtimeTestConfig(t, endpoint, true)
	if _, _, _, err := auth.LoadOrCreateIdentity(name); err != nil {
		t.Fatal(err)
	}
	if err := cfg.UpdateServer(name, endpoint, strings.Replace(endpoint, "http:", "ws:", 1)); err != nil {
		t.Fatal(err)
	}
	credentials, err := auth.LoadV2Credentials(name)
	if err != nil {
		t.Fatal(err)
	}
	credentials.AgentID, credentials.PrincipalID = "42", "principal-42"
	if err := auth.SaveV2Credentials(name, credentials); err != nil {
		t.Fatal(err)
	}
	srv, _ := cfg.GetActive(name)
	home, err := watchstate.CanonicalHome(config.HomeDir())
	if err != nil {
		t.Fatal(err)
	}
	executable, _ := os.Executable()
	binding := dispatch.Binding{
		Version: 1, Home: home, Server: name, Endpoint: endpoint,
		AgentID: credentials.AgentID, PrincipalID: credentials.PrincipalID,
		Scope:    watchstate.Scope(home, name, credentials.AgentID, credentials.PrincipalID),
		Revision: strings.Repeat("c", 32), Events: events, Mode: "command", Host: "custom",
		Command: []string{executable}, WorkDir: home, SkillsDir: home, TimeoutSeconds: 5,
	}
	if err := dispatch.WriteJSON(dispatch.BindingPath(home, name), binding); err != nil {
		t.Fatal(err)
	}
	journal, err := dispatch.OpenJournal(binding)
	if err != nil {
		t.Fatal(err)
	}
	return &accountWatch{home: home, server: *srv, identity: *credentials, scope: binding.Scope,
		binding: &binding, journal: journal, wake: make(chan struct{}, 1),
		emit: func(string, interface{}) error { return nil }}
}

func TestWatchCommissionPersistsWholePageBeforeACKAndDeduplicates(t *testing.T) {
	var watch *accountWatch
	var ackCalls atomic.Int32
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/api/v2/notifications/ack" {
			t.Errorf("unexpected path %s", r.URL.Path)
			http.NotFound(w, r)
			return
		}
		ackCalls.Add(1)
		reopened, err := dispatch.OpenJournal(*watch.binding)
		if err != nil || len(reopened.Snapshot()) != 2 {
			t.Errorf("ACK before both jobs persisted: %v", err)
		}
		var request struct {
			Notifications []map[string]string `json:"notifications"`
		}
		if err := json.NewDecoder(r.Body).Decode(&request); err != nil || len(request.Notifications) != 2 {
			t.Errorf("invalid ACK batch: %+v, %v", request, err)
		}
		for _, item := range request.Notifications {
			if item["source_type"] != "commission_order" || item["notification_id"] == "99" {
				t.Errorf("ACKed unowned source: %+v", item)
			}
		}
		_, _ = io.WriteString(w, `{"code":0,"data":{"acknowledged":2}}`)
	}))
	defer server.Close()
	watch = newCommissionWatch(t, server.URL, "commission_order")
	transports := []string{}
	watch.emit = func(kind string, value interface{}) error {
		if kind != "notification_push" {
			t.Fatalf("unexpected event %s", kind)
		}
		data := value.(map[string]interface{})
		transports = append(transports, data["transport"].(string))
		if _, err := time.Parse(time.RFC3339Nano, data["received_at"].(string)); err != nil {
			t.Fatal(err)
		}
		if len(data["notifications"].([]json.RawMessage)) != 3 {
			t.Fatal("non-commission notification was hidden from event output")
		}
		return nil
	}
	page := commissionTestPage(commissionTestNotification(81, 901, "42"), commissionTestNotification(82, 902, "42"), json.RawMessage(`{"notification_id":"99","source_type":"system"}`))
	for _, transport := range []string{"socket", "http_poll"} {
		if err := watch.deliverCommissionNotifications(context.Background(), page, transport); err != nil {
			t.Fatal(err)
		}
	}
	if ackCalls.Load() != 2 || strings.Join(transports, ",") != "socket,http_poll" {
		t.Fatalf("ACK/transport mismatch: %d %v", ackCalls.Load(), transports)
	}
	if jobs := watch.journal.Snapshot(); len(jobs) != 2 || jobs[0].Status != "pending" || jobs[1].Status != "pending" {
		t.Fatalf("expected one durable pending job per order version: %+v", jobs)
	}
	if _, ok, err := watch.journal.Next(); ok || err != nil {
		t.Fatalf("generic worker claimed commission work: %v %v", ok, err)
	}
}

func TestWatchCommissionFailureDoesNotACK(t *testing.T) {
	for _, failure := range []string{"wrong_recipient", "identity_changed", "invalid_later_item", "persistence"} {
		t.Run(failure, func(t *testing.T) {
			var calls atomic.Int32
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				calls.Add(1)
				_, _ = io.WriteString(w, `{"code":0,"data":{}}`)
			}))
			defer server.Close()
			watch := newCommissionWatch(t, server.URL, "commission_order")
			page := commissionTestPage(commissionTestNotification(81, 901, "42"))
			switch failure {
			case "wrong_recipient":
				page = commissionTestPage(commissionTestNotification(81, 901, "43"))
			case "identity_changed":
				changed := watch.identity
				changed.AgentID = "43"
				if err := auth.SaveV2Credentials(watch.server.Name, &changed); err != nil {
					t.Fatal(err)
				}
			case "invalid_later_item":
				page = commissionTestPage(commissionTestNotification(81, 901, "42"), commissionTestNotification(82, 902, "43"))
			case "persistence":
				if err := watch.journal.AddCommissionNotification(commissionTestNotification(80, 900, "42")); err != nil {
					t.Fatal(err)
				}
				files, _ := filepath.Glob(filepath.Join(watch.home, "watch", "dispatch-*.json"))
				if len(files) != 1 {
					t.Fatalf("unexpected journal files: %v", files)
				}
				if err := os.Remove(files[0]); err != nil {
					t.Fatal(err)
				}
				if err := os.Mkdir(files[0], 0700); err != nil {
					t.Fatal(err)
				}
			}
			err := watch.deliverCommissionNotifications(context.Background(), page, "socket")
			if err == nil || calls.Load() != 0 {
				t.Fatalf("invalid intake was acknowledged: err=%v calls=%d", err, calls.Load())
			}
			if failure == "persistence" && !errors.Is(err, errWatchDispatchJournal) {
				t.Fatalf("journal failure must stop reception: %v", err)
			}
		})
	}
}

func TestWatchCommissionRejectsMalformedPagesWithoutACK(t *testing.T) {
	var requests atomic.Int32
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		requests.Add(1)
		_, _ = io.WriteString(w, `{"code":0,"data":{}}`)
	}))
	defer server.Close()
	watch := newCommissionWatch(t, server.URL, "commission_order")
	for _, raw := range []string{"null", "{}", `{"notifications":null}`, `{"notifications":{}}`} {
		if err := watch.deliverCommissionNotifications(context.Background(), json.RawMessage(raw), "socket"); err == nil {
			t.Fatalf("malformed page accepted: %s", raw)
		}
	}
	if requests.Load() != 0 {
		t.Fatal("invalid page caused HTTP side effects")
	}
}

func TestWatchCommissionSocketBatchACKLimit(t *testing.T) {
	var watch *accountWatch
	var requests, acked atomic.Int32
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		requests.Add(1)
		reopened, err := dispatch.OpenJournal(*watch.binding)
		if err != nil || len(reopened.Snapshot()) != 100 {
			t.Errorf("whole Socket batch must persist before first ACK: %v", err)
		}
		var request struct {
			Notifications []map[string]string `json:"notifications"`
		}
		if err := json.NewDecoder(r.Body).Decode(&request); err != nil || len(request.Notifications) != 50 {
			t.Errorf("ACK must respect server maximum 50: %+v %v", request, err)
		}
		acked.Add(int32(len(request.Notifications)))
		_, _ = io.WriteString(w, `{"code":0,"data":{}}`)
	}))
	defer server.Close()
	watch = newCommissionWatch(t, server.URL, "commission_order")
	notifications := make([]json.RawMessage, 100)
	for index := range notifications {
		notifications[index] = commissionTestNotification(int64(index)+1, int64(index)+901, "42")
	}
	if err := watch.deliverCommissionNotifications(context.Background(), commissionTestPage(notifications...), "socket"); err != nil {
		t.Fatal(err)
	}
	if requests.Load() != 2 || acked.Load() != 100 {
		t.Fatalf("incomplete ACK batches: %d requests, %d rows", requests.Load(), acked.Load())
	}
}

func TestWatchCommissionPollIdentitySwitchAfterFetchDoesNotACK(t *testing.T) {
	var watch *accountWatch
	var ackCalls atomic.Int32
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/api/v2/notifications/ack" {
			ackCalls.Add(1)
		} else {
			changed := watch.identity
			changed.AgentID = "43"
			if err := auth.SaveV2Credentials(watch.server.Name, &changed); err != nil {
				t.Error(err)
			}
		}
		_ = json.NewEncoder(w).Encode(map[string]interface{}{"code": 0, "data": commissionTestPage(commissionTestNotification(81, 901, "42"))})
	}))
	defer server.Close()
	watch = newCommissionWatch(t, server.URL, "commission_order")
	if err := watch.pollCommissionNotifications(context.Background()); !errors.Is(err, errWatchIdentity) {
		t.Fatalf("lost pinned identity: %v", err)
	}
	if ackCalls.Load() != 0 || len(watch.journal.Snapshot()) != 0 {
		t.Fatal("old account notification persisted or ACKed after account switch")
	}
}

func TestWatchCommissionPollPaginationAndBound(t *testing.T) {
	for _, mode := range []string{"two_pages", "cursor_cycle", "page_bound"} {
		t.Run(mode, func(t *testing.T) {
			var requests, acks atomic.Int32
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				if r.URL.Path == "/api/v2/notifications/ack" {
					acks.Add(1)
					_, _ = io.WriteString(w, `{"code":0,"data":{}}`)
					return
				}
				request := requests.Add(1)
				if r.URL.Query().Get("limit") != "50" {
					t.Error("missing bounded HTTP page limit")
				}
				if request > 1 && mode != "cursor_cycle" && r.URL.Query().Get("cursor") != strconv.Itoa(int(request)-1) {
					t.Errorf("wrong cursor on page %d: %s", request, r.URL.RawQuery)
				}
				page := commissionNotificationPage{HasMore: true, NextCursor: strconv.Itoa(int(request)), Notifications: []json.RawMessage{}}
				switch mode {
				case "two_pages":
					page.Notifications = []json.RawMessage{commissionTestNotification(80+int64(request), 900+int64(request), "42")}
					page.HasMore = request < 2
					if request > 1 && acks.Load() != 1 {
						t.Error("advanced cursor before prior page ACK")
					}
				case "cursor_cycle":
					page.NextCursor = "one"
				}
				_ = json.NewEncoder(w).Encode(map[string]interface{}{"code": 0, "data": page})
			}))
			defer server.Close()
			watch := newCommissionWatch(t, server.URL, "commission_order")
			err := watch.pollCommissionNotifications(context.Background())
			if mode == "cursor_cycle" {
				if err == nil || requests.Load() != 2 {
					t.Fatalf("unbounded repeated cursor: %v %d", err, requests.Load())
				}
			} else if err != nil {
				t.Fatal(err)
			}
			if mode == "two_pages" && (requests.Load() != 2 || acks.Load() != 2 || len(watch.journal.Snapshot()) != 2) {
				t.Fatal("did not durably process both pages")
			}
			if mode == "page_bound" && requests.Load() != commissionPollPages {
				t.Fatalf("HTTP pass exceeded bounded work: %d", requests.Load())
			}
		})
	}
}

func TestWatchCommissionSubscriptionDoesNotConsumeOtherSources(t *testing.T) {
	var requests atomic.Int32
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		requests.Add(1)
		_, _ = io.WriteString(w, `{"code":0,"data":{"notifications":[],"has_more":false}}`)
	}))
	defer server.Close()
	watch := newCommissionWatch(t, server.URL, "commission_order")
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	for _, call := range []func(context.Context) error{watch.pmLoop, watch.pmFallbackLoop, watch.pollPM} {
		if err := call(ctx); err != nil {
			t.Fatal(err)
		}
	}
	if requests.Load() != 0 {
		t.Fatal("commission-only binding opened a PM consumer")
	}
	watch.runtimeOnline.Store(true)
	watch.reportReady()
	if !watch.ready.Load() || watch.wsOnline.Load() {
		t.Fatal("HTTP-only binding must be ready without claiming Socket connectivity")
	}
	watch.binding.Events = []string{"pm_push"}
	for _, call := range []func(context.Context) error{watch.commissionFallbackLoop, watch.pollCommissionNotifications} {
		if err := call(ctx); err != nil {
			t.Fatal(err)
		}
	}
	if err := watch.deliverCommissionNotifications(ctx, commissionTestPage(commissionTestNotification(81, 901, "42")), "socket"); err != nil {
		t.Fatal(err)
	}
	if requests.Load() != 0 || len(watch.journal.Snapshot()) != 0 {
		t.Fatal("unsubscribed commission notifications were consumed")
	}
}

func TestWatchCommissionSocketDoesNotReplacePMCursor(t *testing.T) {
	var sockets atomic.Int32
	resumed := make(chan string, 1)
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/api/v2/notifications/ack" {
			_, _ = io.WriteString(w, `{"code":0,"data":{}}`)
			return
		}
		if r.URL.Path != "/api/v2/agent/events/ws" {
			http.NotFound(w, r)
			return
		}
		conn, err := (&websocket.Upgrader{}).Upgrade(w, r, nil)
		if err != nil {
			return
		}
		defer conn.Close()
		if sockets.Add(1) > 1 {
			resumed <- r.URL.Query().Get("cursor")
			_, _, _ = conn.ReadMessage()
			return
		}
		_ = conn.WriteJSON(map[string]interface{}{"type": "pm_push", "data": json.RawMessage(`{"messages":[],"next_cursor":"777"}`)})
		page := commissionNotificationPage{Notifications: []json.RawMessage{commissionTestNotification(81, 901, "42")}, HasMore: true, NextCursor: "notification-opaque-cursor"}
		_ = conn.WriteJSON(map[string]interface{}{"type": "notification_push", "data": page})
	}))
	defer server.Close()
	watch := newCommissionWatch(t, server.URL, "pm_push", "commission_order")
	ctx, cancel := context.WithTimeout(context.Background(), 9*time.Second)
	defer cancel()
	done := make(chan error, 1)
	go func() { done <- watch.pmLoop(ctx) }()
	select {
	case cursor := <-resumed:
		if cursor != "777" {
			t.Fatalf("notification pagination replaced PM resume cursor: %q", cursor)
		}
	case <-ctx.Done():
		t.Fatal("Socket did not reconnect")
	}
	cancel()
	if err := <-done; !errors.Is(err, context.Canceled) {
		t.Fatalf("unexpected loop result: %v", err)
	}
	if len(watch.journal.Snapshot()) != 1 {
		t.Fatal("Socket commission notification not persisted")
	}
}

func TestDrainOrderNotificationsRespectsDispatchOwnership(t *testing.T) {
	var calls atomic.Int32
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		calls.Add(1)
		_, _ = io.WriteString(w, `{"code":0,"data":{"notifications":[],"has_more":false}}`)
	}))
	defer server.Close()
	watch := newCommissionWatch(t, server.URL, "commission_order")
	api := client.New(server.URL+"/api/v2", watch.identity.AccessToken, "test", client.Meta{})
	if err := drainOrderNotifications(api, "json", "en", io.Discard); err != nil {
		t.Fatal(err)
	}
	if calls.Load() != 0 {
		t.Fatal("legacy stream drained notifications owned by watch")
	}
	watch.binding.Events = []string{"pm_push"}
	if err := dispatch.WriteJSON(dispatch.BindingPath(watch.home, watch.server.Name), watch.binding); err != nil {
		t.Fatal(err)
	}
	if err := drainOrderNotifications(api, "json", "en", io.Discard); err != nil {
		t.Fatal(err)
	}
	if calls.Load() != 1 {
		t.Fatal("PM-only binding suppressed unrelated commission consumer")
	}
}

func TestDrainOrderNotificationsStopsAfterOwnershipOrIdentityChangesDuringFetch(t *testing.T) {
	for _, mode := range []string{"handoff", "identity"} {
		t.Run(mode, func(t *testing.T) {
			var watch *accountWatch
			var acks atomic.Int32
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				if r.URL.Path == "/api/v2/notifications/ack" {
					acks.Add(1)
					_, _ = io.WriteString(w, `{"code":0,"data":{}}`)
					return
				}
				err := auth.WithV2CredentialsLock(watch.server.Name, time.Second, func() error {
					if mode == "handoff" {
						binding := *watch.binding
						binding.Events = []string{"commission_order"}
						return dispatch.WriteJSON(dispatch.BindingPath(binding.Home, binding.Server), binding)
					}
					changed := watch.identity
					changed.AgentID, changed.PrincipalID = "43", "principal-43"
					return auth.SaveV2Credentials(watch.server.Name, &changed)
				})
				if err != nil {
					t.Error(err)
				}
				_ = json.NewEncoder(w).Encode(map[string]interface{}{"code": 0, "data": commissionTestPage(commissionTestNotification(81, 901, "42"))})
			}))
			defer server.Close()
			watch = newCommissionWatch(t, server.URL, "pm_push")
			api := client.New(server.URL+"/api/v2", watch.identity.AccessToken, "test", client.Meta{})
			var output strings.Builder
			err := drainOrderNotifications(api, "json", "en", &output)
			if mode == "identity" && !errors.Is(err, errWatchIdentity) {
				t.Fatalf("identity switch not rejected: %v", err)
			}
			if mode == "handoff" && err != nil {
				t.Fatal(err)
			}
			if acks.Load() != 0 || output.Len() != 0 {
				t.Fatal("legacy stream rendered/ACKed after ownership or identity changed")
			}
		})
	}
}

func TestDrainOrderNotificationsPinsBothRequestsToCurrentIdentity(t *testing.T) {
	var requests atomic.Int32
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		requests.Add(1)
		if got := r.Header.Get("Authorization"); got != "Bearer test-v2" {
			t.Errorf("request reused a prior account token: %q", got)
		}
		data := json.RawMessage(`{}`)
		if r.URL.Path == "/api/v2/notifications/pending" {
			data = commissionTestPage(commissionTestNotification(81, 901, "42"))
		}
		_ = json.NewEncoder(w).Encode(map[string]interface{}{"code": 0, "data": data})
	}))
	defer server.Close()
	newCommissionWatch(t, server.URL, "pm_push")
	api := client.New(server.URL+"/api/v2", "previous-account-token", "test", client.Meta{})
	api.OnUnauthorized = func() (string, error) { t.Fatal("legacy drain must not refresh under credential lock"); return "", nil }
	if err := drainOrderNotifications(api, "json", "en", io.Discard); err != nil {
		t.Fatal(err)
	}
	if requests.Load() != 2 || api.Token != "previous-account-token" || api.OnUnauthorized == nil {
		t.Fatal("legacy drain failed to isolate request credentials")
	}
	api.BaseURL = server.URL + "/wrong-endpoint"
	if err := drainOrderNotifications(api, "json", "en", io.Discard); !errors.Is(err, errWatchConfiguration) {
		t.Fatalf("sent credentials to unbound endpoint: %v", err)
	}
	if requests.Load() != 2 {
		t.Fatal("endpoint mismatch caused a request")
	}
}
