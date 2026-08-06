# Main-only production deployment

The root-managed deployer makes `/data/git/eigenflux` a deployment checkout,
not a development workspace. It accepts no user-supplied branch, commit, or
service arguments.

## Production prerequisites

- `/data/git/eigenflux` and its `.git` directory are owned by `root`.
- The worktree is on `main` and is clean.
- The checked-out commit equals the locally fetched `origin/main` when the
  deployer is first installed.
- `git`, `curl`, `jq`, `tar`, `install`, `flock`, and `systemd-run` are installed.
- GitHub branch protection requires changes to `main` to arrive through pull
  requests. The deployer independently verifies that the target commit is
  associated with a merged pull request whose base is `main`.

Install the version-controlled script after its pull request has merged and
the production checkout has been fast-forwarded to that main commit:

```bash
sudo /data/git/eigenflux/scripts/cloud/install_main_deployer.sh
```

Future deployments use the no-argument command:

```bash
sudo /usr/local/sbin/eigenflux-deploy-main
```

The command re-executes itself as a transient systemd service. List and inspect
deployment logs with:

```bash
journalctl --list-boots
journalctl -t eigenflux-deploy-main
journalctl -u 'eigenflux-deploy-*'
```

## Failure behavior

- Dirty worktree, non-`main` branch, divergent/ahead production commit, missing
  merged PR, or a rollback target: stop before build or mutation.
- Build failure: stop before moving production `HEAD`.
- If `origin/main` advances while the isolated build is running, stop before
  moving production `HEAD`; rerun against the new latest commit.
- Migration failure: keep the new `main` checkout, but do not install new
  artifacts or restart services. Fix through another PR and rerun the deployer.
- Restart failure: stop before the aggregate health check; systemd reports the
  exact failed service.
- Health-check failure: return a non-zero deployment status and retain all
  evidence in journald.

The deployer never runs `git reset`, `git clean`, `git stash`, migration down,
or an automatic code rollback. A rollback is a new revert pull request merged
into `main`, followed by another normal deployment.

Run the isolated success and failure drills locally with:

```bash
bash scripts/cloud/test_deploy_main.sh
```
