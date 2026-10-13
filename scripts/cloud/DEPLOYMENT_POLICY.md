# EigenFlux Production Deployment Policy

The production checkout is deployment-only. Do not edit files, create commits,
branches or stashes, or push from the production server.

All changes must be developed locally, reviewed in a pull request, and merged
to `main`. Production deployment is performed only through the root-managed
systemd service:

```bash
sudo systemctl start eigenflux-deploy-main.service
journalctl -u eigenflux-deploy-main.service
```

The service refuses a dirty worktree and always fetches and deploys the latest
`origin/main`. A rollback is a root-only code rollback and accepts only a full
commit SHA contained in the current `origin/main`; it does not reverse database
migrations. The official GitHub remote and executable artifact directory are
fixed in the root-owned deployment files; changing the checkout's `origin` or
ignored `build/` files cannot change what the service executes.

Production environment values, the friend-request rate-limit config, and the
pinned GitHub host keys are root-owned under `/etc/eigenflux`. Agents must not
edit `.env`; configuration changes are an explicit root operation followed by
a deployment.

Temporary diagnostic overrides are the single exception to "followed by a
deployment": a root operator may attach a per-instance systemd drop-in that
appends an `EnvironmentFile=` (the procedure in
`docs/dev/configuration.md`, *Temporary SQL trace on one instance*) and
restart only that instance. Such an override must not touch code, the shared
`runtime.env`, or any other instance; it must be removed — by deleting the two
files it created, never `systemctl revert` — before the diagnostic session
ends, and the on/off restarts are visible in `journalctl -u eigenflux-app@<i>`.

Application services execute and resolve relative resources from one
root-owned release bundle under `/var/lib/eigenflux-deployer/current`; they
never load runtime files from the writable production checkout.

## Updating this policy

## API-only releases

For reviewed API-only changes requiring no database migration or RPC rollout,
a root operator may install the main-merged `deploy_main_lib.sh` and
`cloud/systemd/eigenflux-deploy-api.service.tpl` as the root-owned library and
`eigenflux-deploy-api.service`. Start that service to build and restart only
API. It uses the same deployment lock, fixed official remote, clean-checkout
gate and root-managed environment. No migration or other service restart runs.
API has a separate root-owned release/source pointer and per-instance override;
the override is named `zz-api-only.conf` so it loads after the template's
`deployer.conf`. Successful health checks must also match the running
`/proc/<MainPID>/exe` against the target API binary before recording success.
health-check failure restores the previous override and API release. The old
release is retained. A later full deployment advances both release pointers.
Do not run the full installer for this update: it bootstraps every service and
copies environment values. API authorization remains disabled unless separately
configured and approved. Installing this main-merged entrypoint and policy is
permitted before its first API-only deployment.


The authoritative copy is `/etc/eigenflux/DEPLOYMENT_POLICY.md`, installed once
by `scripts/cloud/install_main_deployer.sh`; ordinary deployments do not touch
`/etc/eigenflux`. After a change to this file is merged and deployed, a root
operator refreshes the authoritative copy from the deployed release bundle:

```bash
sudo install -o root -g root -m 0644 \
  /var/lib/eigenflux-deployer/current/source/scripts/cloud/DEPLOYMENT_POLICY.md \
  /etc/eigenflux/DEPLOYMENT_POLICY.md
diff /var/lib/eigenflux-deployer/current/source/scripts/cloud/DEPLOYMENT_POLICY.md /etc/eigenflux/DEPLOYMENT_POLICY.md && echo "policy in sync"
```

## Release annotation hook updates

A root operator may use the eigen-devops release-annotation installer to install
main-merged `deploy_main_lib.sh`, `release_annotations.py`, this policy, and the
retry service/timer under the deployment lock. This narrow installation must
require a clean deployment checkout and does not build or restart application
services. Do not run the full bootstrap installer for this update. The dedicated
Grafana writer credential is root-only and separate from the application runtime
environment. Only successful application health checks create deployment events;
Grafana failures retain their durable outbox for retry.
