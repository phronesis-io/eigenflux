# Persistent System Notifications

## Overview

Add persistent notification capability by leveraging the existing `type` field on `system_notifications`. Notifications with `type=system` are persistent (returned on every feed refresh while active). Notifications with `type=announcement` are one-time (current behavior, returned once then suppressed after ack).

## Type Semantics

| type | Behavior | Delivery record | ListPending delivery check |
|------|----------|----------------|---------------------------|
| `system` | Persistent — returned every feed refresh while `IsActive()` | Written on ack (for audit/debugging) | Skipped |
| `announcement` | One-time — returned once, suppressed after ack | Written on ack (dedup gate) | Applied |

Default type for new notifications: `announcement`.

## Changes

### 1. rpc/notification/dal/types.go

Add type constants:

```go
const (
    TypeSystem       = "system"
    TypeAnnouncement = "announcement"
)
```

### 2. rpc/notification/handler.go — listPendingSystemNotifications

Current flow: list active → filter by delivery check → return undelivered.

New flow: list active → split by type → `system` type skips delivery check, `announcement` type keeps existing delivery check.

```
candidates = active notifications where IsActive(now)
systemCandidates = candidates where type == "system"   → return all
announcementCandidates = candidates where type != "system" → filter by delivery check
return systemCandidates + announcementCandidates
```

Delivery check query only runs for announcement candidates (optimization: skip DB call entirely if no announcement candidates).

### 3. Ack flow — no change

`AckNotifications` writes delivery records for all source_type=system items regardless of notification type. The UNIQUE constraint with `OnConflict{DoNothing}` handles repeated acks for persistent notifications idempotently.

### 4. Console webapp — type selector

On the create/edit system notification form, replace the free-text `type` input with a select dropdown:

| Value | Label |
|-------|-------|
| `announcement` | Announcement (one-time delivery) |
| `system` | System (persistent while active) |

Default selection: `announcement`.

### 5. API Gateway — no change

`fetchPendingNotifications` already passes through the `type` field. `ackNotifications` acks everything (delivery records are useful for audit). No changes needed.

### 6. IDL — no change

`PendingNotification.type` is already a string field. Console IDL `SystemNotificationInfo.type` is already a string field. No schema or IDL changes required.

### 7. Tests — tests/notify/

Add test case: create `type=system` notification → feed returns it → ack → feed again → still returned. Verify `type=announcement` retains existing one-time behavior.

## Files Modified

| File | Change |
|------|--------|
| `rpc/notification/dal/types.go` | Add `TypeSystem`, `TypeAnnouncement` constants |
| `rpc/notification/handler.go` | Split candidates by type in `listPendingSystemNotifications` |
| `console/webapp/src/pages/system-notifications/` | Type field → select dropdown with labels |
| `tests/notify/system_notify_test.go` | Add persistent notification test case |

## Not Changed

- Database schema (no migration)
- IDL files (no codegen)
- API gateway handlers
- Ack logic (delivery records still written for all types)
- `activePayload` / `ActiveStore` (type field already serialized)
