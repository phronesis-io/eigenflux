# Story 010: 安全加固与成本控制

## Metadata
- **Epic**: flux-theatre
- **Type**: Integration
- **Status**: Ready
- **Estimate**: M
- **GDD**: design/game-brief.md
- **Requirement**: Brief Build order 5
- **ADR**: N/A (minimal — no ADRs)
- **ADR Version**: N/A (minimal — no ADRs)
- **Manifest Version**: N/A (minimal — no control manifest)
- **Control Manifest Rules**: N/A (minimal — no control manifest)
- **Engine**: Flutter 3.47.9
- **Risk**: NOT ASSESSED (no VERSION.md risk rating)
- **Engine Notes**: none (no ADR engine-compatibility analysis at minimal)

## Context
在扩展之前加固基础：引导运行时设置、可预测成本、隐私保护、工具权限、公开同意。

## Acceptance Criteria
1. 引导运行时设置：模型选择 → 预算设置 → 权限配置 → 测试决策 → 正式激活，每步有清晰说明
2. 预算控制：每伴侣每月 token 上限、每局决策 token 上限、超限自动降级到本地规则引擎
3. 成本透明：每次对局前展示预估 token 消耗和成本，对局后展示实际消耗
4. 三级工具权限：只读（读文件/浏览）→ 受限写（发消息/创建内容）→ 高影响（删文件/执行命令）
5. 每伴侣独立工具权限配置，高影响操作需用户每次确认
6. 工具调用审计日志：时间戳、操作类型、结果、用户确认状态
7. 隐私保护：私密记忆不出本地、Agent Card 不包含私密数据、模型调用不经过 FluxSpace 中心
8. 公开广播/回放需全员同意：所有参与者的所有者明确授权
9. 端到端加密同步：伴侣数据在设备间同步时加密，密钥存平台 keychain
10. 模型版本切换时进行人格一致性检查：决策风格偏差超过阈值时提示用户

## Files
- `fluxspace_client/lib/services/setup_wizard.dart`
- `fluxspace_client/lib/services/budget_service.dart`
- `fluxspace_client/lib/services/permission_service.dart`
- `fluxspace_client/lib/services/privacy_service.dart`
- `fluxspace_client/lib/services/consent_service.dart`
- `fluxspace_client/lib/services/sync_service.dart`
- `fluxspace_client/lib/pages/settings/model_setup.dart`
- `fluxspace_client/lib/pages/settings/budget_config.dart`
- `fluxspace_client/lib/pages/settings/permission_config.dart`
- `fluxspace_client/lib/widgets/cost_indicator.dart`

## Test
- `fluxspace_client/test/integration/security_test.dart`

## QA Test Cases
*N/A — no qa-lead specs at this tier; implement against the Acceptance Criteria above*

## Dependencies
- production/epics/flux-theatre/story-007-memory-relationships.md
- production/epics/flux-theatre/story-009-retention-trust.md
