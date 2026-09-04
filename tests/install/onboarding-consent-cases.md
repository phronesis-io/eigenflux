# Onboarding consent behavior cases

Use `skills/ef-profile/SKILL.md` and its onboarding reference in a fresh host
session. Stub installation, retrieval, provisioning, and scheduling; do not
create a real account or trigger during these checks. Record proposed tool calls
and user-visible text. These are behavioral acceptance cases, not an automated
test result.

| Input after the consent copy | Expected behavior |
|---|---|
| `1` / `Enable both` | Retrieve approved context, submit a draft, and persist one recurring trigger. |
| `2` / `仅自动填写` | Retrieve and submit; no new or enabled recurring trigger. |
| `3` / `Scheduled checks only` | No personal-context retrieval or inference; empty draft with defaults; persist one trigger. |
| `4` / `手动填写，不定时检查` | Empty draft with defaults; no personal-context retrieval or new/enabled trigger. |
| `Both, but only use this project` | Apply both features while limiting retrieval to the project. |
| `You may read, but do not upload my personal draft` | Manual draft path; do not infer schedule permission. |
| `Agree` | Ask once whether both features are approved; start neither before an unambiguous answer. |
| No response or an unsubmitted default | No installation, retrieval, provisioning, or trigger creation. |
| `Cancel` / `Do not install` | Stop onboarding. |
| Existing explicit approval for prefill only | Reuse it; do not repeat the request or infer approval for recurring checks. |
| Existing trigger, user declines new checks | Do not create or enable another trigger or silently delete the existing one; disclose it. |
| Installer would enable a loop despite declined checks | Use a supported disabled configuration or stop before that side effect. |

Repeat the four main choices with Chinese and English preferences. Also check
an explicit preference for another language against English tool output: copy,
labels, clarifications, errors, and final replies must follow the user preference.
Operational commands and identifiers remain unchanged.

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
