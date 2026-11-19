#!/usr/bin/env python3
"""Executable regression gate for the frozen fluxspace/_balance.js spec.

ADR-0003 registers a 10% non-rounds-out rate as the MVP baseline for the
480-match balance run (the old >= 60% target no longer applies).  This runner
turns that decision into a gate: it executes ``node _balance.js`` inside
fluxspace/ once per repetition, parses the stdout table, asserts completion,
and asserts the aggregate non-rounds-out rate has not collapsed.  Python 3
standard library only, no third-party dependencies.

Format dependency (frozen spec; do NOT "fix" it in this runner):
  _balance.js prints a blank line, one Chinese header line, one U+2500
  box-drawing separator line, then exactly 6 data rows whose columns are:
      mode  aiCount  strat  wins  losses  timeouts  avgRound  winRoundHits(JSON)
  The last column is JSON and may contain spaces; we only ever inspect the
  first 6 whitespace-separated tokens, so a spaced JSON value cannot shift the
  numeric columns.  Any deviation from this format is a hard error - there is
  intentionally NO fallback / degrade / retry logic.

Why the floor is 0.08 and not 0.10
-----------------------------------
The frozen spec is stochastic: _check.js draws from Math.random() and reads
Date.now(), so neither can be seeded without editing the frozen prototype
(forbidden by ADR-0001).  One 480-match run therefore samples the true rate
with a binomial standard error of about 1.37 percentage points.  Measured on
2026-11-06 over ten independent runs: mean 0.1017, min 0.0854, max 0.1208,
stdev 0.0133 - statistically indistinguishable from binomial noise.

A gate at exactly 0.10 would therefore fail roughly 46% of the time on
unchanged code, which is a broken gate, not a strict one.  This runner instead
aggregates --runs repetitions (default 5 = 2,400 matches, se ~0.61pp) and
rejects only a drop below the three-sigma lower bound of the 10% baseline:
0.10 - 3 * 0.0061 = 0.082, rounded down to 0.08.  That bound still catches a
real regression (a true drop to 6% sits beyond 4 sigma) while a false failure
needs a ~3.5 sigma fluctuation.  The 10% figure itself is unchanged; only the
rejection threshold accounts for sampling noise.  Pass --floor 0.10 to demand
the raw point estimate, accepting that it is expected to flake.
"""

import argparse
import shutil
import subprocess
import sys
from pathlib import Path

# ADR-0003: 10% of 480 matches may end in a decision (win/lose) rather than
# rounds-out (timeout).  480 = 6 rows x 80 matches each in the frozen spec.
EXPECTED_ROWS = 6
EXPECTED_TOTAL = 480
RECORDED_BASELINE = 0.10
DEFAULT_RUNS = 5
# Three-sigma lower bound of the 10% baseline over DEFAULT_RUNS * 480 matches.
DEFAULT_FLOOR = 0.08

# Characters used by the _balance.js separator line ("─".repeat(78)) and common
# ASCII equivalents.  A line made up solely of these is skipped as a separator.
SEPARATOR_CHARS = set("─━═-=—_")


def fail(message):
    """Print a clear error and exit non-zero. Never swallow, never fake success."""
    print(f"ERROR: {message}", file=sys.stderr)
    sys.exit(1)


def parse_args(argv=None):
    parser = argparse.ArgumentParser(
        description=(
            "Executable regression gate for fluxspace/_balance.js "
            "(ADR-0003 10% baseline, three-sigma lower bound)."
        )
    )
    parser.add_argument(
        "--floor",
        type=float,
        default=DEFAULT_FLOOR,
        help=(
            "minimum aggregate non_rounds_out_rate required to pass "
            f"(default: {DEFAULT_FLOOR}, the three-sigma lower bound of the "
            f"recorded {RECORDED_BASELINE} baseline; pass 0.10 for the raw "
            "point estimate, which is expected to flake)"
        ),
    )
    parser.add_argument(
        "--runs",
        type=int,
        default=DEFAULT_RUNS,
        help=(
            "number of independent 480-match runs to aggregate "
            f"(default: {DEFAULT_RUNS} = {DEFAULT_RUNS * EXPECTED_TOTAL} "
            "matches; more runs tighten the sampling error)"
        ),
    )
    parser.add_argument(
        "--verbose",
        action="store_true",
        help="also print the raw node stdout/stderr and preflight details",
    )
    args = parser.parse_args(argv)
    if args.runs < 1:
        parser.error("--runs must be >= 1")
    return args


def decode(raw):
    """Decode node's captured bytes.

    Node writes UTF-8 to a pipe.  The only non-ASCII bytes are in the Chinese
    header and the box-drawing separator, both of which we skip; all data rows
    are pure ASCII, so 'replace' can never corrupt a value the gate reads.
    """
    return raw.decode("utf-8", errors="replace")


def parse_rows(stdout_text):
    """Return exactly 6 strictly-parsed data rows, or fail().

    Header and separator lines are skipped explicitly; every remaining line
    MUST be a valid data row.  A malformed row is an error, never silently
    dropped (no degradation).
    """
    data_lines = []
    for raw in stdout_text.splitlines():
        line = raw.strip()
        if not line:
            continue
        if line.startswith("模式"):  # Chinese header row of _balance.js
            continue
        if set(line) <= SEPARATOR_CHARS:  # box-drawing separator row
            continue
        data_lines.append(line)

    if len(data_lines) != EXPECTED_ROWS:
        fail(
            f"assertion 1 failed: expected exactly {EXPECTED_ROWS} data rows "
            f"(480-match run complete), got {len(data_lines)}"
        )

    rows = []
    for i, line in enumerate(data_lines, 1):
        # str.split() with maxsplit keeps the (possibly spaced) JSON column in
        # the last field; parts[:6] are the 6 leading columns we validate.
        parts = line.split(None, 7)
        if len(parts) < 6:
            fail(f"row {i}: need >= 6 columns, got {len(parts)}: {line!r}")
        first6 = parts[:6]
        numeric = {}
        for name, idx in (
            ("aiCount", 1),
            ("wins", 3),
            ("losses", 4),
            ("timeouts", 5),
        ):
            try:
                numeric[name] = int(first6[idx])
            except ValueError:
                fail(
                    f"row {i}: column {idx} ({name}) is not an int: "
                    f"{first6[idx]!r}"
                )
        rows.append(
            {
                "mode": first6[0],
                "aiCount": numeric["aiCount"],
                "strat": first6[2],
                "wins": numeric["wins"],
                "losses": numeric["losses"],
                "timeouts": numeric["timeouts"],
                "avgRound": parts[6] if len(parts) > 6 else "",
                "winRoundHits": parts[7] if len(parts) > 7 else "",
            }
        )
    return rows


def print_summary(runs, wins, losses, timeouts, total, rate, floor, runs_n):
    print("=" * 78)
    print(" check_balance.py - fluxspace/_balance.js 480-match gate (ADR-0003)")
    print("=" * 78)
    print(f"{'run':>3}  {'wins':>5} {'losses':>6} {'timeouts':>8} {'matches':>7} "
          f"{'non_timeout':>11} {'rate':>7}")
    print("-" * 78)
    for i, r in enumerate(runs, 1):
        r_total = r["wins"] + r["losses"] + r["timeouts"]
        r_rate = (r["wins"] + r["losses"]) / r_total if r_total else 0.0
        print(f"{i:>3}  {r['wins']:>5} {r['losses']:>6} {r['timeouts']:>8} "
              f"{r_total:>7} {r['wins'] + r['losses']:>11} {r_rate:>7.4f}")
    print("-" * 78)
    print(f"aggregate over     : {runs_n} run(s) x {EXPECTED_TOTAL} matches")
    print(f"total wins         : {wins}")
    print(f"total losses       : {losses}")
    print(f"total timeouts     : {timeouts}")
    print(f"total matches      : {total}")
    print(f"non_rounds_out     : {wins + losses}")
    print(f"rate               : {rate:.4f}  ({rate * 100:.2f}%)")
    print(f"recorded baseline  : {RECORDED_BASELINE:.4f}  "
          f"({RECORDED_BASELINE * 100:.2f}%)   [ADR-0003 point estimate]")
    print(f"gate floor         : {floor:.4f}  ({floor * 100:.2f}%)   "
          f"[three-sigma lower bound; see module docstring]")
    print(f"meets floor        : {'YES' if rate >= floor else 'NO'}")
    print("-" * 78)


def run_once(repo_root, fluxspace_dir, balance_js, verbose):
    """Execute one 480-match run and return its per-row sums.

    _balance.js reads '_check.js' via a relative path, so the working directory
    MUST be fluxspace/.  Do not change this.
    """
    try:
        proc = subprocess.run(
            ["node", "_balance.js"],
            cwd=str(fluxspace_dir),
            capture_output=True,
        )
    except OSError as exc:
        fail(f"failed to launch node: {exc}")

    stdout_text = decode(proc.stdout)
    stderr_text = decode(proc.stderr)

    if verbose:
        print("[verbose] ---- raw stdout ----")
        sys.stdout.write(stdout_text)
        if stderr_text.strip():
            print("[verbose] ---- raw stderr ----")
            sys.stdout.write(stderr_text)
        print(f"[verbose] returncode = {proc.returncode}")

    # A failed spec run must never be reported as a successful gate.
    if proc.returncode != 0:
        fail(
            f"node _balance.js exited with returncode {proc.returncode}\n"
            f"STDERR:\n{stderr_text}"
        )

    rows = parse_rows(stdout_text)
    sums = {
        "wins": sum(r["wins"] for r in rows),
        "losses": sum(r["losses"] for r in rows),
        "timeouts": sum(r["timeouts"] for r in rows),
    }

    # Assertion 1 (completion): exactly 6 rows (enforced in parse_rows) AND a
    # full 480-match run.  Checked per run so a truncated run cannot hide
    # inside an aggregate.
    run_total = sums["wins"] + sums["losses"] + sums["timeouts"]
    if run_total != EXPECTED_TOTAL:
        fail(
            f"assertion 1 failed: 480-match run incomplete - "
            f"wins+losses+timeouts = {run_total}, expected {EXPECTED_TOTAL}"
        )

    # Assertion 2 (decomposition): timeouts must be consistent with the
    # non-rounds-out count.
    non_rounds_out = sums["wins"] + sums["losses"]
    if sums["timeouts"] != EXPECTED_TOTAL - non_rounds_out:
        fail(
            f"assertion 2 failed: timeouts ({sums['timeouts']}) inconsistent "
            f"with non_rounds_out ({non_rounds_out}) over {EXPECTED_TOTAL} "
            f"matches"
        )
    return sums


def main(argv=None):
    args = parse_args(argv)

    # Resolve the repo root from this file's own location, never from cwd.
    # hub/scripts/check_balance.py -> parents[2] == <repo root>.
    repo_root = Path(__file__).resolve().parents[2]
    fluxspace_dir = repo_root / "fluxspace"
    balance_js = fluxspace_dir / "_balance.js"

    if args.verbose:
        print(f"[verbose] repo_root     = {repo_root}")
        print(f"[verbose] fluxspace_dir = {fluxspace_dir}")
        print(f"[verbose] command        = node _balance.js (cwd=fluxspace/) "
              f"x{args.runs}")

    # Preflight: missing node or missing spec is a hard, explicit error.
    if shutil.which("node") is None:
        fail("node executable not found on PATH")
    if not balance_js.is_file():
        fail(f"_balance.js not found at {balance_js}")

    runs = [
        run_once(repo_root, fluxspace_dir, balance_js, args.verbose)
        for _ in range(args.runs)
    ]

    wins = sum(r["wins"] for r in runs)
    losses = sum(r["losses"] for r in runs)
    timeouts = sum(r["timeouts"] for r in runs)
    total = wins + losses + timeouts
    non_rounds_out = wins + losses
    if total != args.runs * EXPECTED_TOTAL:
        fail(f"aggregate total {total} != {args.runs * EXPECTED_TOTAL}")
    rate = non_rounds_out / total

    # Structured summary (also printed on a floor failure, for diagnosis).
    print_summary(runs, wins, losses, timeouts, total, rate, args.floor,
                  args.runs)

    # Assertion 3: the ADR-0003 baseline, measured against the documented
    # three-sigma lower bound (overridable via --floor).
    if rate >= args.floor:
        print(
            f"PASS: aggregate non_rounds_out_rate {rate:.4f} >= floor "
            f"{args.floor:.4f} over {total} matches in {args.runs} run(s) "
            f"(ADR-0003 baseline {RECORDED_BASELINE:.2f})"
        )
        return 0

    print(
        f"FAIL: aggregate non_rounds_out_rate {rate:.4f} < floor "
        f"{args.floor:.4f} over {total} matches in {args.runs} run(s) "
        f"(ADR-0003 baseline {RECORDED_BASELINE:.2f})",
        file=sys.stderr,
    )
    return 1


if __name__ == "__main__":
    sys.exit(main())
