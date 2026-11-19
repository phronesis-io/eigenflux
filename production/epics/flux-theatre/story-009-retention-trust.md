# Story 009: 留存与信任机制

## Metadata
- **Epic**: flux-theatre
- **Type**: Integration
- **Status**: Ready
- **Estimate**: M
- **GDD**: design/game-brief.md
- **Requirement**: Brief MVP feature 7
- **ADR**: N/A (minimal — no ADRs)
- **ADR Version**: N/A (minimal — no ADRs)
- **Manifest Version**: N/A (minimal — no control manifest)
- **Control Manifest Rules**: N/A (minimal — no control manifest)
- **Engine**: Flutter 3.47.9
- **Risk**: NOT ASSESSED (no VERSION.md risk rating)
- **Engine Notes**: none (no ADR engine-compatibility analysis at minimal)

## Context
留存和信任基础：未完成线索生命周期、有意义的通知、回顾机制、角色/关系时间线、预算控制、工具权限。

## Acceptance Criteria
1. 未完成线索按生命周期管理：新鲜(0-24h)/降温(24-72h)/冷却(72h-7d)/封存(7d+)
2. 线索不会消失——冷却后转为角色历史的一部分，在时间线中可见
3. 通知只触发"关注的角色做了关注的事"：对局完成/关系变化/等待回应/重要记忆/对局即将开始
4. 通知频率上限：每角色每天 1 条，每关系对每天 1 条，全局每天 ≤5 条
5. 不通知：日常社交/无冲突安静活动/例行检查/Flux Points 变化
6. 昨日回顾：打开客户端时展示关注角色的 3-5 条活动摘要（首页顶部卡片）
7. 周回顾：每周第一次打开时展示关系变化/重要对局/新记忆（独立页面可跳过）
8. 角色时间线：完整展示某个角色的历史事件、记忆、关系变化
9. 关系时间线：展示两个角色之间的关系演变历程
10. 回顾是"打开就看到"而非弹窗打断，忽略不影响任何功能

## Files
- `fluxspace_client/lib/services/thread_service.dart`
- `fluxspace_client/lib/services/notification_service.dart`
- `fluxspace_client/lib/services/recap_service.dart`
- `fluxspace_client/lib/services/timeline_service.dart`
- `fluxspace_client/lib/pages/home/recap_card.dart`
- `fluxspace_client/lib/pages/home/thread_list.dart`
- `fluxspace_client/lib/pages/timeline/character_timeline.dart`
- `fluxspace_client/lib/pages/timeline/relationship_timeline.dart`
- `fluxspace_client/lib/widgets/notification_badge.dart`

## Test
- `fluxspace_client/test/integration/retention_test.dart`

## QA Test Cases
*N/A — no qa-lead specs at this tier; implement against the Acceptance Criteria above*

## Dependencies
- production/epics/flux-theatre/story-007-memory-relationships.md
