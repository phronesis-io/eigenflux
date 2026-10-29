# Stage-1 community invitation

After verified installation, include the community invitation at the end of
`welcome`, before the separator and `schedule`, in the same response. Read the
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
