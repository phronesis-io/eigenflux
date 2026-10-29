# Need budget validation — 2026-10-29

## Decision

Keep the Need experiment off. The implementation passes unit, race and Sort
integration checks, but the requested effect gate fails: LLM4search still has
zero own dense results on the first page. The final page also contains no
A-stock results, so absence of technical content there is not evidence of useful
market coverage. No per-Need minimum quota or model change is included.

## Frozen effect replay

The original private snapshot was captured at 2026-10-29T16:58:27.914000+08:00.
SHA-256: `2784a069cc04aa4d4937ba17c0b678b42a874c6a960693df6b028fa8d2555ce9`.
It contains 843 distinct items and the deployed `rating_lr_20261019_0000_offline`
model. Original contexts, source facts, seen state, cosine evidence, author
classes, injection claims, rules and model remain frozen across all arms.

Consumed history was read once per capture using the production read-only
verification scripts: 992 groups, complete within the 8,192-group bound. Dense
recall was recaptured at k=80 with `ReplayChannelQuery`-equivalent production
queries and the original execution clock. Every returned item was already in
the frozen inventory; no new hydration, cosine or author fact was required.
History and the live ES index were read later than the original snapshot, so
this is a controlled comparison, not an atomic historical reconstruction.

Each cell below is **own dense on first page / all candidates in snapshot /
first-page attribution disagreements**. A disagreement means another eligible
copy has context-local lexical/dense recall when the delivered copy does not,
or higher current-Need cosine at the same recall-evidence tier. This is a
reproducible assignment metric, not a human semantic label for every article.

| Arm | LLM4search | A-stock | Proactive Agent | Swing on page |
| --- | --- | --- | --- | --- |
| Baseline | 0 / 8 / 5 | 1 / 91 / 10 | 1 / 59 / 0 | 16 |
| + Swing 80 and 1/4 page ceiling | 0 / 8 / 3 | 5 / 91 / 4 | 4 / 59 / 0 | 5 |
| + Context cap 300 | 0 / 33 / 3 | 1 / 93 / 3 | 7 / 74 / 0 | 5 |
| + Consumed-group dense exclusion | 0 / 34 / 2 | 0 / 76 / 3 | 11 / 90 / 0 | 5 |
| + Attribution before global ordering | 0 / 23 / 0 | 0 / 44 / 0 | 11 / 133 / 0 | 5 |

All pages contain 20 items. The baseline snapshot has 158; the expanded arms
have 200. Final LLM4search own dense count in the snapshot is 13. The original
Engine and the new Engine with the switch off produced byte-identical full
execution JSON on the frozen snapshot. The count-only capture was repeated
and reproduced the per-Need page and snapshot counts.

## Latency

Twenty measured samples plus one excluded warmup per case, sequential kNN
requests at no more than two per second. The rating case reads the profile and
three Need vectors concurrently against all 843 frozen IDs; this bounds the
inventory more conservatively than the selected eligible pool. Quantiles use
nearest rank. Only aggregate counts and timings leave the production host.

| Operation | p95 ms | Maximum ms |
| --- | ---: | ---: |
| Need kNN: LLM4search (HTTP) | 31.0 | 33.7 |
| Need kNN: A-stock (HTTP) | 57.9 | 156.8 |
| Need kNN: proactive (HTTP) | 34.3 | 44.9 |
| Four parallel rating cosine reads | 58.3 | 67.5 |
| Hydrate: 300 items, isolated forward cache | 14.1 | 15.7 |

kNN p95 passes the 100 ms gate; one A-stock request took 156.8 ms. Rating
cosine samples remain below 300 ms. These samples do not establish performance
under production concurrency or at the maximum 8,192-group exclusion size.

Direct production Hydrate attempted a forward-cache repair, which the read-only
Redis guard rejected before any write. Hydrate was therefore measured using
production read-only PostgreSQL with copied forward facts in isolated miniredis.
Its timing is provisional and does not validate production cache misses or Redis
latency. The new histogram is required to close that rollout observation gap.

## Validation coverage

- Core build and local service startup passed.
- All Sort unit packages and config tests passed.
- Discovery, legacy Sort and config race checks passed.
- Sort integration passed after updating the expected configured source ceilings.
- The legacy e2e suite was attempted but cannot pass with the local empty model
  API key (HTTP 401 in profile/embedding processing); it was stopped after those
  failures.
- Discovery process e2e passed across the full run and targeted rechecks with
  the Need experiment enabled. The three authorization cases needed explicit
  loopback dependency addresses; the Swing fixture now requests four slots to
  retain one item under the quarter-page ceiling. Three CLI-dependent cases
  skipped because no separate CLI binary was supplied.

No production source, model, environment, service or business data was changed.
The example Swing value does not update an existing deployment environment.
The global Swing controls are independent of the default-off Need bucket.
