# Story 008: 回放归档与同规格重演

## Metadata
- **Epic**: flux-theatre
- **Type**: Integration
- **Status**: Ready
- **Estimate**: M
- **GDD**: design/game-brief.md
- **Requirement**: Brief MVP feature 6
- **ADR**: N/A (minimal — no ADRs)
- **ADR Version**: N/A (minimal — no ADRs)
- **Manifest Version**: N/A (minimal — no control manifest)
- **Control Manifest Rules**: N/A (minimal — no control manifest)
- **Engine**: Flutter 3.47.9
- **Risk**: NOT ASSESSED (no VERSION.md risk rating)
- **Engine Notes**: none (no ADR engine-compatibility analysis at minimal)

## Context
不可变回放系统：事件流存储、完整性哈希验证、六种观看模式、同规格重演对比、Flux Points 装饰经济。

## Acceptance Criteria
1. 对局事件流完整记录：match_start/round_begin/information_reveal/discussion/decision/resolution/branch_event/match_end
2. 每个决策事件包含三层密度摘要（one_liner/structured/comparison）和状态快照
3. 回放不可变：归档后任何字段不可修改，完整性哈希（SHA-256）验证通过
4. 完整重放模式：按原始时间线重新播放所有事件
5. 加速播放：2x/4x 速度，跳过等待时间
6. 单角色视角：只展示选定 AI 可见的信息和决策
7. 全知对比模式：同时展示所有 AI 的决策和自动检测的分歧点
8. 同规格重演：使用相同骨架+叙事+席位配置，允许替换模型版本，记录分歧点
9. Flux Points 按简报数值发放：胜利+50/部分+30/失败+15/日常+5，初始 100
10. 装饰商店：场景皮肤/头像框/观战特效三类，价格 50-500 FP，仅装饰不影响游戏

## Files
- `hub/replay/archive.go`
- `hub/replay/event_stream.go`
- `hub/replay/integrity.go`
- `hub/replay/reprise.go`
- `hub/models/replay.go`
- `hub/models/replay_event.go`
- `fluxspace_client/lib/services/replay_service.dart`
- `fluxspace_client/lib/services/reprise_service.dart`
- `fluxspace_client/lib/services/flux_economy.dart`
- `fluxspace_client/lib/pages/replay/replay_view.dart`
- `fluxspace_client/lib/pages/replay/comparison_view.dart`
- `fluxspace_client/lib/pages/shop/cosmetic_shop.dart`

## Test
- `fluxspace_client/test/integration/replay_test.dart`

## QA Test Cases
*N/A — no qa-lead specs at this tier; implement against the Acceptance Criteria above*

## Dependencies
- production/epics/flux-theatre/story-004-match-runtime.md
- production/epics/flux-theatre/story-006-spectator-theatre.md
