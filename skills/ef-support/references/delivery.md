# Community channel delivery

Respect the caller's response mode and selected channels. A Discord-only request
needs only the maintained link; skip this image flow. For an explicit contact
request, answer directly using the confirmed delivery outcome, without an
onboarding invitation preamble. The `community_invite*` fragments below apply
only to the eligible automatic onboarding invitation.

## WorkBuddy inline image

In WorkBuddy, use this maintained HTTPS image directly as `<wechat-qr-path>`:

https://raw.githubusercontent.com/phronesis-io/eigenflux/6e1349833d7672cd5a425c425687ffffa8f2f476/skills/ef-support/assets/wechat-group-qr.png

Render it with Markdown image syntax immediately after the invitation text,
before the separator and scheduling question. Use `community_invite` for the
automatic welcome. For explicit requests, put the image after the direct contact
answer. Do not call `present_files`, copy files, or probe the URL on this primary
path. Do not claim that client-side rendering was verified merely by emitting
Markdown. The final scheduling question remains last.

This immutable public image matches the bundled PNG. When replacing that asset,
maintainers must update this URL to the corresponding published image and verify
byte equality before release. Never construct a URL from the current workspace.

Use the native fallback below only if HTTPS image loading is known unavailable
or the user reports a failed image. Do not deliver both forms preemptively.
A user-reported failure permits one fallback for that request, not a new
onboarding invitation or a repetition of setup/permission questions.

## Bundled asset and WorkBuddy fallback

For the fallback and other hosts, resolve `<wechat-qr-path>` to the absolute path
of `assets/wechat-group-qr.png` under this installed ef-support Skill, not cwd or
a developer-machine path.

In WorkBuddy, deliver the QR through a native image attachment only on the
fallback path, before the final response. Copy the bundled PNG byte-for-byte to a fresh file under the current
task workspace's `outputs/` directory; do not overwrite an existing file or
change Agent Home. Verify the copy's SHA-256 equals the bundled asset. Call
`present_files` once with `cwd` set to that workspace, `files` containing only
the copied absolute path, and a brief community-QR explanation. A non-error
`present_files_result` must include that exact path in `files`; `previewed` or
a generic success message is insufficient. For the automatic invitation, use
`community_invite_attachment` instead of `community_invite`. For an explicit
request, refer to the attached QR directly, including Discord only when requested
or when the user asked for general contacts. In either mode, do not add a raw local Markdown image or file link.
If copying, validation or delivery fails, or the tool is unavailable, use
`community_invite_discord` for the automatic invitation without retrying or blocking onboarding.
For explicit requests, report unavailable QR delivery and offer Discord as an
alternative; do not claim the QR was attached. Record the
attachment attempt in task history so continuation does not deliver it again
for the same request. A new explicit user request permits another delivery.
Do not open an external viewer, change browser settings, or upload the image.

For other hosts, use `community_invite` for the automatic invitation only;
for explicit requests, use direct contact wording. Show the bundled asset as an inline
image where supported; otherwise replace the image with a localized clickable
file link to the same asset. Never invent a public image
URL or decode the QR into an unverified join link. If the asset is unavailable,
show Discord only for the automatic invitation rather than a broken image;
for explicit requests, explain unavailable QR delivery and offer Discord as an
alternative. The invitation is optional and
has no effect on onboarding completion, permissions, or scheduling.
