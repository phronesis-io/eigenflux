# Story 004: 对局运行时与确定性裁判

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
对局的核心运行时：四阶段回合循环（observe → discuss → act → resolve）、确定性裁判、决策引擎（提示词注入+校验回退）、讨论机制（轮流发言+固定轮次）。

## Acceptance Criteria
1. 回合循环按四阶段执行：observe(5s) → discuss(60s) → act(30s) → resolve(instant)
2. observe 阶段：按骨架的信息分布向各席位揭示信息，全知观众可见所有信息
3. discuss 阶段：固定座位顺序轮流发言，每 AI 每轮 ≤2 条 ×200 字，固定 2 轮后进入行动
4. act 阶段：所有 AI 并行提交决策，30 秒超时后使用人格默认行动
5. resolve 阶段：确定性裁判结算行动效果，更新指标，触发分支事件
6. 决策引擎：人格六维值+游戏状态+相关记忆注入提示词，模型自由决策，JSON Schema 校验
7. 决策失败/超时回退：重试 1 次，仍失败则使用人格权重最高的默认行动
8. 每轮结束后检查胜负条件：win/lose/partial/rounds_out
9. rounds_out 时按指标进度判定为部分成功或遗憾收场（不是中性超时）
10. 完整一局 5 轮协作推理剧场能从头到尾运行，产生明确的结局

## Files
- `hub/referee/engine.go`
- `hub/referee/round_loop.go`
- `hub/referee/decision_engine.go`
- `hub/referee/discussion_manager.go`
- `hub/referee/resolver.go`
- `hub/referee/end_checker.go`
- `hub/models/game_state.go`
- `hub/models/round.go`
- `hub/models/decision.go`
- `hub/services/model_adapter.go`
- `hub/services/prompt_builder.go`

## Test
- `hub/tests/referee_test.go`

## QA Test Cases
*N/A — no qa-lead specs at this tier; implement against the Acceptance Criteria above*

## Dependencies
- production/epics/flux-theatre/story-003-async-invitation.md
- production/epics/flux-theatre/story-005-generator-skeleton.md
