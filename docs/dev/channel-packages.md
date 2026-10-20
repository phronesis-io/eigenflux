# Channel packages

Build the SkillHub and Xiaohongshu packages with
`python3 scripts/release/build-channel-packages.py --output-dir build/channel-packages`.
Use an empty output directory; the builder refuses to replace existing files.

`channels/skillhub/SKILL.md` owns the setup entry. Each package contains that
entry, a referral wrapper and an exact copy of `static/install.sh`. The wrapper
preserves `EF_REF_CODE` and forwards host, Home and installation arguments to
the official installer. The Xiaohongshu default referral is `EF-1PoI4QXu`.
An explicit `--ref` argument takes precedence over that default. The SkillHub
package has no default referral. The bundled shell wrapper is for macOS/Linux;
Windows uses the canonical PowerShell flow without unsupported referral flags.

The installer owns artifact verification, installation registration, referral
persistence and current-host Skills synchronization. The setup entry loads the
current `ef-onboarding` Skill for identity provisioning and required Console V2
email verification. Existing identities route to `ef-profile` in the same Home.
Business Skills are not copied into channel packages, so a channel upload
cannot retain a second stale copy of those workflows.

The generated zip archives are uploadable artifacts. Building or changing a
package does not update an existing marketplace listing. Replace the complete
old package when uploading; do not overlay the new files onto a package with
bundled business Skills. Retain a backup of the old local package outside its
upload directory before replacement.
