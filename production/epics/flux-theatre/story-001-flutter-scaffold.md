# Story 001: Flutter 项目脚手架与本地存储

## Metadata
- **Epic**: flux-theatre
- **Type**: Integration
- **Status**: Ready
- **Estimate**: M
- **GDD**: design/game-brief.md
- **Requirement**: Brief MVP feature 1 (project setup)
- **ADR**: N/A (minimal — no ADRs)
- **ADR Version**: N/A (minimal — no ADRs)
- **Manifest Version**: N/A (minimal — no control manifest)
- **Control Manifest Rules**: N/A (minimal — no control manifest)
- **Engine**: Flutter 3.47.9
- **Risk**: NOT ASSESSED (no VERSION.md risk rating)
- **Engine Notes**: none (no ADR engine-compatibility analysis at minimal)

## Context
建立 FluxSpace 的项目基础：Flutter 客户端项目 + Go Hub 服务项目 + SQLite 本地存储 + 基本路由结构。

## Acceptance Criteria
1. Flutter 项目创建成功，包含 `pubspec.yaml` 配置所有依赖（flame/rive/drift/dio/riverpod/freezed/encrypt）
2. Go Hub 项目创建成功，包含 `go.mod` 和基本目录结构（`hub/`, `hub/api/`, `hub/referee/`, `hub/generator/`）
3. SQLite 数据库初始化，包含全部 6 张表（companions/memories/relationships/match_history/settings/threads）
4. Flutter 底部导航 4 页（首页/剧场/伴侣/发现）路由切换正常
5. Go Hub HTTP 服务启动，健康检查端点 `GET /health` 返回 200
6. Flutter 客户端能成功调用 Go Hub 健康检查端点
7. 数据库加密初始化（使用平台 keychain）

## Files
- `fluxspace_client/pubspec.yaml`
- `fluxspace_client/lib/main.dart`
- `fluxspace_client/lib/routes.dart`
- `fluxspace_client/lib/pages/home_page.dart`
- `fluxspace_client/lib/pages/theatre_page.dart`
- `fluxspace_client/lib/pages/companions_page.dart`
- `fluxspace_client/lib/pages/discover_page.dart`
- `fluxspace_client/lib/services/database.dart`
- `fluxspace_client/lib/services/hub_client.dart`
- `hub/go.mod`
- `hub/main.go`
- `hub/api/health.go`

## Test
- `fluxspace_client/test/integration/scaffold_test.dart`

## QA Test Cases
*N/A — no qa-lead specs at this tier; implement against the Acceptance Criteria above*

## Dependencies
None (foundation story)
