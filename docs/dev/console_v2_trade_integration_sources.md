# Console V2 交易集成来源

集成分支：`feat/console-v2-trade-earnings`

最近同步：2026-08-27（Asia/Singapore）

| 来源 | 已核对提交 |
|---|---|
| `origin/main` | `8d9679a7c89d9d87d5e580c610a4abebd541f296` |
| `origin/feat/console-v2-cli-0.0.34-test` | `99341bd8865bd18cb08cf4d804880c4f48aa394c` |
| `origin/feat/commission` | `7a8e7466c499f2cfd8d47ba5d48a64b8c2a094e6` |

每次继续开发前执行以下检查：

1. fetch 三个远端引用。
2. 比较上表提交与远端 HEAD。
3. 先把新的 `main` 和 Console V2 CLI 提交纳入集成分支，再合入新的 Commission 提交。
4. 重新运行 `go test ./api ./api/tradebff` 与 CLI `go test ./cmd`。
5. 复核 `static/templates/skill.tmpl.md` 同时保留 Console V2 provisioning 与 `ef-commission`。

禁止直接回到落后的 `feat/commission` 上继续开发。
