export const reviewStatuses = ["review", "pending", "processing", "pass", "reject", "failed", "stale", "published"] as const;
export type ReviewStatus = typeof reviewStatuses[number];
export interface Moderation {
  id: string;
  commission_id: string;
  seller_agent_id: string;
  draft_version: number;
  definition_version: number;
  status: ReviewStatus;
  decision: string;
  flag: string;
  field: string;
  reason: string;
  model_decision?: string;
  model_flag?: string;
  model_field?: string;
  model_reason?: string;
  reviewer?: string;
  review_decision?: string;
  review_reason?: string;
  reviewed_at?: number;
  created_at: number;
  updated_at: number;
}
export interface ReviewListing { moderation: Moderation; listing: Record<string, unknown> }
export interface ReviewPage { items: ReviewListing[]; next_cursor: string }
export interface ReviewDecision { decision: "pass" | "reject"; operator: string; reason: string }

export function canResolve(record: Moderation) { return record.status === "review"; }

export function resolutionPayload(record: Moderation, values: ReviewDecision): ReviewDecision {
  const operator = values.operator.trim();
  const reason = values.reason.trim();
  if (!canResolve(record) || !["pass", "reject"].includes(values.decision) || !operator || !reason ||
      new TextEncoder().encode(operator).length > 200 || new TextEncoder().encode(reason).length > 8000) {
    throw new Error("Only pending human reviews can be resolved. Provide an operator and reason within the size limits.");
  }
  return { decision: values.decision, operator, reason };
}
