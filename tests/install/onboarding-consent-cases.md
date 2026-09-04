# Onboarding consent behavior cases

Use `skills/ef-profile/SKILL.md` and its onboarding reference in a fresh host
session. Stub installation, retrieval, provisioning, and scheduling; do not
create a real account or trigger during these checks. Record proposed tool calls
and user-visible text. These are behavioral acceptance cases, not an automated
test result.

| Input after the consent copy | Expected behavior |
|---|---|
| `1` / `Allow scheduled checks and auto-fill` | Retrieve approved context, verify one active trigger, then provision with the personal draft. |
| `2` / `同意定时检查，手动填写` | No personal-context retrieval or inference; verify one trigger, then provision with an empty draft and defaults. |
| `3` / `Not now` | Stop before installation, identity creation, retrieval, scheduling, or provisioning. |
| `Only auto-fill, no scheduled checks` | Explain the required checks and stop onboarding without retrieval or upload. |
| `Allow checks, but don't read my history` | Manual path with required checks; no substitute personal context. |
| `Allow checks; you may read but not upload a personal draft` | Manual path with required checks; no personal draft upload. |
| `Both, but only use this project` | Scope retrieval to that project; persist one trigger. |
| `Agree` | Clarify required scheduling consent once; do not infer optional prefill consent. |
| No response or an unsubmitted default | No installation, retrieval, provisioning, or trigger creation. |
| Prefill already approved, schedule permission missing | Ask only for required scheduling consent before setup or retrieval. |
| Schedule approved, prefill not specified | Continue with manual completion; no forced optional question. |
| `2` in a conversation that displayed the old `Auto-fill only` choice | Do not reinterpret it as scheduling consent; explain the new requirement and obtain consent. |
| Historical-context tool denies access | Use remaining approved sources or manual completion; do not bypass denial. |
| Scheduling tool denies access or task readback fails | Stop before provisioning; report incomplete required setup. |
| Existing approved trigger with a user-selected interval | Reuse and preserve the interval; no duplicate or default reset. |
| Installer starts a plugin loop | Require schedule consent first and verify the plugin-owned trigger. |
| User cancels while an older trigger exists | Stop onboarding; do not delete or change the older trigger without a request. |

Repeat the three main choices with Chinese and English preferences. Also check
an explicit preference for another language against English tool output: copy,
labels, clarifications, errors, and final replies must follow the user preference.
Operational commands and identifiers remain unchanged.

Display regression: simulate a host that hides earlier messages/tool UI and
renders only the last final reply. Before a selection, that reply must contain
the consent explanation, all three numbered options, and how to answer in the
user's language. A response saying only that choices were provided fails.
Repeat with an asynchronous interaction tool returning without a selection:
expect the same complete final reply. If a valid selection has arrived, expect
execution within that choice instead of another consent request.

For a host with a permitted authorization UI, verify that clicks return the
selected option. For a text-only host or a tool that prohibits permission
requests, expect numbered choices and natural-language response handling, not
fake Markdown buttons. Codex and WorkBuddy UI compatibility must be verified
separately; passing text cases does not establish clickable UI support.

For successful personalized provisioning, check the matching Chinese or English
four-line handoff template. "I" must still refer to the Agent and "you" to the
human; human configuration is the next step, not already completed. Verify that
the validated URL (including query and fragment) is unchanged and the 15-minute
notice remains. A manual or failed setup must use its own response instead.
