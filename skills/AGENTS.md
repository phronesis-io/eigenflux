# Skills maintenance

Read this file before modifying anything under `skills/`.

## Editing rules

- Every change to an existing Skill must increase `metadata.version` in its
  `SKILL.md` in the same commit. Include changes to references, scripts, and
  assets. Increase at least the patch version relative to `main`; upgrade each
  affected Skill. Give new Skills an explicit version.
- Use stable `major.minor.patch` metadata versions for production `ef-*` Skills.
- Use concise, imperative instructions. Keep only necessary context and steps.
  Omit repetition, filler, examples, and counterexamples.
- Keep installation instructions in [install.md](install.md); link to that
  source wherever installation guidance is needed.
- Raise `SKILLS_MIN_CLI_VERSION` in `cli/.cli.config` when new instructions
  require a newer CLI.

## Onboarding prefill regression

When changing onboarding draft-generation instructions, run both behavioral
cases in [the prefill regression suite](../tests/onboarding_prefill/README.md).
Generate fresh drafts in independent Agents without inherited history or
expected outcomes. Complete structural checks and semantic review before
reporting validation. Keep regression fixtures outside the Skills bundle.

## Automatic publishing

Changes under `skills/**` merged into `main` trigger
[Release Skills](../.github/workflows/release-skills.yml). Runs are serialized
and check out current `main`. After tests pass,
[release-skills.sh](../cli/scripts/release-skills.sh) builds and signs the
production `ef-*` Skills bundle, excluding `ef-localdev`, then publishes the
bundle and manifest to R2 at `skills/latest/` and `cli/latest/` and verifies
the public CDN downloads.

The workflow also publishes `install.md` unchanged to
<https://cdn.eigenflux.ai/skills/latest/install.md> and verifies its content
and `Cache-Control: no-store`. The `/install` page's `/r/:ref` entry points to
this URL, so installation instructions refresh without an API redeployment.

Clients adopt compatible updates on their next Skills sync. Content hashes
determine the bundle `revision`; signed releases use an increasing `sequence`.
The publisher does not increment Skill versions or enforce version bumps;
editors must update `metadata.version` themselves. Pure Skills changes need
no CLI or plugin release.

The root `install.md` and this maintenance file are outside the Skills
bundle and have no Skill version. Editing them still triggers the workflow;
leave unrelated Skill versions unchanged.

After merging, verify the workflow's publish step and live CDN content. The
later `static/feed_contract.md` sync can fail independently after publishing.
