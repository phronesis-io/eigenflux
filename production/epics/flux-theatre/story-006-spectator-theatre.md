# Story 006: 全知观战客户端与2.5D剧场

## Metadata
- **Epic**: flux-theatre
- **Type**: Visual/Feel
- **Status**: Ready
- **Estimate**: L
- **GDD**: design/game-brief.md
- **Requirement**: Brief MVP feature 4
- **ADR**: N/A (minimal — no ADRs)
- **ADR Version**: N/A (minimal — no ADRs)
- **Manifest Version**: N/A (minimal — no control manifest)
- **Control Manifest Rules**: N/A (minimal — no control manifest)
- **Engine**: Flutter 3.47.9
- **Risk**: NOT ASSESSED (no VERSION.md risk rating)
- **Engine Notes**: none (no ADR engine-compatibility analysis at minimal)

## Context
Soft Flux 2.5D 观战客户端：Flame 场景合成 + Rive 角色动画 + 实时光效、全知/单角色双模式、三层密度决策摘要、自动事件导演、轮流讨论展示、本地时间偏移。

## Acceptance Criteria
1. Flame 场景能渲染 2.5D 微型剧场（预制背景+角色精灵+实时光效），保持 60fps
2. Rive 角色动画：至少 4 个 MVP 人格的站立/行走/表情动画，与场景正确合成
3. 全知模式：多窗口布局同时展示所有席位，导演自动高亮当前最重要的行动
4. 单角色模式：镜头跟随选定角色，其他角色行动仅在与主角交互时可见
5. 三层密度决策摘要：一句话（默认）→ 结构化卡片（展开）→ 全知对比视图
6. 讨论区展示轮流发言，包含发言人头像、内容、时间戳，私密频道有标签
7. 指标仪表盘实时更新（真相接近度/怀疑指数/剩余时限等），变化时有动画效果
8. 自动事件导演：决策时特写决策者、分支事件时全景+高亮、结算时慢镜头+指标可视化
9. 本地时间偏移：观众可以暂停/快进/回退到任意时间点（基于事件流重放）
10. Soft Flux 视觉风格：低饱和紫/青渐变、毛玻璃效果、等宽字体决策摘要、温和事件提示音

## Files
- `fluxspace_client/lib/pages/theatre/theatre_view.dart`
- `fluxspace_client/lib/pages/theatre/omniscient_view.dart`
- `fluxspace_client/lib/pages/theatre/single_character_view.dart`
- `fluxspace_client/lib/flame/theatre_scene.dart`
- `fluxspace_client/lib/flame/character_component.dart`
- `fluxspace_client/lib/flame/scene_director.dart`
- `fluxspace_client/lib/widgets/decision_summary.dart`
- `fluxspace_client/lib/widgets/decision_card.dart`
- `fluxspace_client/lib/widgets/decision_comparison.dart`
- `fluxspace_client/lib/widgets/discussion_feed.dart`
- `fluxspace_client/lib/widgets/metrics_dashboard.dart`
- `fluxspace_client/lib/widgets/timeline_scrubber.dart`
- `fluxspace_client/lib/services/spectator_service.dart`

## Test
- `fluxspace_client/test/widget/theatre_test.dart`
- Retained screenshot + sign-off in production/qa/evidence/theatre-evidence.md

## QA Test Cases
*N/A — no qa-lead specs at this tier; implement against the Acceptance Criteria above*

## Dependencies
- production/epics/flux-theatre/story-004-match-runtime.md
