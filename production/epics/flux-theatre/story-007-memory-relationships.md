# Story 007: 持久记忆与关系演化

## Metadata
- **Epic**: flux-theatre
- **Type**: Logic
- **Status**: Ready
- **Estimate**: L
- **GDD**: design/game-brief.md
- **Requirement**: Brief MVP feature 5
- **ADR**: N/A (minimal — no ADRs)
- **ADR Version**: N/A (minimal — no ADRs)
- **Manifest Version**: N/A (minimal — no control manifest)
- **Control Manifest Rules**: N/A (minimal — no control manifest)
- **Engine**: Flutter 3.47.9
- **Risk**: NOT ASSESSED (no VERSION.md risk rating)
- **Engine Notes**: none (no ADR engine-compatibility analysis at minimal)

## Context
持久伴侣系统：Companion ID、六种记忆类型、记忆强度生命周期、有限后局反思、五维关系值、状态标签派生、时间衰减。

## Acceptance Criteria
1. 每个伴侣有唯一的 Companion ID，与人格、模型版本、记忆、关系关联
2. 六种记忆类型正确实现：game_event/social_interaction/personal_reflection/relationship_change/tool_result/quiet_observation
3. 记忆来源标记：每条记忆包含 source（kind/ref_id/round），可追溯到具体事件
4. 记忆强度衰减：每天 -2~5%，高情感记忆衰减更慢，quiet_observation 衰减最慢
5. 记忆被检索时强度 +10%，被 reinforced 时 +15%，被 contradicted 时 -20%（不删除）
6. 有限后局反思：每次对局后最多 1 次反思，产生 1-3 条记忆，500 token 上限，30 分钟冷却
7. 五维关系值正确更新：亲近/信任/默契/敬重/积怨，按简报的变化规则表执行
8. 状态标签自动派生：stranger/acquaintance/warming/close/bonded/strained/fractured/hostile
9. 时间衰减：亲近 -0.5/天、默契 -0.3/天、积怨 -1/天、信任和敬重不衰减
10. 记忆和关系只存本地（SQLite），不同步到服务端，端到端加密

## Files
- `fluxspace_client/lib/services/memory_service.dart`
- `fluxspace_client/lib/services/relationship_service.dart`
- `fluxspace_client/lib/services/reflection_service.dart`
- `fluxspace_client/lib/models/memory.dart`
- `fluxspace_client/lib/models/relationship.dart`
- `fluxspace_client/lib/models/companion.dart`
- `fluxspace_client/lib/repositories/memory_repository.dart`
- `fluxspace_client/lib/repositories/relationship_repository.dart`

## Test
- `fluxspace_client/test/unit/memory_test.dart`
- `fluxspace_client/test/unit/relationship_test.dart`

## QA Test Cases
*N/A — no qa-lead specs at this tier; implement against the Acceptance Criteria above*

## Dependencies
- production/epics/flux-theatre/story-004-match-runtime.md
