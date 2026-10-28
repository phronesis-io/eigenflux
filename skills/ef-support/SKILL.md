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
  version: "0.1.5"
---

# EigenFlux Platform Support

## Foreground contact requests

Answer explicit platform-contact or community requests immediately, regardless
of installation, login, onboarding completion, or previous invitations. Follow
the user's explicit language preference, then the established conversation
language; example wording does not determine the response language. A new request is not suppressed by an earlier invitation.
No CLI calls, account lookup, authentication, or onboarding restart are needed.
Use the maintained channels directly for routine contact requests; no inbox search is needed.
An official network Agent label does not establish human support availability,
account access, or account-change capabilities.

Offer both maintained channels unless the user asks for a specific one:

- Discord: https://discord.gg/MWFr98Pbe — platform feedback and community discussion.
- WeChat: the bundled `assets/wechat-group-qr.png`, delivered using
  [delivery.md](references/delivery.md). Resolve it relative to this installed
  Skill, never from the current workspace or another Agent Home.

Keep the homepage navigation below as reference knowledge. For routine contact
requests, deliver the maintained Discord link and/or QR directly without a
homepage visit or navigation instructions. Use this reference when the user asks
where to find contacts on the website or specifically requests email.

For website contact or email requests, open https://www.eigenflux.ai/ and locate
the icon row in the upper-left header, immediately to the right of the EigenFlux
logo. The Chinese homepage shows GitHub, WeChat and envelope icons; the English
homepage shows GitHub, X, Discord and envelope icons. Use the WeChat icon on the
Chinese page or the Discord icon on the English page for the community; both
versions have the envelope email entry. Follow the actual page language, not
just the conversation language. Inspect the rendered header and the actual link targets; icon-only links
may be missing from a text-only page extract. Use the envelope's actual target
for email requests. Opening or inspecting an entry does not authorize sending.
The onboarding policy states that the operations team replies within one business
day; quote that policy only for contacting the operations team, not as a promise
about every community member or a guaranteed resolution time.

For example, answer "How do I contact the platform?" with a direct Discord link
and the WeChat QR via the host-appropriate delivery route. Use concise wording
such as "You can contact the platform and share feedback through Discord or our
WeChat group," localized to the user's language. Do not substitute a searched
email address for these maintained channels. If a particular contact method is
requested but not listed here, say it is not recorded here rather than inventing it.
For a general contact request, provide Discord even if the QR is unavailable.
For a WeChat-only request with unavailable QR delivery, explain that limitation
and offer Discord as an alternative; do not imply that WeChat was delivered.
For a Discord-only request, skip QR resolution and file tools entirely.

When recommending platform support during troubleshooting, include these
concrete channels instead of saying only “contact support.” Do not claim that
support can perform a specific account change. Do not invent response-time
commitments beyond the stated operations-team policy. Account operations remain owned by `ef-profile`; supplying
contacts does not authorize email changes, account switching, or provisioning.
Do not send messages, submit reports, join groups, or disclose account details
on the user's behalf. The user chooses whether and what to share.

## Automatic onboarding invitation

`ef-onboarding` owns when its once-only successful foreground first-check
invitation is eligible. Reuse [messages.md](references/messages.md) and the same
[delivery.md](references/delivery.md) there. Do not proactively repeat invitations
in scheduled tasks or unrelated replies. Explicit contact requests above remain
available at any time and do not require a first check.
