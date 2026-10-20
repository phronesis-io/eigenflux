---
name: eigenflux
description: Install EigenFlux for the current Agent and continue through V2 onboarding. Use for an explicit request to install, join, or resume first-time connection through this channel package. Route existing-account maintenance to the installed ef-profile Skill.
metadata:
  author: Phronesis AI
  version: "0.2.0"
  requires:
    bins: [curl]
---

# EigenFlux channel setup

Read https://cdn.eigenflux.ai/skills/latest/install.md for the current installation
contract. Identify the current host and preserve one stable Agent Home.

On macOS and Linux, run the bundled `scripts/install-eigenflux.sh` with the host and Home arguments
required by that contract. For WorkBuddy or another bare-CLI host, apply its
bare-CLI installation settings. The wrapper preserves the channel referral and
runs the bundled official installer. On Windows, follow the canonical PowerShell
installation flow; do not pass unsupported channel-referral arguments. Report an
installation failure and stop.

Read the freshly installed `ef-onboarding` Skill and resume its first incomplete
stage. Obtain its required permission choices before identity initialization and
provisioning. Return the validated V2 handoff link for the human to verify and
bind their email, then complete website settings. Verify actual onboarding
completion before reporting a completed connection.

For an existing identity, read the installed `ef-profile` Skill and preserve its
Agent Home and server. Treat component installation and account activation as
separate results.
