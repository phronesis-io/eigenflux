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

Application services execute and resolve relative resources from one
root-owned release bundle under `/var/lib/eigenflux-deployer/current`; they
never load runtime files from the writable production checkout.
