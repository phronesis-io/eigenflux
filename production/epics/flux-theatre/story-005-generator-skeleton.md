> **状态注记 (2026-11-06, ADR-0003):** 本文件 AC2（`~90%` / `40-60%` / `~10%` 三路径胜率）与 AC10（480 局中 `>=60%` 非 `rounds_out`）的数值目标已由 owner 设计裁决**放宽/停用**（见 `docs/architecture/adr-0003-relaxed-calibration-targets.md`）；权威基线为冻结的 `fluxspace/_balance.js`（实测 **10%** 非 `rounds_out`，且不测量三路径胜率）。本文件正文作为历史记录保留，不予重写。

# Story 005: 生成器骨架与可达性校准

## Metadata
- **Epic**: flux-theatre
- **Type**: Logic
- **Status**: Ready
- **Estimate**: L
- **GDD**: design/game-brief.md
- **Requirement**: Brief MVP feature 3
- **ADR**: N/A (minimal — no ADRs)
- **ADR Version**: N/A (minimal — no ADRs)
- **Manifest Version**: N/A (minimal — no control manifest)
- **Control Manifest Rules**: N/A (minimal — no control manifest)
- **Engine**: Flutter 3.47.9
- **Risk**: NOT ASSESSED (no VERSION.md risk rating)
- **Engine Notes**: none (no ADR engine-compatibility analysis at minimal)

## Context
游戏生成器：三层 Schema（骨架 → 叙事包装 → 会话实例）、三路径可达性模拟、结构指纹防重复、人格映射到行动池。

## Acceptance Criteria
1. 骨架生成器能产出合法的协作推理剧场骨架（目标图+信息分布+指标+回合结构+行动池+分支池+胜负条件）
2. 三路径可达性模拟：最优路径胜率 ~90%（60-80% 回合时获胜）、随机路径胜率 ~40-60%、最差路径胜率 ~10%
3. 校准不合格的骨架不进入候选池（calibration.validated = false）
4. 叙事包装层能将故事主题填充到骨架的叙事槽位，生成完整的游戏描述
5. 结构指纹计算：hash(游戏类型+目标图拓扑+信息分布+回合数+分支数+胜负类型+资源种类)
6. 同一伴侣群最近 5 局内结构指纹不重复
7. 行动池中的 personality_weights 正确映射六维人格到决策评分
8. 生成器产出 3 个候选提案供 AI 选择，每个包含摘要卡片（类型/人数/时长/契约要点）
9. 每次生成的游戏都有唯一的 structural_hash 和 content_hash
10. 生成器的输出通过平衡测试：480 局中至少 60% 能到达非 rounds_out 的结局

## Files
- `hub/generator/skeleton.go`
- `hub/generator/narrative.go`
- `hub/generator/calibrator.go`
- `hub/generator/fingerprint.go`
- `hub/generator/action_pool.go`
- `hub/generator/proposal.go`
- `hub/models/skeleton.go`
- `hub/models/narrative.go`
- `hub/models/calibration.go`

## Test
- `hub/tests/generator_test.go`

## QA Test Cases
*N/A — no qa-lead specs at this tier; implement against the Acceptance Criteria above*

## Dependencies
- production/epics/flux-theatre/story-001-flutter-scaffold.md
