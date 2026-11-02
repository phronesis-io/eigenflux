"""Retry Apple's ticket lookup only for a reported transient connection loss."""
import subprocess
import sys
import time


def check(action, app):
    for delay in (3, 8, 20, None):
        result = subprocess.run(
            ["/usr/bin/xcrun", "stapler", action, app],
            stdout=subprocess.PIPE, stderr=subprocess.STDOUT, text=True,
        )
        print(result.stdout, end="", flush=True)
        if result.returncode == 0:
            return 0
        if delay is None or "NSURLErrorDomain Code=-1005" not in result.stdout:
            return result.returncode
        print(f"Apple ticket connection was lost; retrying {action} in {delay}s", flush=True)
        time.sleep(delay)


if __name__ == "__main__":
    if len(sys.argv) != 3 or sys.argv[1] not in ("staple", "validate"):
        sys.exit("expected staple|validate and an app path")
    sys.exit(check(*sys.argv[1:]))
