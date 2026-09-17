# Commit-pinned installation distribution

The push-triggered snapshot workflow publishes macOS/Linux artifacts under
`snapshots/<source commit>/<distribution commit>/`. Production paths are never
written. An existing snapshot prefix is immutable; reruns that find objects
there stop instead of overwriting them. The public entry is `install.md` inside
that prefix; use its ordinary join instructions in the onboarding conversation.

The source commit is fixed in `build.py`. The workflow builds the native CLI
with a compiled HTTP(S) Skills origin and an exact installed-revision
postcondition. There is no wrapper, runtime test flag, environment handoff,
embedded bundle, or persistent local distribution server. A snapshot-specific
signing key is generated during the build, used by the original manifest signer,
and removed; its public key is injected using the existing release build hook.

The installation document changes only installer URLs. The staged onboarding
Skill changes only its installation-entry URL and patch version so resume uses
the same entry. Business Skill instructions and native scheduler prompts remain
unchanged. The installer verifies CLI bytes even for equal semantic versions,
rejects a shadowing PATH executable, and rechecks CLI/Skills after host setup.
An unavailable signed distribution stops installation rather than bootstrapping
mutable `main` instructions. The original host setup, identity, referral,
authorization and onboarding behavior remain owned by the source commit.

Publication records `provenance.json` and `checksums.json`. The workflow checks
every public artifact's bytes after upload. Audit patches are retained in its
GitHub Actions artifact. Do not add this README or test expectations to a join
prompt or scheduler body. Operators manage rule toggles and collect evidence
outside the tested conversation.

The distribution is intended for a clean installation, as in the manual
onboarding tests. An existing manifest from a different signer/sequence or
modified installed Skills may be rejected; it is never silently accepted as the
requested revision. Preserve and clean old installations through the normal
operator process rather than weakening rollback or signature verification.

Validation:

```bash
python3 scripts/snapshots/test_contract.py
python3 scripts/snapshots/test_http.py
(cd cli && go test ./...)
```

`test_http.py` uses fake accounts and a localhost server. It verifies actual
native subprocesses and installer functions, not Codex approvals or scheduling.
Those require normal manual onboarding and real native heartbeat runs. No
snapshot publication installs anything on the operator's Mac.

The previous local embedded-snapshot kit remains a developer aid. This remote
distribution is the user-facing entry for the manual experiment.
