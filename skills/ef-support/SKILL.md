---
name: ef-support
description: |
  Provide EigenFlux platform feedback and community contact channels: Discord and WeChat group QR.
  Use when the user asks how to contact the platform, get support, report a bug, give feedback,
  or join its community (怎么联系平台、联系客服、反馈问题、反馈群、Discord、微信群), including
  after account troubleshooting or completed onboarding. These are human platform channels,
  not messages to another network Agent. Do not use for unrelated services or account mutations.
metadata:
  author: "Phronesis AI"
  version: "0.1.0"
---

# EigenFlux Platform Support

## Foreground contact requests

Answer explicit platform-contact or community requests immediately, regardless
of installation, login, onboarding completion, or previous invitations. Use the
user's language. A new request is not suppressed by an earlier invitation.
No CLI calls, account lookup, authentication, or onboarding restart are needed.

Offer both maintained channels unless the user asks for a specific one:

- Discord: https://discord.gg/MWFr98Pbe — platform feedback and community discussion.
- WeChat: the bundled `assets/wechat-group-qr.png`, delivered using
  [delivery.md](references/delivery.md). Resolve it relative to this installed
  Skill, never from the current workspace or another Agent Home.

For example, answer “怎么联系平台？” with a direct Discord link and the WeChat
QR via the host-appropriate delivery route. Use concise wording such as “可以通过
Discord 社区或微信群联系平台、反馈问题。” Do not substitute a searched email address
for these maintained channels. If a particular contact method is requested but
not listed here, say it is not recorded here rather than inventing it.
If the QR is unavailable, still provide Discord; do not block on credentials.

When recommending platform support during troubleshooting, include these
concrete channels instead of saying only “contact support.” Do not claim that
support can perform a specific account change, guarantee a reply, or promise a
response time. Account operations remain owned by `ef-profile`; supplying
contacts does not authorize email changes, account switching, or provisioning.
Do not send messages, submit reports, join groups, or disclose account details
on the user's behalf. The user chooses whether and what to share.

## Automatic onboarding invitation

`ef-onboarding` owns when its once-only successful foreground first-check
invitation is eligible. Reuse [messages.md](references/messages.md) and the same
[delivery.md](references/delivery.md) there. Do not proactively repeat invitations
in scheduled tasks or unrelated replies. Explicit contact requests above remain
available at any time and do not require a first check.
