# Console V2 交易、收益与提现 TODO

本文只记录当前代码无法从 `feat/commission` 证明或实现的后端缺口。Console 不复制 Commission 的订单、钱包或提现事实。

## 已接入

- Console V2 路由统一位于 `/api/v2/console/bff/`，复用完成态会话、同源校验、CSRF 和 `private, no-store`。
- 已注册任务概览、能力列表、订单列表与详情、收益摘要与记录、提现方式、提现提交与详情路由。
- Commission CLI 仅在 Agent V2 凭证明确带有 `commission:*` scope 时复用该凭证；否则保留显式 legacy 身份兼容，避免把未经 Commission 认证契约确认的 V2 token 发往资金服务。
- Commission 已有订单、钱包余额、提现方式绑定、提现列表、提现提交和提现详情的 V1 API。

## 阻塞真实数据接入

### 1. Console 会话到 Commission 的身份委托

`feat/commission` 只证明 Commission 接受 EigenFlux Bearer token，并且主体由 token 推导；它禁止请求体传 `agent_id`。Console V2 浏览器只有 HttpOnly Console Cookie，服务端只保存会话和 token 哈希，没有可转发的 Bearer 明文。

Commission 必须提供并文档化以下任一可信契约后，BFF 才能代理真实数据：

- 支持校验 EigenFlux Agent V2 `efv2a_` token，并允许 Console 服务为当前主体取得短期委托 token；或
- 支持双向认证的服务间调用，并验证由 EigenFlux 签名的主体委托声明。

禁止把 Console Cookie 转发给 Commission，禁止接收浏览器提供的 `agent_id`，禁止使用一个静态用户 Bearer 为所有 Console 用户查询资金数据。

在该契约完成前，读取路由返回 `available=false`、金额为 `null`、空列表和 `COMMISSION_IDENTITY_DELEGATION_REQUIRED`；资金写路由返回 503。前端必须显示不可用原因，不能显示伪造金额。

### 2. 收益明细事实接口

当前分支未发现以下 Commission API：

- 按钱包分页读取 credit/收益明细；
- 每笔收益的 `order_id`、成熟时间和聚合结算状态；
- 收益与提现合并时间线需要的稳定排序字段和游标。

需要 Commission 增加分页事实接口，至少返回：

- `record_id`、`record_type`、`occurred_at`、`status`；
- 收益记录的 `order_id`、金额和成熟时间；
- 提现记录的 `withdrawal_id`、金额、状态、`provider_operation_ref`、`last_error_code`；
- 不透明 `next_cursor`。

列表接口应直接返回页面需要的聚合状态，不能要求 BFF 对每笔收益逐条查询 allocation。

### 3. 我的能力与任务概览聚合

现有 Commission RPC 只提供公开索引快照和按 commission 查询的统计；当前分支未提供按卖方分页列出“我的能力”或按当前主体聚合买卖订单数量的接口。

需要 Commission 提供主体受控的：

- 我的 commission 列表；
- 买方/卖方订单数量或轻量 summary。

BFF 不翻遍所有分页结果计算总数，也不在 Console 数据库新增镜像统计表。

### 4. 提现方式读取与安全展示

当前 CLI 证明存在 `POST /api/v1/wallet/binding`，但未证明存在读取当前绑定的 GET API。需要确认或增加只返回以下安全字段的接口：

- provider 类型；
- 后端生成的脱敏账户标识；
- `cooling_until`；
- 更新时间。

provider authorization 只允许在绑定请求内转交，禁止日志、APM、错误响应和项目数据库持久化。

## 联调验收

- 当前 Console 主体只能读取自己的 commission、order、wallet、credit 和 withdrawal。
- 订单详情只允许 buyer 或 seller 读取。
- 绑定和提现端到端透传同一 `Idempotency-Key`，同 key 不同请求体返回冲突。
- `pending` 和 `unknown` 不映射为成功；仅 `succeeded` 和 `failed` 有终态时间。
- 提现失败后刷新余额，以 Commission 最新 `withdrawable_fen` 为准。
