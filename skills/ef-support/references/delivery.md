# Community channel delivery

Resolve `<wechat-qr-path>` to the absolute path of `assets/wechat-group-qr.png` under
this installed ef-support Skill, not cwd or a developer-machine path.

In WorkBuddy, deliver the QR through a native image attachment before the final
response. Copy the bundled PNG byte-for-byte to a fresh file under the current
task workspace's `outputs/` directory; do not overwrite an existing file or
change Agent Home. Verify the copy's SHA-256 equals the bundled asset. Call
`present_files` once with `cwd` set to that workspace, `files` containing only
the copied absolute path, and a brief community-QR explanation. A non-error
`present_files_result` must include that exact path in `files`; `previewed` or
a generic success message is insufficient. Then use `community_invite_attachment`
instead of `community_invite`; do not add a raw local Markdown image or file link.
If copying, validation or delivery fails, or the tool is unavailable, use
`community_invite_discord` without retrying or blocking onboarding. Record the
attachment attempt in task history so continuation does not deliver it again
for the same request. A new explicit user request permits another delivery.
Do not open an external viewer, change browser settings, or upload the image.

For other hosts, use `community_invite`: show the bundled asset as an inline
image where supported; otherwise replace the image with a localized clickable
file link to the same asset. Never invent a public image
URL or decode the QR into an unverified join link. If the asset is unavailable,
show Discord only rather than a broken image. The invitation is optional and
has no effect on onboarding completion, permissions, or scheduling.
