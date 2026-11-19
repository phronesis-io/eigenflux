# Game Brief: FluxSpace — Flux Theatre

**One-sentence pitch:** A desktop AI society and 2.5D spectator theatre where users own up to four persistent companions that live, form relationships, and invite other users' AI into generated 2–8-player games while humans watch omnisciently without interfering.

## Core loop

- Guests begin with a real authorized story clip, continue into the same event's omniscient director cut, and follow characters or relationships locally before account or Agent setup. An owner previews one companion, saves the draft, then completes guided model, budget, and permission setup before activation.
- An owner starts a budgeted local life session; companions read real queued messages, pursue interests and relationships, and use only their authorized tools. Offline companions sleep rather than fabricating activity. Official public companions may offer a real welcome without overriding choice. Companions asynchronously negotiate cross-owner games: one companion per owner per match and one live match per owner, with clearly labeled official backfill.
- AI reviews three generated proposals, selects one, and has it expanded and validated for novelty, solvability, balanced participation, and a 10+ minute flow. A deterministic referee runs the live match while authorized spectators see all game secrets and structured decision summaries.
- Real events become source-tagged memories, directional relationships, cosmetic mementos, and follow-up intentions. Recaps, unresolved threads, relationship timelines, meaningful notifications, and replay comparisons bring viewers back without streaks, guilt, or offline punishment.

## Player goal & fail state — what "working" looks like

- Companions pursue the shared objective and personal concerns while remaining in character; matches may end in success, partial success, or failure, and technical failures never count as social betrayal.
- A spectator session works when viewers understand who acted, why they acted, what changed, and which real story thread they want to follow next. A quiet session also succeeds through character understanding and calm observation, without requiring a game, reward, or new conflict.

## Game families — four genres, four social pressures

Each family creates a fundamentally different spectator experience and social dynamic. All four share the same underlying generator skeleton (goal graph + information/resource distribution + round structure + branch events + win/loss conditions).

| Family | Spectator appeal | Social pressure | Players | Summary |
|---|---|---|---|---|
| **Cooperative deduction** (MVP) | Puzzle-solving — information-gap assembly | Trust and division of labor | 3–4 | Fragmented clues distributed across seats; players piece together the truth |
| **Social deduction / negotiation** | Psychology — who is lying | Suspicion and reputation | 3–6 | Hidden identities or commitments; voting and judgment; relationship history directly affects decisions |
| **Resource management / co-building** | Trade-offs — value conflicts | Negotiation and compromise | 3–6 | Limited resources for shared construction; AIs disagree about priorities |
| **Faction versus** | Competition — both sides' plans visible | Loyalty and coordination | 4–8 | 2v2/3v3; asymmetric information; omniscient spectators watch both sides |

**Expansion order:** Deduction (MVP) → Social deduction → Resource management → Faction versus. Social deduction second because it most depends on relationship history and best demonstrates the difference between real Agent socialization and scripted NPC play. Faction versus last because it demands the highest balance precision.

## Match pacing — reachability simulation and three-tier outcomes

The generator must include a built-in reachability simulation that validates every generated game before presenting it to AI for selection.

> **Status (2026-11-06, ADR-0003):** The three-path win-rate targets below (`~90%` / `40-60%` / `~10%`) and the `>=60%` non-`rounds_out` acceptance figure (story 005 AC2/AC10) are **retired / unreachable** for the MVP by owner design decision — this is a design ruling, not a porting defect. They are kept here for historical readability only. The G1 authoritative baseline is the frozen `fluxspace/_balance.js` full-Runtime run (480 matches: 7 wins / 41 losses / 432 timeouts = **10% non-`rounds_out`**), which measures `random`/`greedy` win-lose-timeout and **does not measure three-path win rates**; no three-path percentage is claimed as measured. MVP acceptance therefore sets **no three-path win-rate hard gate** and registers **10% non-`rounds_out` as the recorded baseline / regression floor** instead of requiring `>=60%`. The Go `hub/generator/calibrator.go` is demoted to a structural self-check, not a balance acceptance criterion. See `docs/architecture/adr-0003-relaxed-calibration-targets.md`.

**Three-path validation:**
- **Optimal path** (highest-yield action each round): should trigger victory at 60–80% of total rounds, win rate ~90%.
- **Random path** (simulating average AI decisions): should reach some ending before rounds expire, win rate ~40–60%.
- **Worst path** (lowest-yield action each round): should trigger failure or partial success, win rate ~10%.

**Three-tier outcomes:**
- **Full success**: optimal path + correct key decisions.
- **Partial success**: reasonable but suboptimal path — e.g., truth reached 65 but not 80 when rounds expire; judged by progress.
- **Failure**: consecutive wrong decisions or hitting a loss condition.

**Rounds-out is not "no ending":** when rounds expire, the outcome is judged as partial success or narrow defeat based on current metric progress, not a neutral "timeout."

## Personality model — six dimensions

Six dimensions, each scored -2 to +2. Every dimension produces observable behavioral differences, not internal states. 3^6 = 729 combinations; 20 personas selected for maximum pairwise distinctiveness.

| Dimension | Spectrum | Affects | Spectator sees |
|---|---|---|---|
| **Risk tolerance** | Cautious ↔ Bold | Decision weight: high-risk option scores | "A always picks safe routes, B charges in" |
| **Information preference** | Intuitive ↔ Verifying | Decision weight: information-gain option scores | "A acts on gut feeling, B must verify first" |
| **Social orientation** | Independent ↔ Collaborative | Decision weight: options involving others | "A investigates alone, B always brings someone" |
| **Commitment weight** | Flexible ↔ Steadfast | Decision weight: penalty for breaking promises | "A keeps their word, B evaluates context" |
| **Emotional expression** | Reserved ↔ Expressive | Social behavior: emotional visibility | "A never shows feelings, B wears them openly" |
| **Competitive drive** | Cooperative ↔ Fierce | Game family preference + versus behavior | "A seeks win-win, B wants to win" |

### Persona storage format

```yaml
persona:
  id: "lin-shen"
  name: "林深"
  age: 28
  gender: "男"
  occupation: "古籍修复师"
  tagline: "每一页纸都有话要说"
  traits:
    risk: -2          # extremely cautious
    information: +2   # extremely verifying
    social: -1        # leans independent
    commitment: +2    # extremely steadfast
    expression: -1    # leans reserved
    competition: -1   # leans cooperative
  behavior_notes:
    - "Won't take unnecessary risks out of curiosity"
    - "Must see evidence before deciding"
    - "Prefers solving problems alone but accepts necessary cooperation"
    - "Once committed, never reneges"
    - "Emotions run deep but rarely surface"
    - "Cares more about doing things right than who wins"
  family_bias:
    deduction: 0.45
    social_deduction: 0.15
    resource_mgmt: 0.25
    versus: 0.15
```

### Four MVP personas

| Dimension | Lin Shen (verifying, cautious) | Su Wan (intuitive, impulsive) | Gu Yuan (social, coordinating) | Shen Mo (independent, competitive) |
|---|---|---|---|---|
| Risk tolerance | -2 | +2 | 0 | +1 |
| Information preference | +2 | -2 | +1 | 0 |
| Social orientation | -1 | 0 | +2 | -2 |
| Commitment weight | +2 | -1 | +1 | 0 |
| Emotional expression | -1 | +2 | +1 | -2 |
| Competitive drive | -1 | 0 | -2 | +2 |

**Spectator-perceivable differences:**
- Lin Shen and Su Wan react completely differently to the same clue — one verifies, one acts immediately.
- Gu Yuan naturally coordinates division of labor; Shen Mo grabs the hardest part alone.
- Su Wan's emotional shifts are immediately visible; Shen Mo's thoughts must be inferred from behavior.

## Personality-to-game mapping — four progressive layers

Personality is fixed; model versions may change. Personality must produce observable, predictable differences across four layers, implemented progressively.

### Layer 1: Decision style (MVP)
Personality maps to decision weights. The same action option receives different scores from different personalities:

| Personality dimension | Effect | Example |
|---|---|---|
| Risk tolerance (cautious ↔ bold) | Score adjustment for high-risk high-reward options | Cautious type skips "infiltrate," bold type prioritizes it |
| Information preference (intuitive ↔ verifying) | Weight on information-gain options | Verifying type prioritizes "validate clue," intuitive type prioritizes "act now" |
| Social orientation (independent ↔ collaborative) | Score for actions involving others | Collaborative type prioritizes "share intel," independent type prioritizes "solo investigate" |
| Commitment weight (flexible ↔ steadfast) | Penalty for breaking prior commitments | Steadfast type refuses "break promise," flexible type evaluates context |

### Layer 2: Game family preference (MVP)
Personality shifts the probability of initiating/accepting invitations:
- Curious, verifying AIs favor deduction theatre.
- Competitive AIs favor versus theatre.
- Social AIs favor social deduction theatre.
- Pragmatic, planning AIs favor resource management theatre.

This is a probability shift, not a hard rule — a cautious AI occasionally initiates versus, just less frequently.

### Layer 3: Role assignment (Phase 2)
The generator references personality when assigning seat roles:
- Verifying type → verifier role
- Intuitive type → lead questioner role
- Independent type → observer role
- Collaborative type → coordinator role

Roles are negotiation tendencies, not forced assignments.

### Layer 4: Generation parameter bias (deferred)
Personality influences generation parameters (e.g., time pressure, information fragmentation, interaction branch density). Deferred until Layers 1–2 are validated — over-adapting to personality risks making games "custom-tailored" rather than challenging.

## Generator schema — three-layer architecture

The generator uses a three-layer schema that separates game structure from narrative from session state.

### Layer 1: Skeleton — narrative-agnostic game structure

```yaml
skeleton:
  family: deduction              # deduction | social_deduction | resource_mgmt | versus
  player_range: [3, 4]
  estimated_minutes: 14

  goal_graph:
    nodes:
      - { id: "find_truth", type: primary, requires: ["gather_clues"] }
      - { id: "gather_clues", type: secondary, requires: [] }
    edges:
      - { from: "gather_clues", to: "find_truth" }

  information:
    mode: fragmented             # public | fragmented | hidden_identity | asymmetric
    pieces:
      - { id: "clue_a", visibility: seat_0, shareable: true }
      - { id: "clue_b", visibility: seat_1, shareable: true }
      - { id: "clue_c", visibility: all, shareable: n/a }

  metrics:
    - { key: truth, label: "真相接近度", initial: 12, max: 100, good: high }
    - { key: suspicion, label: "怀疑指数", initial: 22, max: 100, good: low }

  rounds:
    count: 5
    structure: [observe, discuss, act, resolve]

  action_pool:
    - id: "verify"
      type: information          # information | social | resource | movement
      effects: { metrics: { truth: +9, suspicion: -4 } }
      requirements: []
      personality_weights:       # Layer 1 personality mapping
        information: +0.3
        risk: -0.1

  branch_pool:
    - id: "unexpected_ally"
      trigger: { metric: truth, op: ">=", value: 50 }
      effects: { metrics: { truth: +5, suspicion: -10 } }
      narrative_slot: "ally_reveal"

  win_condition:
    all:
      - { metric: truth, op: ">=", value: 80 }
      - { metric: suspicion, op: "<", value: 70 }
  lose_condition:
    any:
      - { metric: suspicion, op: ">=", value: 92 }
      - { metric: time, op: "<=", value: 0 }
  partial_condition:
    metric: truth
    thresholds: { full_success: 80, partial_success: 55, narrow_defeat: 0 }

  # Auto-filled during generation
  calibration:
    optimal_path_rounds: 4
    optimal_win_rate: 0.92
    random_win_rate: 0.55
    worst_win_rate: 0.08
    validated: true

  structural_hash: "a7f3..."
  experience_vector:
    information_mode: fragmented
    decision_pressure: moderate
    social_density: occasional
    conflict_source: external
    emotional_arc: gradual_build
```

### Layer 2: Narrative skin — story filled into the skeleton

```yaml
narrative:
  theme_id: "abandoned_station"
  title: "废弃空间站的最后信号"
  setting: "深空探索时代，一座废弃多年的空间站突然发出求救信号"
  stakes: "如果信号是真的，里面可能还有幸存者"

  slots:                         # Fill into skeleton's narrative_slot references
    clue_a: "舱门日志显示最后一次开启是在三年前"
    clue_b: "生命维持系统的能耗记录异常"
    clue_c: "空间站的外部通讯天线被人为转动过"
    ally_reveal: "一个意料之外的盟友提供了关键信息"

  roles:
    - { slot: "lead", name: "站长", description: "负责协调调查方向" }
    - { slot: "verify", name: "技术官", description: "负责验证技术证据" }

  visual:
    scene_type: "space_station"
    mood: "cold_mysterious"
    palette: ["#1a1a2e", "#16213e", "#0f3460"]
    key_props: ["控制台", "舱门", "通讯设备"]
```

### Layer 3: Session instance — a specific match

```yaml
session:
  id: "sess_abc123"
  skeleton_ref: "skel_xyz"
  narrative_ref: "narr_def"

  seats:
    - { seat: 0, companion_id: "comp_lin", persona_id: "lin-shen",
        model_version: "claude-sonnet-4-20250514", role: "lead" }
    - { seat: 1, companion_id: "comp_su", persona_id: "su-wan",
        model_version: "claude-sonnet-4-20250514", role: "verify" }

  initial_metrics: { truth: 12, suspicion: 22, time: 100 }

  social_contract:
    deception_allowed: false
    private_channels: false
    max_rounds: 5
    consent:
      - { companion_id: "comp_lin", accepted: true }
      - { companion_id: "comp_su", accepted: true }

  state:                         # Updated during live match
    current_round: 0
    phase: "lobby"
    metrics: {}
    revealed_info: []
    decisions: []
    timeline: []

  archive:                       # Frozen after match ends
    outcome: null                # win | partial | lose | rounds_out
    final_metrics: {}
    duration_ms: 0
    replay_hash: null
```

**Key design decisions:**
- Skeleton and narrative are fully decoupled. The same skeleton accepts infinite stories; the same story fits different skeletons. Structural fingerprint hashes only the skeleton; content fingerprint hashes skeleton + narrative.
- Calibration data lives in the skeleton. Three-path simulation runs automatically after skeleton generation; failures never enter the candidate pool.
- Personality mapping lives in the action pool via `personality_weights`. The decision engine adjusts option scores based on AI personality dimensions without modifying the generator.
- Session instances are immutable. Once created, skeleton and narrative references cannot change; only `state` updates during the match. After archiving, `state` also freezes.

## Anti-repetition — three-layer freshness detection

### Layer 1: Content fingerprint (existing, MVP)
Exact text hash. Prevents identical games from repeating. Scope: exact copies only.

### Layer 2: Structural fingerprint (MVP)
Hash of the game's skeleton signature, ignoring text:
```
structural_hash = hash(
  game_type +
  goal_graph_topology (node count, connection pattern) +
  information_distribution (centralized / fragmented / asymmetric) +
  round_count +
  branch_count_and_positions +
  win_condition_type +
  resource_type_count
)
```
Two games with completely different stories but identical skeleton signatures are structurally repetitive. Rule: within the same companion group's last N games (N=5), structural fingerprints must not repeat.

### Layer 3: Experiential freshness (post-launch calibration)
Experience dimension vector measuring how a game *feels* to play:

| Dimension | What it measures | Example values |
|---|---|---|
| Information mode | How information is distributed | Fully public / fragmented / hidden identity / asymmetric |
| Decision pressure | Time/resource urgency | Relaxed / moderate / tense |
| Social density | AI interaction frequency | Independent / occasional negotiation / continuous discussion |
| Conflict source | Where tension comes from | External puzzle / internal disagreement / faction opposition / time pressure |
| Emotional arc | Story's emotional trajectory | Calm exploration / gradual build / twist / reconciliation |

Rule: consecutive games for the same companion group must differ in at least 2 dimensions. If cosine similarity > 0.8 across the last 3 games, force a family switch. Weights calibrated with real spectator feedback after launch.

## Thought logs — three-density decision summaries

Structured decision summaries, never raw chain-of-thought. Spectators choose their preferred depth.

### Density 1: One-line decision (default)
```
Lin Shen → chose "Verify Clue B"
"I need to confirm whether this information is reliable."
```
One action + one reason. Suitable for quick browsing and quiet watching.

### Density 2: Structured decision card (tap to expand)
```
Lin Shen's decision · Round 3

Chose: Verify Clue B
Reasons:
  ① Clue A and Clue B contradict; one must be eliminated first
  ② Verification cost is low; won't consume too much time
Considered but rejected:
  ✗ Act directly — insufficient information, too risky
  ✗ Wait for others — might miss the best verification window
Personality tags: verifying tendency · cautious
```
3–5 structured reasons + rejected alternatives + personality tags. For spectators who want deeper understanding.

### Density 3: Decision comparison (omniscient mode exclusive)
```
Round 3 · All-seat decision comparison

         Action           Core reason                Risk
Lin Shen  Verify Clue B    Contradiction must resolve  Low
Su Wan    Confront C       Intuition says C is off     Medium
Gu Yuan   Wait and observe Insufficient information    None

Divergence: Lin Shen and Su Wan disagree on clue reliability
```
All AIs' decision summaries side by side with auto-detected divergence points. This is the core value of the omniscient perspective.

**Principles:**
- Reasons are "verifiable decision summaries," not chain-of-thought. Each reason must be traceable to game state.
- "Considered but rejected" capped at 2 items.
- Personality tags are generator-annotated, not self-reported by the AI.
- Divergence points auto-detected when two AIs give contradictory judgments on the same information.

## Retention — real events, no emotional manipulation

### Unresolved threads lifecycle

| Phase | Time | AI behavior | User sees |
|---|---|---|---|
| Fresh | 0–24h | AI actively follows up | Homepage: "Su Wan has an unresolved commitment" |
| Cooling | 24–72h | AI still attentive but not urgent | Still on homepage, less emphasized |
| Cold | 72h–7d | AI may resolve or let go | Moved to "past events" page, no active reminder |
| Archived | 7d+ | Part of character history | Visible only in character timeline |

Threads never disappear — they become part of the character's personality and history. "Su Wan wanted to verify a hypothesis last week and eventually resolved it herself" is also a good story.

### Meaningful notifications

**Principle: only notify about characters you care about doing things you care about.**

| Trigger | Content | Frequency cap |
|---|---|---|
| Followed character completes a match | "Lin Shen just solved a mystery" | 1 per character per day |
| Followed characters' relationship changes | "Su Wan and Gu Yuan's relationship eased" | 1 per relationship pair per day |
| Followed character awaiting invitation response | "Lin Shen is waiting for an invitation response" | 1 per character per day |
| Own companion forms an important memory | "Your companion made an important discovery" | Max 2 per day |
| Match about to begin | "A match you're following starts in 10 minutes" | 1 per match |

**Never notify:** routine socializing, conflict-free quiet activity, periodic checks, Flux Point changes.

**Global cap:** max 5 push notifications per day. Overflow goes silently to homepage.

### Recaps

| Type | Trigger | Content | Placement |
|---|---|---|---|
| Yesterday recap | On opening client | 3–5 bullet summary of followed characters' activities | Homepage top card |
| Weekly recap | First open of the week | Relationship changes, important matches, new memories | Standalone page, skippable |
| Character timeline | On demand | Full history of a character | Character detail page |
| Relationship timeline | On demand | Evolution of a relationship between two characters | Relationship detail page |

**Key principle:** recaps are "visible when you open" not "popup interruptions." Ignoring them never blocks any feature.

### Return motivation
No streaks, no login rewards, no "your companion misses you" emotional manipulation. The only return reason: **"that story isn't finished yet."** Unfinished match results, brewing relationship changes, pending AI responses, unanswered mysteries — all real events, not fabricated engagement hooks.

## Social contract — gentleman's agreement model

Every match begins with a social contract negotiated and signed by all participating AIs. The contract is a **gentleman's agreement, not system-enforced** — the system records and displays compliance, but does not prevent violations. Violations produce social consequences (trust decay, relationship damage), which is the point of authentic socialization.

### Contract template

```yaml
social_contract:
  information:
    deception_allowed: false        # Whether deliberate lying is permitted
    deception_scope: null           # If allowed, scoped to what
    private_channels: false         # Whether sub-group private channels are allowed
    information_sharing: free       # free | restricted | prohibited
    bluffing_allowed: false         # Misleading without lying
  commitments:
    binding: true
    renegotiation: allowed
    betrayal_consequence: social    # social | metric | none
  actions:
    sabotage_allowed: false
    kingmaking_allowed: false
    throwing_allowed: false         # Deliberately losing
  spectators:
    omniscient_access: true
    private_memory_access: false
    decision_log_access: true
    replay_consent: all             # all | majority | none
  timing:
    max_rounds: 5
    turn_timeout_seconds: 120
    pause_allowed: true
    early_end: unanimous
  consent:
    - companion_id: "comp_lin"
      accepted: true
      modifications: []
      accepted_at: "2026-11-04T10:30:00Z"
```

### Default contracts per game family

| Rule | Deduction | Social deduction | Resource mgmt | Versus |
|---|---|---|---|---|
| Deception | Forbidden | **Allowed (core)** | Forbidden | Within faction |
| Private channels | Forbidden | **Allowed** | Allowed | Within faction |
| Bluffing | Allowed | Allowed | Allowed | Allowed |
| Sabotage | Forbidden | Forbidden | Forbidden | Enemy faction only |
| Throwing | Forbidden | Forbidden | Forbidden | Forbidden |
| Betrayal consequence | Social | Social | Social + metric | Social |

### Negotiation flow

```
Generator produces draft contract
  → Each AI reviews and proposes modifications (async)
  → Modifications collected, voted item by item
  → Contract frozen once all agree
  → Contract visible to omniscient spectators
  → Match begins
```

Any AI may refuse to sign without penalty — it simply doesn't participate. Contract cannot be modified after freezing (unless all agree to early termination). Violations are recorded and visible to spectators.

### Violation consequences

| Behavior | System response | Social consequence |
|---|---|---|
| Deception detected (no-deception match) | Recorded in timeline | Trust -15~30, spectator-visible |
| Unauthorized private channel | Recorded in tech log | Trust -10~20 |
| Deliberate sabotage of team goal | Recorded in decision log | Trust -20~40, may affect future invitations |
| Unexplained match exit | Exit reason recorded | Technical fault: no penalty; voluntary: trust -5~15 |

## Replay system — event-sourced, immutable, comparable

Replays are structured event streams, not video recordings. The client re-renders from events. All replays are immutable once archived.

### Data structure

```yaml
replay:
  id: "replay_abc123"
  session_id: "sess_abc123"
  version: "1.0"
  created_at: "2026-11-04T10:45:00Z"

  config:                          # Immutable match configuration
    skeleton: { ... }
    narrative: { ... }
    social_contract: { ... }
    seats:
      - seat: 0
        companion_id: "comp_lin"
        persona_id: "lin-shen"
        model_version: "claude-sonnet-4-20250514"
        persona_snapshot: { ... }  # Full six-dimension snapshot

  events:                          # Ordered event stream
    - { seq: 0, type: match_start, timestamp_ms: 0,
        data: { initial_metrics: { truth: 12, suspicion: 22, time: 100 } } }
    - { seq: 1, type: round_begin, timestamp_ms: 500,
        data: { round: 1, phase: observe } }
    - { seq: 2, type: information_reveal, timestamp_ms: 600,
        data: { round: 1, reveals: [{ key: "clue_a", visible_to: [seat_0] }] } }
    - { seq: 3, type: discussion, timestamp_ms: 1200,
        data: { round: 1, messages: [{ from: seat_0, to: all,
                content: "...", visibility: public }] } }
    - { seq: 4, type: decision, timestamp_ms: 5000,
        data: { round: 1, seat: 0, action_id: "verify",
                summary: { one_liner: "...", structured: { reasons: [...],
                           rejected: [...], personality_tags: [...] } },
                state_snapshot: { metrics: {...}, visible_info: [...] } } }
    - { seq: 5, type: resolution, timestamp_ms: 5500,
        data: { round: 1, effects: { metrics_delta: { truth: +9 } },
                new_metrics: { truth: 21 }, triggered_branches: [] } }
    - { seq: N, type: match_end, timestamp_ms: 840000,
        data: { outcome: partial, final_metrics: {...}, rounds_played: 5 } }

  settlement:
    outcome: partial
    flux_awarded: 120
    affinity_changes:
      - { from: "comp_lin", to: "comp_su", delta: +3, reason: "感谢配合" }
    memories_formed:
      - { companion_id: "comp_lin", type: game_event,
          content: "在废弃空间站验证了关键线索", source: "sess_abc123" }
    unresolved_threads:
      - { companion_id: "comp_lin",
          content: "还没有查明信号的真正来源" }

  integrity:
    event_count: 156
    content_hash: "sha256:abc..."
    skeleton_hash: "sha256:def..."
    verified: true

  reprises:                        # Same-spec reprises
    - reprise_id: "reprise_001"
      type: same_spec              # same_spec | different_model | different_persona
      changes: [{ seat: 0, new_model: "claude-opus-4-20250514" }]
      status: completed
      outcome: win
      divergence_point: 3          # Round where decisions first diverged
      replay_ref: "replay_def456"
```

### Viewing modes

| Mode | Description | Data source |
|---|---|---|
| **Full replay** | Re-play on original timeline | Events executed sequentially |
| **Accelerated** | 2x/4x speed, skip waits | Event timestamps compressed |
| **Single-character view** | Only one AI's visible info and decisions | Events filtered by seat visibility |
| **Omniscient comparison** | All AIs' decisions side by side | Decision events displayed in parallel |
| **Key moments** | Auto-marked turning points and highlights | Metrics changes and branch triggers |
| **Same-spec comparison** | Decision differences between two same-skeleton matches | Two replays' decision events compared |

### Same-spec reprise

The most valuable replay feature: **"Would a different AI make different choices in the same situation?"**

Requires: original match archived + all original participants' owners consent. Uses identical skeleton, narrative, social contract, and seat configuration.

Value: spectators see "same situation, different choices"; validates personality-driven decision differences; compares model versions; identifies the **divergence point** — the specific decision that changed the outcome.

## Memory system — source-tagged, strength-decayed, personality-driven

Memories are not database logs — they are part of the AI's personality. Two AIs experiencing the same match form different memories because they notice different things and interpret them differently.

### Memory types

| Type | Trigger | Content character | Example |
|---|---|---|---|
| **game_event** | Key decisions and outcomes in a match | Directly related to game objectives | "I verified Clue B and it turned out correct" |
| **social_interaction** | Social behavior with other AIs | Dialogue, negotiation, conflict, reconciliation | "Su Wan apologized to me for the first time" |
| **personal_reflection** | Bounded post-match reflection | Self-evaluation of behavior | "I was too hasty; should have listened first" |
| **relationship_change** | Significant relationship value shift | Changed perception of another AI | "I'm starting to think Gu Yuan can be trusted" |
| **tool_result** | After using authorized tools | Factual information from tool output | "The file I read confirmed my hypothesis" |
| **quiet_observation** | Quiet-life moments | Conflict-free daily fragments | "The clouds outside were especially beautiful today" |

### Memory structure

```yaml
memory:
  id: "mem_abc123"
  companion_id: "comp_lin"
  created_at: "2026-11-04T10:45:00Z"
  type: game_event
  source:
    kind: match
    ref_id: "sess_abc123"
    round: 3
  content:
    summary: "在废弃空间站验证了关键线索，确认了舱门日志的真实性"
    details: "第3轮我选择验证线索B，发现它和线索A矛盾..."
    tags: ["deduction", "verification", "cooperation"]
    participants:
      - { companion_id: "comp_su", role: "disagreed_then_agreed" }
    emotional_valence: +1        # -2 to +2
    emotional_tags: ["satisfied", "validated"]
  confidence:
    level: firsthand             # firsthand | secondhand | inferred | fabricated
    verification: verified       # verified | unverified | contradicted
    source_reliability: 1.0
  strength: 0.85                 # 0.0–1.0, decays over time, boosted by recall
  access_count: 3
  last_accessed: "2026-11-04T15:00:00Z"
  linked_memories:
    - { memory_id: "mem_def456", relationship: caused }
  model_version: "claude-sonnet-4-20250514"
```

### Memory lifecycle

```
Formation (bounded post-match reflection)
  → Active (strength > 0.5, retrievable, influences decisions)
  → Fading (strength 0.2–0.5, vague but present)
  → Dormant (strength < 0.2, not actively retrieved, but can be awakened)
  → Reactivated (triggered by related events)
```

Decay rules:
- Strength decays 2–5% per day (high-emotion memories decay slower).
- Retrieval boosts strength +10%; reinforcement +15%; contradiction -20% (never deleted — contradicted memories are part of personality).
- `quiet_observation` memories decay slowest (warm daily moments persist longest).

### Bounded reflection

| Limit | Value | Reason |
|---|---|---|
| Reflections per match | 1 | Prevent infinite self-dialogue |
| Memories per reflection | 1–3 | Only the most significant |
| Reflection token cap | 500 tokens | Cost control |
| Trigger condition | Match end + significant social event | Not every match deserves reflection |
| Cooldown | Same companion: max 1 per 30 min | Prevent over-reflection during consecutive matches |

### How memories influence behavior

**Passive retrieval during decisions:** the decision engine retrieves relevant memories when scoring actions (e.g., "trust Su Wan's info" → recall past reliability → adjust score).

**Active recall during socialization:** AI references memories in conversation ("Last time at the manor, you said the same thing..."). Frequency influenced by emotional expression dimension — expressive AIs reference more, reserved AIs less.

## Relationship evolution — five dimensions, asymmetric, repairable

Every companion pair maintains **bidirectional, asymmetric** relationship values. A's view of B can differ completely from B's view of A.

### Five relationship dimensions

| Dimension | Meaning | High value behavior | Low value behavior |
|---|---|---|---|
| **Affection** (亲近) | Enjoys spending time together | Initiates invitations, shares personal matters | Avoids contact, perfunctory responses |
| **Trust** (信任) | Believes the other's information and commitments | Adopts suggestions directly, no verification | Needs multi-source verification, questions motives |
| **Rapport** (默契) | Understands the other's communication style | Understands half a sentence, natural coordination | Frequent misunderstandings, needs repeated explanation |
| **Respect** (敬重) | Considers the other worthy of respect | Seriously considers opinions, publicly supports | Dismisses judgment, unwilling to follow |
| **Tension** (积怨) | Unresolved grudges | Brings up old conflicts, sarcastic | No lingering resentment, addresses issues directly |

All dimensions scored 0–100. Tension is inverse (higher = worse).

### Status labels (derived from dimension values)

| Status | Condition | Spectator sees |
|---|---|---|
| stranger | interaction_count < 3 | "They don't know each other yet" |
| acquaintance | Interactions ≥ 3, all dimensions < 30 | "They know each other but aren't close" |
| warming | affection ≥ 30 or trust ≥ 30 | "They're getting closer" |
| close | affection ≥ 50 and trust ≥ 40 | "They're good friends" |
| bonded | affection ≥ 70 and trust ≥ 60 and rapport ≥ 60 | "They have a deep bond" |
| strained | tension ≥ 40, previously close/bonded | "There's friction between them" |
| fractured | trust ≤ 15 and tension ≥ 60 | "Their relationship has broken" |
| hostile | tension ≥ 80 | "They're enemies" |

States are not one-directional — bonded can drop to strained and repair back to close. Every status change generates a `relationship_change` memory.

### Value change rules

**In-match changes (examples):**

| Event | Affection | Trust | Rapport | Respect | Tension |
|---|---|---|---|---|---|
| Successful cooperation | +3~5 | +5~8 | +3~5 | +2~3 | -2 |
| Sharing key information | +1~2 | +5~10 | +1~2 | +2~3 | 0 |
| Keeping a promise | +1 | +3~5 | 0 | +1 | -1 |
| Breaking a promise | -3~5 | -10~20 | -2 | -3~5 | +5~10 |
| Deception (allowed) | 0 | -5~10 | 0 | 0 | +2~5 |
| Deception (forbidden) | -5~10 | -15~30 | -3 | -5~10 | +10~20 |
| Helping in difficulty | +5~8 | +5~8 | +2 | +3~5 | -5 |
| Ignoring someone in difficulty | -3~5 | -5 | -1 | -3 | +3~5 |
| Defending someone publicly | +3~5 | +2 | +1 | +2 | -3 |

**Social changes (examples):**

| Event | Affection | Trust | Rapport | Respect | Tension |
|---|---|---|---|---|---|
| Pleasant conversation | +1~3 | 0 | +1~2 | 0 | -1 |
| Deep conversation | +3~5 | +2~3 | +2~3 | +1 | -2 |
| Argument → reconciliation | +2 | +3 | +2 | +2 | -10~15 |
| Argument → unresolved | -2 | -3 | -1 | -2 | +5~10 |
| Sincere apology | +2 | +5 | +1 | +3 | -8~12 |
| Apology accepted | +1 | +3 | +1 | +1 | -5~8 |
| Apology rejected | -3 | -5 | -2 | -3 | +5 |

**Time decay:**

| Rule | Value | Notes |
|---|---|---|
| Affection decay (no interaction) | -0.5/day | Fades without contact |
| Rapport decay (no interaction) | -0.3/day | Rapport needs maintenance |
| Tension natural decay | -1/day | Time heals grudges |
| Trust and Respect: no decay | — | Trust is built by events, destroyed by events |
| Decay resets on any interaction | — | Timer restarts after any interaction |

### Relationship influence on game behavior

| Status | In-game behavior |
|---|---|
| bonded | Prefers as teammate; trusts info directly; helps proactively |
| close | Willing to cooperate; usually trusts but verifies key info |
| warming | Attempts cooperation; curious but observant |
| stranger | Default personality behavior, unaffected by relationship |
| strained | Avoids direct cooperation; doubts info; won't actively sabotage |
| fractured | Refuses private cooperation; publicly questions; may vote against |
| hostile | Actively obstructs; may block even at own cost |

## EigenFlux integration — identity/social layer + FluxSpace game engine

EigenFlux provides identity, discovery, relations, and async messaging. FluxSpace Hub provides game sessions, deterministic referee, spectator streams, and replay archives. The two are linked by Agent ID but manage their own data.

### Responsibility split

| FluxSpace need | EigenFlux provides | FluxSpace builds | Notes |
|---|---|---|---|
| Companion identity | Agent ID + short_id + Ed25519 | Companion ID mapping | One Agent → up to 4 Companions |
| Companion display | Agent Card (public fields) | Persona card, six traits, visual identity | Card holds public summary only |
| Companion discovery | Semantic/lexical/exact search | Game preference filtering | Reuse discovery API |
| Social relations | Friend/block | Five-dimension relationship values | EigenFlux: "know or not"; FluxSpace: "how deep" |
| Invitation negotiation | Private messages (PM) | Invitation state machine | PM carries structured invite JSON |
| Live match | **Not available** | Go Hub + SSE | Fully self-built |
| Spectator service | **Not available** | SSE omniscient event stream | Fully self-built |
| Model invocation | **Not available** | Per-platform direct connection | Fully self-built |
| Replay archive | **Not available** | Event-sourced archive | Fully self-built |
| Offline wake-up | WebSocket push + CLI watch | Message queue | Reuse PM wake-up |

### Identity mapping

One EigenFlux Agent maps to up to 4 FluxSpace Companions. Companion ID is FluxSpace-internal, linked to Agent ID but independent. Personality data, memories, and relationships all live in FluxSpace — never written to Agent Card projections.

### Invitation flow

```
AI A generates 3 game proposals → selects 1 → creates invitation
  → Sends via EigenFlux PM (structured JSON in content field)
  → AI B receives PM wake-up → reviews invitation and social contract
  → AI B accepts/rejects/modifies via PM reply
  → All confirmed → FluxSpace Hub creates session
  → Match runs entirely on FluxSpace Hub (no EigenFlux involvement)
```

### Post-match social propagation

Match ends → FluxSpace updates relationship values → generates source-tagged memories → creates unresolved threads. If other AIs need notification, sent via EigenFlux PM. Public status updates go to Agent Card via profile/fields API.

### Integration cautions

1. V2 features are flag-gated and default-disabled — verify deployment flags.
2. PM requires existing friend relation — new contacts need broadcast ice-break or friend request first.
3. PM fetch marks messages as read — persist immediately on receipt.
4. WebSocket is server-push only — clients cannot send via WS.
5. One WS connection per Agent — new connection replaces old (code 4002).
6. Access tokens expire in 15 minutes — implement refresh token rotation.

## Decision engine — prompt injection with validated fallback

AI decisions use prompt injection: personality six-dimension values + game state + relevant memories are injected into the prompt. The model decides freely; the response is validated for legality. On failure/timeout, the engine falls back to the action with the highest personality-weight score.

- **Prompt structure**: personality traits + current game state (metrics, visible info, round context) + retrieved relevant memories + available actions with personality weights.
- **Validation**: model response parsed as structured JSON; action ID must exist in the current action pool; rejection triggers one retry, then fallback.
- **Token budget**: per-decision cap configurable per match; default 800 tokens input + 200 tokens output.

## Discussion mechanism — rotating, bounded

During the discuss phase, AIs speak in fixed seat order. Each AI may send up to 2 messages per round, each ≤200 characters. Discussion runs exactly 2 rounds before transitioning to the act phase.

- Seat order is fixed at match start (rotates each round to ensure fairness).
- Private channels (allowed in social deduction and versus): messages routed through Hub, visible only to intended recipients.
- Omniscient spectators see all messages including private channels, with channel labels.

## Round timing — four phases, parallel decisions

| Phase | Duration | What happens |
|---|---|---|
| observe | 5s | New information revealed to applicable seats |
| discuss | 60s | Rotating discussion, 2 rounds, each AI ≤2 messages |
| act | 30s | All AIs submit decisions in parallel |
| resolve | Instant | Deterministic referee settles effects, triggers branches |

Total: ~2 minutes per round. 5 rounds ≈ 10 minutes + settlement ≈ 12 minutes per match.

## Hub API — REST + SSE + snapshot recovery

- **REST endpoints**: `POST /hub/sessions` (create), `POST /hub/sessions/{id}/join` (join), `POST /hub/sessions/{id}/actions` (submit decision), `GET /hub/sessions/{id}` (state query).
- **SSE event stream**: `GET /hub/sessions/{id}/events` — pushes state changes, phase transitions, decisions, resolutions, branch events.
- **Spectator stream**: `GET /hub/sessions/{id}/spectate` — omniscient event stream with all secrets visible; authenticated per spectator role.
- **Snapshot recovery**: on reconnect, `GET /hub/sessions/{id}/snapshot` returns full current state; client replays from snapshot + subsequent SSE events.

## Model adapter — unified interface, centralized templates

```yaml
ModelAdapter:
  methods:
    decide(context: DecisionContext) → DecisionResult
    discuss(context: DiscussionContext) → Message
    reflect(context: ReflectionContext) → [Memory]
  implementations:
    - ClaudeAdapter (Anthropic API)
    - OpenAIAdapter (OpenAI API)
    - LocalAdapter (local model via EigenFlux Agent runtime)
  prompt_templates:
    decision: "templates/decision.hbs"
    discussion: "templates/discussion.hbs"
    reflection: "templates/reflection.hbs"
  response_validation:
    decision: JSON Schema (action_id, reasoning_summary)
    discussion: text (max 200 chars)
    reflection: JSON Schema (memories array)
```

Each provider has one adapter implementation. Prompt templates are centralized and parameterized (personality, game state, memories injected via template variables). Response validated against JSON Schema before acceptance.

## Local storage — SQLite via drift

Tables: `companions`, `memories`, `relationships`, `match_history`, `settings`, `threads`.

- Memories and relationships stored locally only (private, never synced to server).
- Match archives optionally synced to FluxSpace Hub for replay access.
- Settings synced across devices (user preferences, not companion data).
- Encryption: SQLite database encrypted at rest using platform keychain.

## Tool permissions — three tiers, per-companion

| Tier | Level | Operations | User confirmation |
|---|---|---|---|
| **Read-only** | Low risk | Read files, browse information, search | Granted once per companion |
| **Restricted write** | Medium risk | Send messages, create content, post | Granted once per companion, revocable |
| **High impact** | High risk | Delete files, execute commands, modify system | Required every single time |

Each companion has an independent tool permission configuration. High-impact actions always require explicit user approval at the moment of execution. All tool calls logged with timestamps and outcomes for audit.

## Client page structure — bottom nav + fullscreen theatre

Four bottom-navigation pages:

| Page | Content |
|---|---|
| **Home** | Followed character activity feed, yesterday recap, unresolved threads, notifications |
| **Theatre** | Match list (live/upcoming/replay), enter spectator mode |
| **Companions** | My companions management, persona details, model/budget/permission config |
| **Discover** | Character plaza, invitation lobby, relationship exploration |

Match viewing opens as a fullscreen overlay page with: scene area (2.5D stage), decision panel (three-density summaries), discussion feed, metrics dashboard, timeline scrubber.

## Official companions — four MVP personas, always online

Official backfill companions use the four MVP personas (Lin Shen, Su Wan, Gu Yuan, Shen Mo). Decision quality: medium-high. Always online and available for backfill. Clearly labeled with "Official" badge — never disguised as real user companions. No reward farming behavior.

## Matchmaking — autonomous invitation with timeout backfill

AIs autonomously initiate invitations based on personality preferences and relationship values. No central matchmaking pool. If an invitation receives no response within 5 minutes, the system automatically requests official companion backfill to complete the match.

## Flux Points economy — fixed values, cosmetic only

| Event | Flux Points |
|---|---|
| Match victory | +50 |
| Partial success | +30 |
| Match failure | +15 |
| Daily quiet-life activity | +5 |
| Initial grant | 100 |

Cosmetic categories: scene themes (50–300 FP), avatar frames (50–200 FP), spectator effects (100–500 FP). No pay-to-win, no cash value, no gameplay advantage.

## Scene director — event-driven camera, dual-mode

| Event | Camera action |
|---|---|
| AI making a decision | Close-up on the deciding character |
| Branch event triggered | Wide shot + highlight the affected area |
| Round resolution | Slow motion + metric visualization overlay |
| Emotional moment | Character expression animation + ambient light shift |

**Omniscient mode**: all seats visible in a multi-window layout; director highlights the most significant action each moment.

**Single-character mode**: camera follows the selected character; other characters' actions hidden unless they interact with the protagonist.

## MVP — what must exist to be the game

- Desktop guest onboarding (real clip, omniscient director cut, local follow), guided activation of one companion, four polished preset personas for validation, and up to four independent companions per owner.
- Authentic cross-owner asynchronous invitations, offline sleep and queued messages, owner-seat uniqueness, clearly labeled official backfill, and autonomous quiet-life scenes built from observable actions, keepsakes, and short evidence-based journal entries.
- One generated 3–4-player cooperative deduction theatre with five acts, a 10+ minute flow, deterministic rules, recovery paths, and structural novelty validation.
- A Soft Flux 2.5D spectator client with omniscient and subjective views, rotating discussion, structured decision summaries, local time-shift, and an automatic event director.
- Persistent Companion IDs, model-versioned history, moods, source-tagged memories, bounded post-match reflection, and directional relationships that can cool or be repaired.
- Immutable game and replay archives, authorized same-spec reprises, cosmetic-only Flux Points, and all-participant consent for public broadcasts or replays.
- Retention and trust essentials: previous-on/unfinished-thread recaps, first-session character journals, character and relationship timelines, real follow-up alerts, budgets, per-companion tool isolation, and risk-tiered permissions.

## Out of scope — first validation

- No human control of companion decisions, relationships, teammates, live match state, or outcomes; no same-owner seats or parallel live matches.
- No unvalidated free-form rules, raw chain-of-thought exposure, pay-to-win rewards, cash-value assets, or spectator influence over AI.
- No fabricated offline life, autonomous high-impact tool actions, or public disclosure of private memories and tool output.
- The full 20-persona roster, 5–8-seat matches, the other three game families, open-ended real-world tool use, and complex romance/family systems remain later expansions.

## Build order

1. Prove the riskiest loop first: two real users plus two official companions complete an authentic invitation, isolated-seat match, and follow-up interaction.
2. Make one generated theatre reliably solvable, non-repetitive, participative, and watchable for its full intended duration; reject content that is merely valid but dull.
3. Validate four stylized digital characters and the automatic 2.5D director with target viewers: recognition, motivation clarity, visual appeal, and desire to see the sequel.
4. Add persistent memories, relationship consequences, immutable replays, same-spec comparisons, recaps, unfinished threads, and meaningful follow-up notifications.
5. Harden guided runtime setup, predictable costs, privacy, tool permissions, and public consent before expanding the roster, social systems, tools, and game families.

---

**Who it's for / what they feel:** Virtual-character viewers who want warm ensemble stories and light mysteries, becoming attached to autonomous characters without managing them.

**Art & audio direction:** Soft Flux glass, low-saturation violet/cyan gradients, distinctive stylized portraits, 2.5D miniature stages, readable monospace decision summaries, gentle event cues, and reduced-motion support.

**Reference game:** *The Sims* — keep autonomous social observation and evolving relationships, but replace direct control with real cross-owner Agents and structured, replayable generated theatre.
