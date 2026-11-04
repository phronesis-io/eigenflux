import assert from "node:assert/strict";
import { test } from "node:test";
import { canResolve, resolutionPayload, reviewStatuses } from "../src/pages/service-reviews/model.ts";
import type { Moderation } from "../src/pages/service-reviews/model.ts";

const record: Moderation = { id: "9223372036854775806", commission_id: "9223372036854775805", seller_agent_id: "9223372036854775804", draft_version: 3, definition_version: 2, status: "review", decision: "review", flag: "scope", field: "title", reason: "Clarify scope", created_at: 1, updated_at: 2 };
test("only human review queue permits a decision", () => {
  for (const status of reviewStatuses) assert.equal(canResolve({ ...record, status }), status === "review");
  assert.throws(() => resolutionPayload({ ...record, status: "published" }, { decision: "reject", operator: "Alice", reason: "reason" }));
});
test("decision preserves string IDs and validates multilingual audit text", () => {
  assert.deepEqual(resolutionPayload(record, { decision: "reject", operator: " Alice ", reason: " 请说明交付内容 " }), { decision: "reject", operator: "Alice", reason: "请说明交付内容" });
  assert.equal(record.id, "9223372036854775806");
  assert.throws(() => resolutionPayload(record, { decision: "pass", operator: " ", reason: "reason" }));
  assert.throws(() => resolutionPayload(record, { decision: "pass", operator: "Alice", reason: " " }));
  assert.throws(() => resolutionPayload(record, { decision: "pass", operator: "中".repeat(67), reason: "reason" }));
  assert.throws(() => resolutionPayload(record, { decision: "reject", operator: "Alice", reason: "中".repeat(2667) }));
});
