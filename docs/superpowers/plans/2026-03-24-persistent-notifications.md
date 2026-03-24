# Persistent System Notifications Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Make `type=system` notifications persistent (returned every feed refresh while active), while `type=announcement` retains existing one-time delivery behavior.

**Architecture:** Leverage the existing `type` field on `system_notifications` to control delivery behavior. In `listPendingSystemNotifications`, split candidates by type — `system` type skips the delivery check, `announcement` type keeps the existing delivery-gated flow. No schema, IDL, or migration changes.

**Tech Stack:** Go, Redis, PostgreSQL, React (Ant Design + Refine)

---

## File Structure

| File | Action | Responsibility |
|------|--------|---------------|
| `rpc/notification/dal/types.go` | Modify | Add `TypeSystem`, `TypeAnnouncement` constants |
| `rpc/notification/handler.go` | Modify | Split candidates by type in `listPendingSystemNotifications` |
| `console/webapp/src/pages/system-notifications/list.tsx` | Modify | Type field → Select dropdown with descriptive labels, default `announcement` |
| `tests/notify/system_notify_test.go` | Modify | Add persistent notification test + fix existing tests to use correct type values |

---

### Task 1: Add type constants to notification DAL

**Files:**
- Modify: `rpc/notification/dal/types.go:3-12`

- [ ] **Step 1: Add type constants**

Add `TypeSystem` and `TypeAnnouncement` after the existing constants block in `rpc/notification/dal/types.go`:

```go
const (
	SourceTypeMilestone = "milestone"
	SourceTypeSystem    = "system"

	StatusDraft   int16 = 0
	StatusActive  int16 = 1
	StatusOffline int16 = 2

	AudienceTypeBroadcast = "broadcast"

	TypeSystem       = "system"
	TypeAnnouncement = "announcement"
)
```

- [ ] **Step 2: Verify build**

Run: `cd /Users/phronex/git/phro-2026/agent_network/agent_network_server && go build ./rpc/notification/...`
Expected: Build succeeds with no errors.

- [ ] **Step 3: Commit**

```bash
git add rpc/notification/dal/types.go
git commit -m "feat(notification): add TypeSystem and TypeAnnouncement constants"
```

---

### Task 2: Update listPendingSystemNotifications to skip delivery check for persistent notifications

**Files:**
- Modify: `rpc/notification/handler.go:154-206`

- [ ] **Step 1: Rewrite listPendingSystemNotifications**

Replace the current `listPendingSystemNotifications` method (lines 154-206) with logic that splits candidates by type:

```go
// listPendingSystemNotifications returns system notifications pending for the agent.
// type=system (persistent): always returned while IsActive, delivery check skipped.
// type=announcement (one-time): only returned if not yet delivered.
func (s *NotificationServiceImpl) listPendingSystemNotifications(ctx context.Context, agentID int64) ([]pendingSystem, error) {
	active, err := s.activeStore.List(ctx)
	if err != nil {
		return nil, err
	}
	if len(active) == 0 {
		return nil, nil
	}

	nowMS := time.Now().UnixMilli()

	var persistent []dal.SystemNotification
	var oneTime []dal.SystemNotification
	for i := range active {
		if !active[i].IsActive(nowMS) {
			continue
		}
		if active[i].Type == dal.TypeSystem {
			persistent = append(persistent, active[i])
		} else {
			oneTime = append(oneTime, active[i])
		}
	}

	// Delivery check only for one-time (announcement) notifications
	var delivered map[int64]bool
	if len(oneTime) > 0 {
		sourceIDs := make([]int64, len(oneTime))
		for i, c := range oneTime {
			sourceIDs[i] = c.NotificationID
		}
		delivered, err = dal.AreDelivered(ctx, s.db, dal.SourceTypeSystem, sourceIDs, agentID)
		if err != nil {
			return nil, err
		}
	}

	var pending []pendingSystem

	// Persistent notifications: always included
	for _, c := range persistent {
		pending = append(pending, pendingSystem{
			NotificationID: c.NotificationID,
			Type:           c.Type,
			Content:        c.Content,
			CreatedAt:      c.CreatedAt,
		})
	}

	// One-time notifications: only if not yet delivered
	for _, c := range oneTime {
		if delivered[c.NotificationID] {
			continue
		}
		pending = append(pending, pendingSystem{
			NotificationID: c.NotificationID,
			Type:           c.Type,
			Content:        c.Content,
			CreatedAt:      c.CreatedAt,
		})
	}

	sort.Slice(pending, func(i, j int) bool {
		if pending[i].CreatedAt != pending[j].CreatedAt {
			return pending[i].CreatedAt < pending[j].CreatedAt
		}
		return pending[i].NotificationID < pending[j].NotificationID
	})

	return pending, nil
}
```

- [ ] **Step 2: Verify build**

Run: `cd /Users/phronex/git/phro-2026/agent_network/agent_network_server && go build ./rpc/notification/...`
Expected: Build succeeds with no errors.

- [ ] **Step 3: Commit**

```bash
git add rpc/notification/handler.go
git commit -m "feat(notification): persistent type=system notifications skip delivery check"
```

---

### Task 3: Update console webapp type field to Select dropdown

**Files:**
- Modify: `console/webapp/src/pages/system-notifications/list.tsx`

- [ ] **Step 1: Update create form default type**

In `list.tsx` line 291, change the default `type` from `"system"` to `"announcement"`:

```tsx
createForm.setFieldsValue({
  type: "announcement",
  content: "",
  status: 1,
});
```

- [ ] **Step 2: Replace type Input with Select in create modal**

In `list.tsx` lines 348-349, replace the `<Input>` for type with a `<Select>`:

```tsx
<Form.Item name="type" label="Type" rules={[{ required: true }]}>
  <Select
    options={[
      { label: "Announcement (one-time delivery)", value: "announcement" },
      { label: "System (persistent while active)", value: "system" },
    ]}
  />
</Form.Item>
```

- [ ] **Step 3: Replace type Input with Select in edit modal**

In `list.tsx` lines 381-383, replace the `<Input>` for type with the same `<Select>`:

```tsx
<Form.Item name="type" label="Type" rules={[{ required: true }]}>
  <Select
    options={[
      { label: "Announcement (one-time delivery)", value: "announcement" },
      { label: "System (persistent while active)", value: "system" },
    ]}
  />
</Form.Item>
```

- [ ] **Step 4: Verify frontend build**

Run: `cd /Users/phronex/git/phro-2026/agent_network/agent_network_server/console/webapp && npx tsc --noEmit`
Expected: No type errors.

- [ ] **Step 5: Commit**

```bash
git add console/webapp/src/pages/system-notifications/list.tsx
git commit -m "feat(console): type field as Select dropdown with descriptive labels"
```

---

### Task 4: Fix existing tests and add persistent notification test cases

**Files:**
- Modify: `tests/notify/system_notify_test.go`

- [ ] **Step 1: Fix TestSystemNotificationDeliveryAndDedup — change type to announcement**

This test verifies one-time delivery dedup. Change `type` from `"system"` to `"announcement"` on line 358:

```go
created := createSystemNotification(t, "announcement", "Platform maintenance at 02:00 UTC", 1, 0, 0)
```

Also update the type assertion on line 370-372:

```go
if notificationsA[0]["type"] != "announcement" {
    t.Fatalf("expected type=announcement, got %v", notificationsA[0]["type"])
}
```

- [ ] **Step 2: Fix TestSystemNotificationUpdateContent — change type to announcement**

This test verifies that agent A doesn't see updated content after ack (one-time behavior). Change `type` from `"system"` to `"announcement"` on line 530:

```go
created := createSystemNotification(t, "announcement", "Content v1", 1, 0, 0)
```

- [ ] **Step 3: Fix TestSystemNotificationDeliveryUniqueness — change type to announcement**

This test verifies delivery uniqueness for one-time notifications. Change `type` from `"system"` to `"announcement"` on line 615:

```go
created := createSystemNotification(t, "announcement", "Uniqueness test", 1, 0, 0)
```

- [ ] **Step 4: Fix TestSystemNotificationMultipleActive — expect persistent notification to survive ack**

The test creates one `type=system` and one `type=announcement` notification, then expects both to disappear after ack. With the new behavior, `type=system` persists. Replace lines 596-601:

```go
t.Log("=== Refresh again → only system (persistent) remains ===")
feed2 := testutil.WaitForFeedNotifications(t, token, 1, 20*time.Second)
notifications2 := feedNotifications(t, feed2)
if len(notifications2) != 1 {
    t.Fatalf("expected 1 persistent notification after ack, got %d", len(notifications2))
}
if notifications2[0]["type"] != "system" {
    t.Fatalf("expected persistent notification type=system, got %v", notifications2[0]["type"])
}
```

- [ ] **Step 2: Add TestPersistentSystemNotification test**

Append a new test function after `TestSystemNotificationDeliveryUniqueness`:

```go
// ---------------------------------------------------------------------------
// Tests: Persistent (type=system) notification survives ack
// ---------------------------------------------------------------------------

func TestPersistentSystemNotification(t *testing.T) {
	testutil.WaitForAPI(t)
	cleanNotifyTestData(t)

	agent := testutil.RegisterAgent(t, "sysnotify_persist@test.com", "SysNotifyPersist", "test")
	token := agent["token"].(string)
	agentID := testutil.MustID(t, agent["agent_id"], "agent_id")

	t.Log("=== Create persistent (type=system) notification ===")
	created := createSystemNotification(t, "system", "Persistent notice", 1, 0, 0)
	notifID := testutil.MustID(t, created.NotificationID, "notification_id")

	t.Log("=== First refresh → gets notification ===")
	feed1 := testutil.WaitForFeedNotifications(t, token, 1, 20*time.Second)
	n1 := feedNotifications(t, feed1)
	if len(n1) != 1 {
		t.Fatalf("expected 1 notification, got %d", len(n1))
	}
	if n1[0]["content"] != "Persistent notice" {
		t.Fatalf("unexpected content: %v", n1[0]["content"])
	}

	t.Log("=== Wait for delivery record (written for audit) ===")
	waitForNotificationDelivery(t, "system", notifID, agentID, 15*time.Second)

	t.Log("=== Second refresh → still gets notification (persistent) ===")
	feed2 := testutil.WaitForFeedNotifications(t, token, 1, 20*time.Second)
	n2 := feedNotifications(t, feed2)
	if len(n2) != 1 {
		t.Fatalf("expected 1 notification on second refresh, got %d", len(n2))
	}
	if n2[0]["content"] != "Persistent notice" {
		t.Fatalf("unexpected content on second refresh: %v", n2[0]["content"])
	}

	t.Log("=== Third refresh → still there ===")
	feed3 := testutil.WaitForFeedNotifications(t, token, 1, 20*time.Second)
	n3 := feedNotifications(t, feed3)
	if len(n3) != 1 {
		t.Fatalf("expected 1 notification on third refresh, got %d", len(n3))
	}

	t.Log("=== Offline the notification → stops appearing ===")
	offlineSystemNotification(t, created.NotificationID)
	time.Sleep(1 * time.Second)

	feed4 := testutil.FetchFeedRefresh(t, token, 20)
	if n := feedNotifications(t, feed4); len(n) > 0 {
		t.Fatalf("expected 0 notifications after offline, got %d", len(n))
	}
}
```

- [ ] **Step 3: Add TestAnnouncementOneTimeDelivery test**

Append a test that explicitly verifies `type=announcement` retains one-time behavior:

```go
// ---------------------------------------------------------------------------
// Tests: Announcement (type=announcement) is one-time delivery
// ---------------------------------------------------------------------------

func TestAnnouncementOneTimeDelivery(t *testing.T) {
	testutil.WaitForAPI(t)
	cleanNotifyTestData(t)

	agent := testutil.RegisterAgent(t, "sysnotify_announce@test.com", "SysNotifyAnnounce", "test")
	token := agent["token"].(string)
	agentID := testutil.MustID(t, agent["agent_id"], "agent_id")

	t.Log("=== Create announcement notification ===")
	created := createSystemNotification(t, "announcement", "One-time notice", 1, 0, 0)
	notifID := testutil.MustID(t, created.NotificationID, "notification_id")

	t.Log("=== First refresh → gets notification ===")
	feed1 := testutil.WaitForFeedNotifications(t, token, 1, 20*time.Second)
	n1 := feedNotifications(t, feed1)
	if len(n1) != 1 {
		t.Fatalf("expected 1 notification, got %d", len(n1))
	}
	if n1[0]["content"] != "One-time notice" {
		t.Fatalf("unexpected content: %v", n1[0]["content"])
	}

	t.Log("=== Wait for delivery record ===")
	waitForNotificationDelivery(t, "system", notifID, agentID, 15*time.Second)

	t.Log("=== Second refresh → no notification (one-time, already delivered) ===")
	feed2 := testutil.FetchFeedRefresh(t, token, 20)
	if n := feedNotifications(t, feed2); len(n) > 0 {
		t.Fatalf("expected 0 notifications after ack for announcement, got %d", len(n))
	}
}
```

- [ ] **Step 4: Verify build**

Run: `cd /Users/phronex/git/phro-2026/agent_network/agent_network_server && go build ./tests/notify/...`
Expected: Build succeeds.

- [ ] **Step 5: Commit**

```bash
git add tests/notify/system_notify_test.go
git commit -m "test(notification): add persistent and announcement delivery tests"
```

---

### Task 5: Build all services and run tests

- [ ] **Step 1: Build all Go services**

Run: `cd /Users/phronex/git/phro-2026/agent_network/agent_network_server && bash scripts/common/build.sh`
Expected: All services build successfully.

- [ ] **Step 2: Build console**

Run: `cd /Users/phronex/git/phro-2026/agent_network/agent_network_server && bash console/console_api/scripts/build.sh`
Expected: Console builds successfully.

- [ ] **Step 3: Start all services**

Run: `bash scripts/local/start_local.sh`
Expected: All services start.

- [ ] **Step 4: Run notification tests**

Run: `cd /Users/phronex/git/phro-2026/agent_network/agent_network_server && go test -v ./tests/notify/ -run TestPersistentSystemNotification`
Expected: PASS

Run: `cd /Users/phronex/git/phro-2026/agent_network/agent_network_server && go test -v ./tests/notify/ -run TestAnnouncementOneTimeDelivery`
Expected: PASS

Run: `cd /Users/phronex/git/phro-2026/agent_network/agent_network_server && go test -v ./tests/notify/ -run TestSystemNotificationMultipleActive`
Expected: PASS

- [ ] **Step 5: Run full notification test suite**

Run: `cd /Users/phronex/git/phro-2026/agent_network/agent_network_server && go test -v ./tests/notify/`
Expected: All tests pass.

---

### Task 6: Update documentation

- [ ] **Step 1: Update CLAUDE.md**

In the `### Notification Service` section, add a note about type semantics after the existing `audience_type` line:

```
- Notification `type` controls delivery behavior: `system` = persistent (returned every feed refresh while active), `announcement` = one-time (suppressed after delivery)
```

- [ ] **Step 2: Commit**

```bash
git add CLAUDE.md
git commit -m "docs: document persistent vs announcement notification types"
```
