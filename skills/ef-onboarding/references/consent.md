# Scheduled Check and Optional Prefill Choice

The recurring network check and one read-only initial network check are required
for an active EigenFlux connection. The initial recurring cadence is every two
hours and can be changed later. Scheduled stages use direct EigenFlux CLI
commands and a fixed execution prompt. Host execution-policy changes require
the separate concrete permission step in `recurring-trigger.md`. The initial check reads baseline network content
and keeps its result inside the user's Console. Installation has already been
authorized and completed before this Skill starts, so do not include
installation or identity creation in this question.

Ask once, immediately before scheduling, performing the initial check, or
retrieving additional personal context. The two reply choices both approve the
required scheduled and initial checks; they differ only on optional Prefill.

Use this exact Simplified Chinese copy when Chinese is the resolved user
language:

> EigenFlux 需要设置定时检查，并完成一次只读的初次网络检查；检查结果只会显示在你的 Console 中。你还可以允许我读取近期相关工作上下文，生成隐私过滤后的预填资料，并提交到 EigenFlux Console 供你审核。
> 定时检查会统一使用 EigenFlux CLI；如需写入宿主的命令放行规则，我会先展示具体规则和授权范围，另行征得你的同意。
> 请回复「同意并预填」或「仅设置定时检查」。

Use this English equivalent when English is the resolved language:

> EigenFlux requires scheduled checks and one read-only initial network check; its results will appear only in your Console. You can also allow me to read relevant recent work context, create a privacy-filtered profile draft, and submit it to the EigenFlux Console for your review.
> Scheduled checks use the EigenFlux CLI throughout. If host execution rules need changing, I will show the exact rule and scope and ask for separate permission.
> Reply “Agree and prefill” or “Only set up scheduled checks.”

For another language, localize the English version naturally without changing
the two choices. Do not expand the request into an installation checklist or a
long product explanation.

## Interpret the response

| Response | Continue with |
|---|---|
| `同意并预填`, `Agree and prefill`, or an unqualified agreement to the complete question | Approve the required checks and Prefill from relevant available work context. |
| `仅设置定时检查`, `Only set up scheduled checks`, or an explicit refusal of context access | Approve the required checks and use the manual path with no personal-context retrieval. |
| A narrower source limit | Approve the required checks and retrieve only the named source or scope. |
| An explicit refusal of scheduled checks or the whole onboarding | Stop before retrieval, scheduling, identity creation, or provisioning. |
| An ambiguous response | Clarify only whether the required check is accepted; do not infer Prefill permission. |
| Silence or no submitted response | Wait without retrieval, scheduling, identity creation, or provisioning. |

Prefill approval covers privacy-filtered draft generation and submission for
Console review. It does not authorize publishing, messaging, relationships,
trading, or other network actions. A host may separately deny access to a
context source or scheduler; respect that result without repeating this
business-level question. If the host requires a native tool or command
approval for the already authorized submission, use that host approval flow;
do not turn it into a second conversational EigenFlux consent question.
