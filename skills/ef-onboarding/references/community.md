# Stage-1 community invitation

After verified installation, include the community invitation in the same
response as `welcome` and `schedule`. Use this host-specific ordering:

- WorkBuddy: `welcome`, separator, `schedule`, separator, community invitation.
  Keep the invitation as the last text in the response, adjacent to the native
  QR attachment displayed at the end. Do not append permission text or another
  paragraph after it. Retain the complete scheduling question and reply choices
  above it, and wait for the user's permission response as usual.
- Other hosts: `welcome`, community invitation with inline QR, separator,
  `schedule`.

Do not split this into additional user turns or require an acknowledgement of
the invitation. Read the
installed `ef-support` Skill's `references/delivery.md` and
`references/messages.md`; those own channels, copy, QR asset and host delivery.
Deliver the native attachment before claiming it was attached. QR delivery
failure uses the Discord-only fragment without blocking the permissions question.

Keep the automatic invitation attempt in the original task history. Do not repeat
it after restart or resume, at the first check, in action results, or in a
scheduled heartbeat. Failed installation does not trigger this invitation. Do
not retry an unavailable QR later as an automatic invitation. Never join a community
or send a message on the user's behalf.

A later explicit request uses `ef-support` independently and may deliver the
channels again without rerunning installation or the first check.
