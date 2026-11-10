# Need snapshot distribution contract

Product SHA: `56c553200652e84d7ac25f5821697328e02c43a0`.
Production baseline: `6aefaa0` (record full SHA in provenance/acceptance record).

| Boundary | Authority and verification |
| --- | --- |
| Need scheduling, limits, submission and receipts | Product source; CLI regression suite, legacy writer reproduction and HTTP subprocess acceptance |
| Identity/Home/server, authorization, scheduler and owner-visible output | Unchanged product CLI/Skills and installer; nondefault Home/server fixture and exact scheduler prefix assertions |
| Distribution source | Compiled snapshot CDN in cmd/skills.go and internal/skills/types.go; hostile environment and fresh-process tests |
| Skill integrity and rollback | Original signature/hash/preservation/atomic install; same-key sequence checks remain, separate signing authorities have independent sequences |
| Exact installed Skill revision | Distribution-only sync postcondition rejects old/edited/mismatched artifacts; no silent production fallback |
| Installation and repair entry | Only product install.md URLs and onboarding link/patch version change; normalized-entry equality and staged file audit |
| CLI identity | Hash checks for equal semantic versions and actual PATH executable; complete signed native packages |
| Notification helper | Existing product-version notarized App restored read-only; signed receipt, Apple identity and ticket verification before public build |
| Publication | New snapshots/<40-hex product>/<40-hex distribution> only; refuses nonempty prefix and conditional writes prevent overwrite; entry last, all remote bytes checked |

The tool-only clirelease overlay accepts a subset of platform artifacts when
signing this snapshot, while retaining all integrity checks for included files.
Supported published targets are macOS/Linux arm64/amd64. Windows entry fails
explicitly; Windows installation is not certified by this distribution.
No product command handler, host approval, runtime Home, scheduler prompt, API
endpoint, or account is changed by distribution overlays. The backend remains
the real EigenFlux service. Signing seeds are temporary, never public artifacts.

Before product merge, remove distribution scaffolding and URL/key overlays from
any production change. Retain the product fixes and regression tests. Restore a
user installation only through owner-authorized installation/channel procedures;
never automatically delete credentials, Skills, bindings or scheduled tasks.
