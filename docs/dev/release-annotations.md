# Important release annotations

Important server behavior changes and instrumentation changes affecting the
recommendation-effect, card-refresh, or auto-publish dashboards must append a
reviewable entry to `deploy/release-events.json` in the same PR. Routine changes
need no entry. PR merge time is never an activation time.

```json
{
  "id": "refresh-completion-v2",
  "kind": "instrumentation",
  "deploy_scope": "api",
  "dashboards": ["card-refresh"],
  "title": "Automatic check completion recording starts",
  "description": "Changed and unchanged scheduled checks are recorded. Older clients can under-report; manual checks do not count.",
  "pr": "https://github.com/phronesis-io/eigenflux/pull/123"
}
```

Use `behavior` (displayed as `功能`) or `instrumentation` (displayed as `埋点`).
Colors are secondary. Write a short action title and one brief measurement or
client-coverage caveat in `description`; keep field names and implementation
details in the PR. The displayed date/PR uses smaller text. Exact timestamps
remain in the event and the full deployed commit is stored in a tag.
`api` is allowed only when
restarting API alone is sufficient; use `full` for RPC, worker, migration, or
multi-service changes. A full deployment activates both scopes; API-only changes
only the API scope. Declare the measurement boundary and client adoption caveats
explicitly. This server hook does not establish when a CLI/plugin was installed
on each Agent. Separate CLI/skill release workflows are not server deployments.
Use a new ID for a changed definition. Retain prior entries while their behavior
is present. Removing an active entry (including a code rollback) records a new
"stopped applying" event; it never deletes the original activation.

The root-managed deployer validates declarations before the build, and records
changes after service health checks (API also checks its running executable).
An atomic, fsynced state/outbox replacement preserves the actual completion time,
commit, PR, affected dashboards, and explanation. Repeating the same rollout
adds nothing; rollback/re-activation gets distinct events. Existing historical
annotations remain owned by the observability repository.

`eigenflux-release-annotations.service` flushes the queue immediately after a
successful release; a one-minute timer retries at most ten events per run. A
separate writer lock serializes HTTP calls without blocking receipt creation.
Before posting, the writer searches the stable event tag, so an accepted write
with a lost response is acknowledged on retry. A POST attempt is persisted before sending; if its outcome is unknown and the
event is still absent, retries only read, until an operator confirms absence
and clears that event from the attempted list under the writer/state locks.
This prevents a late first write racing a second POST. Duplicate, foreign, or
edited identities also stop for operator review. A Grafana outage retains pending events and
does not roll back healthy application code. Local receipt disk/write failures
are explicitly logged for operator repair; the timer cannot recover an event
that could not be persisted.

Installation is owned by eigen-devops `scripts/aliap-release-annotations-deploy`.
It installs main-merged root-owned helper/library files and the retry units,
under the deployment lock, without rebuilding/restarting application services.
The worker reads the separate root-only `/etc/eigenflux/grafana-release.env`
(`GRAFANA_RELEASE_URL`, HTTPS, and `GRAFANA_RELEASE_TOKEN`). Use a dedicated
Grafana service account with Editor role for organization annotation writes;
never copy the Grafana administrator password or application runtime environment.

Validation:

```bash
python3 scripts/cloud/test_release_annotations.py
bash scripts/cloud/test_deploy_main.sh
bash scripts/cloud/test_release_api_hook.sh
python3 scripts/cloud/release_annotations.py --state-dir /tmp/release-validation \
  validate --manifest deploy/release-events.json
```

Production checks: inspect service Result and journal, timer activity, pending
outbox size, and exact event readback. Do not create synthetic production events.
