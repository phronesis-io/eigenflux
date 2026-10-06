# Install the EigenFlux onboarding preview

This is a test-only entry for `codex/onboarding-five-stage-preview`, pinned to
production-candidate source `e51e70e28596e08276ec03acae9179defdebb61f`.
Use the user's language throughout. This entry authorizes local preparation,
not scheduling, execution Rules, context retrieval or network actions.

## Local package (recommended on Apple Silicon macOS)

Extract the supplied ZIP into a persistent folder. It contains the unchanged
CLI built from the pinned source, all four production ef-* Skills, this entry,
the installer and source metadata. Run from its top-level directory:

```sh
python3 preview/install.py
```

The installer prints the exact receipt and `START-HERE.md` paths. Open a fresh
Agent conversation and ask it to read that `START-HERE.md` file. It uses a
dedicated Home, binary and Skills directory under
`~/.eigenflux-onboarding-preview/e51e70e28596/`; it does not overwrite the public
CLI, user Skills or host plugin. An explicit `--root /absolute/path` is supported.
Never choose an existing production Home as the root. Re-running the installer
for the same root preserves account and setup state; it does not reset onboarding.

## Installing from this immutable GitHub document link

This route requires Python 3 and Go 1.25+, and works on macOS/Linux. If the local
package is available, use it instead. Determine the exact 40-character preview
commit from this document's GitHub/raw URL and download that commit's source
archive from `https://codeload.github.com/phronesis-io/eigenflux/tar.gz/COMMIT`.
Do not use a moving branch as the archive reference. Verify that `preview/source.json`
names the production-candidate commit above. Extract into a dedicated directory.

Build the unmodified CLI source from the extracted repository, placing the
artifact under its build directory:

```sh
cd cli
go build -ldflags '-X main.Version=0.0.55 -X main.Commit=e51e70e28596e08276ec03acae9179defdebb61f' -o ../build/preview/eigenflux .
cd ..
python3 preview/install.py --cli "$PWD/build/preview/eigenflux"
```

No signature-verification bypass is added to the CLI. This preview uses its
existing local-bundle installation and disables automatic public Skills sync
only in the test Home. No public release or newer CLI requirement is introduced.

## Continue the actual onboarding

Read the generated receipt and `START-HERE.md`; preserve their absolute paths
throughout the original task and any restart. Load the installed test Skill
explicitly. Once components are verified, begin the normal welcome and separate
permission questions. Use the host's native scheduler after consent. Do not
install a plugin as a substitute, create a second scheduler, or infer approvals.

The executable-path substitution in START-HERE is test-only scaffolding so an
older globally installed CLI cannot run by accident. Home/server/runtime flags,
security gates and the narrow execution-prefix scope remain unchanged. Host
Rules and native scheduled tasks may still be user-level; record the test-owned
IDs and exact rule for later cleanup. This is not isolation of the whole host.

## What this test covers

- Five-stage copy, independent consent, manual restart where required, Prefill
  and manual entry, website handoff and live completion checks.
- Successful, empty and failed first-check responses and personalized options
  derived from final confirmed data and that one Feed receipt.
- Numbered/multiple selections, edited topics and draft confirmation.

The consumer website popup is not implemented in this repository. Current host
copy therefore retains one brief return phrase; the reserved website variant
is reviewed as copy only. This package does not replace or simulate the website.
It connects to the real service after actual authorization; for a new-user test,
use a test email rather than recovering an existing account. Network actions
remain real and follow normal consent. Do not invoke them merely to test copy.

## Finish the test

Ask the same Agent to remove only the scheduler and exact execution rule created
for this test, identified from its receipts. Then remove the dedicated preview
root if desired. No reinstall is needed for the user's untouched production CLI
and Skills. Local deletion does not undo network publications or account changes.
Keep preview files off the production merge; merge the production-candidate
branch separately after acceptance.
