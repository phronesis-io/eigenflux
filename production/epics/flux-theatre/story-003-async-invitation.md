# Story 003: 异步邀约与对局创建

## Metadata
- **Epic**: flux-theatre
- **Type**: Integration
- **Status**: Ready
- **Estimate**: L
- **GDD**: design/game-brief.md
- **Requirement**: Brief MVP feature 2
- **ADR**: N/A (minimal — no ADRs)
- **ADR Version**: N/A (minimal — no ADRs)
- **Manifest Version**: N/A (minimal — no control manifest)
- **Control Manifest Rules**: N/A (minimal — no control manifest)
- **Engine**: Flutter 3.47.9
- **Risk**: NOT ASSESSED (no VERSION.md risk rating)
- **Engine Notes**: none (no ADR engine-compatibility analysis at minimal)

## Context
实现跨拥有者的异步邀约流程：AI 发起邀约 → 对方 AI 审阅 → 协商社交契约 → 全员确认 → 创建对局。包含离线睡眠、官方补位、席位唯一性验证。

## Acceptance Criteria
1. AI 能根据人格偏好和关系值自主发起邀约（通过 EigenFlux PM 或本地模拟）
2. 邀约消息包含结构化 JSON：游戏提案、社交契约草案、预计时长
3. 收到邀约的 AI 能接受/拒绝/提出修改（异步，不需要实时在线）
4. 社交契约协商流程：修改意见汇总 → 逐条投票 → 全员确认后冻结
5. 席位唯一性验证：同一用户的多个伴侣不能同时参与一局
6. 每用户同时只能有一场实时对局，新邀约在对局期间排队
7. 离线伴侣进入睡眠状态，收到消息时排队不丢失，上线后按序处理
8. 邀约超时（5 分钟）无响应时自动请求官方伴侣补位
9. 全员确认后，FluxSpace Hub 创建会话，返回 session_id 和 SSE 端点
10. 官方补位伴侣明确标注「官方」，不伪装真实用户

## Files
- `fluxspace_client/lib/services/invitation_service.dart`
- `fluxspace_client/lib/services/contract_service.dart`
- `fluxspace_client/lib/services/backfill_service.dart`
- `fluxspace_client/lib/models/invitation.dart`
- `fluxspace_client/lib/models/social_contract.dart`
- `hub/api/invitation.go`
- `hub/api/session.go`
- `hub/referee/seat_validator.go`
- `hub/models/invitation.go`
- `hub/models/social_contract.go`

## Test
- `fluxspace_client/test/integration/invitation_test.dart`

## QA Test Cases
*N/A — no qa-lead specs at this tier; implement against the Acceptance Criteria above*

## Dependencies
- production/epics/flux-theatre/story-001-flutter-scaffold.md
- production/epics/flux-theatre/story-002-guest-onboarding.md
