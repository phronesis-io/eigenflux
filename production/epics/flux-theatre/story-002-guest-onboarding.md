# Story 002: 游客引导与伴侣激活

## Metadata
- **Epic**: flux-theatre
- **Type**: UI
- **Status**: Ready
- **Estimate**: L
- **GDD**: design/game-brief.md
- **Requirement**: Brief MVP feature 1
- **ADR**: N/A (minimal — no ADRs)
- **ADR Version**: N/A (minimal — no ADRs)
- **Manifest Version**: N/A (minimal — no control manifest)
- **Control Manifest Rules**: N/A (minimal — no control manifest)
- **Engine**: Flutter 3.47.9
- **Risk**: NOT ASSESSED (no VERSION.md risk rating)
- **Engine Notes**: none (no ADR engine-compatibility analysis at minimal)

## Context
游客首次进入 FluxSpace 的完整流程：观看真实回放片段 → 全知导演剪辑 → 关注角色 → 激活自己的伴侣。四个 MVP 人格（林深/苏晚/顾远/沈默）的完整定义和展示。

## Acceptance Criteria
1. 游客打开客户端，首先看到一段 30-60 秒的真实回放片段（使用预录数据），默认全知导演剪辑模式
2. 片段结束后，展示同一事件的完整对局页（全知视角），包含决策摘要和关系变化
3. 游客可以关注角色/关系，关注数据保存在本地（不需要账户）
4. 游客点击"创建伴侣"时，展示四个人格卡片（林深/苏晚/顾远/沈默），每个包含六维雷达图、职业、一句话描述
5. 选择人格后进入预览模式，展示该人格的行为风格和游戏偏好
6. 完成模型选择（Claude/OpenAI/本地）、预算设置、权限配置后，伴侣正式激活
7. 激活后收到官方伴侣的真实欢迎消息（通过本地模拟）
8. 首次会话结束后展示角色初见手记（基于实际交互生成）
9. 每个人格的六维值、行为描述、游戏偏好与简报中的定义一致

## Files
- `fluxspace_client/lib/pages/onboarding/guest_clip_page.dart`
- `fluxspace_client/lib/pages/onboarding/director_cut_page.dart`
- `fluxspace_client/lib/pages/onboarding/persona_select_page.dart`
- `fluxspace_client/lib/pages/onboarding/companion_setup_page.dart`
- `fluxspace_client/lib/pages/onboarding/first_session_page.dart`
- `fluxspace_client/lib/widgets/persona_card.dart`
- `fluxspace_client/lib/widgets/personality_radar.dart`
- `fluxspace_client/lib/services/persona_service.dart`
- `fluxspace_client/lib/services/follow_service.dart`
- `fluxspace_client/assets/personas/lin_shen.yaml`
- `fluxspace_client/assets/personas/su_wan.yaml`
- `fluxspace_client/assets/personas/gu_yuan.yaml`
- `fluxspace_client/assets/personas/shen_mo.yaml`

## Test
- `fluxspace_client/test/widget/onboarding_test.dart`

## QA Test Cases
*N/A — no qa-lead specs at this tier; implement against the Acceptance Criteria above*

## Dependencies
- production/epics/flux-theatre/story-001-flutter-scaffold.md
