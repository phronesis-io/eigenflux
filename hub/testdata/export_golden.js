'use strict';
// Bridge from the frozen executable specification (fluxspace/_check.js) to the
// Go golden tests. Run from anywhere:  node hub/testdata/export_golden.js
// The prototype is read-only; nothing here modifies it.
const fs = require('fs');
const path = require('path');

const ROOT = path.resolve(__dirname, '..', '..');
const src = fs.readFileSync(path.join(ROOT, 'fluxspace', '_check.js'), 'utf8');

global.localStorage = {
  s: {},
  getItem(k) { return this.s[k] || null; },
  setItem(k, v) { this.s[k] = String(v); },
  removeItem(k) { delete this.s[k]; },
};
global.window = undefined;
Math.random = () => 0.5; // freeze noise, picks and shuffles

const F = new Function(src + '\nreturn {hashStr, fingerprintOf, estimateMinutes, fatigueMul, computeRoundDelta, scoreAction, evalCondition};')();

const hash = ['abc', '', 'deduction|d1|title', '结构指纹', 'x'.repeat(200)]
  .map((s) => ({ in: s, out: F.hashStr(s) }));

const fatigue = [0, 1, 2, 3, 4, 5, 6].map((n) => ({ in: n, out: F.fatigueMul(n) }));

const duration = [];
for (const rounds of [3, 4, 5, 6]) {
  for (const ai of [0, 1, 2, 3, 4]) {
    duration.push({ rounds, ai, out: F.estimateMinutes(rounds, ai) });
  }
}

const fixedGame = {
  type: 'deduction',
  meta: { themeId: 'd1' },
  title: 'T',
  themeText: 'TT',
  rounds: [
    { title: 'R1', reveal: [{ key: 'c1' }], actions: [{ id: 'b' }, { id: 'a' }] },
    { title: 'R2', reveal: [], actions: [{ id: 'c' }] },
  ],
};
const fingerprint = { out: F.fingerprintOf(fixedGame) };

const A = (id, family, risk, eff, extra = {}) => Object.assign({
  id, family, risk, infoGain: 1, teamHarm: 0, contest: 0,
  effects: { metrics: eff },
}, extra);

const metrics = {
  truth: { value: 12, max: 100, good: 'high' },
  suspicion: { value: 22, max: 100, good: 'low' },
  time: { value: 100, max: 100, good: 'high' },
  my: { value: 0, max: 100, good: 'high' },
  their: { value: 0, max: 100, good: 'low' },
  momentum: { value: 0, max: 100, good: 'high' },
};

const deltaCases = [
  {
    name: 'single', mode: 'coop', rounds: 5, usage: {},
    entries: [{ actorId: 'p', isPlayer: true, side: 'A', action: A('a', 'verify', 'low', { truth: 8, suspicion: 2 }) }],
  },
  {
    name: 'three_seats', mode: 'coop', rounds: 5, usage: { verify: 2, evidence: 1 },
    entries: [
      { actorId: 'p', isPlayer: true, side: 'A', action: A('a', 'verify', 'low', { truth: 8, suspicion: 2 }) },
      { actorId: 'c1', isPlayer: false, side: 'A', action: A('b', 'evidence', 'mid', { truth: 14, suspicion: 5 }) },
      { actorId: 'c2', isPlayer: false, side: 'A', action: A('c', 'wait', 'low', { truth: 2, suspicion: -8 }) },
    ],
  },
  {
    name: 'versus_pace3', mode: 'versus', rounds: 3, usage: {},
    entries: [
      { actorId: 'p', isPlayer: true, side: 'A', action: A('a', 'verify', 'low', { truth: 8, suspicion: 2 }) },
      { actorId: 'x', isPlayer: false, side: 'B', action: A('b', 'probe', 'high', { truth: 10, suspicion: 12 }) },
    ],
  },
  {
    name: 'all_same_family', mode: 'coop', rounds: 6, usage: {},
    entries: [1, 2, 3, 4].map((i) => ({
      actorId: 'c' + i, isPlayer: false, side: 'A',
      action: A('a' + i, 'verify', 'low', { truth: 6, suspicion: 1 }),
    })),
  },
];
const roundDelta = deltaCases.map((c) => ({
  name: c.name,
  in: { entries: c.entries, metrics, playerSide: 'A', mode: c.mode, usage: c.usage, rounds: c.rounds },
  out: F.computeRoundDelta(c.entries, metrics, 'A', c.mode, c.usage, c.rounds),
}));

const vector = { risk: 70, logic: 60, empathy: 40, coop: 75, aggression: 30, intuition: 55 };
const baseCtx = {
  companion: { vector },
  mode: 'coop', roundIndex: 1, evidenceCount: 2,
  teamIntent: null, teamFamily: '', playerLastFamily: '',
  affinity: 30, mood: 50,
};
const scoreCases = [
  { name: 'plain', action: A('a_testimony', 'verify', 'low', { truth: 8 }, { infoGain: 2, teamHarm: 1 }), ctx: baseCtx },
  { name: 'risky', action: A('a_probe', 'probe', 'high', { truth: 10 }, { infoGain: 2, teamHarm: -1, contest: 1 }), ctx: baseCtx },
  {
    name: 'team_signal', action: A('a_evidence', 'evidence', 'mid', { truth: 14 }, { infoGain: 3, teamHarm: 1 }),
    ctx: Object.assign({}, baseCtx, { teamIntent: 'a_evidence', teamFamily: 'evidence', playerLastFamily: 'evidence', affinity: 80, mood: 30 }),
  },
  {
    name: 'versus', action: A('a_probe', 'probe', 'high', { truth: 10 }, { contest: 1, teamHarm: -1 }),
    ctx: Object.assign({}, baseCtx, { mode: 'versus', evidenceCount: 0 }),
  },
];
const score = scoreCases.map((c) => ({
  name: c.name,
  in: { action: c.action, ctx: c.ctx },
  out: F.scoreAction(c.action, c.ctx),
}));

const conditionCases = [
  { name: 'win', cond: { all: [{ metric: 'truth', op: '>=', value: 80 }, { metric: 'suspicion', op: '<', value: 70 }] }, metrics },
  { name: 'lose', cond: { any: [{ metric: 'suspicion', op: '>=', value: 92 }, { metric: 'time', op: '<=', value: 0 }] }, metrics },
  { name: 'sentinel_my', cond: { metric: 'truth', op: '>=', value: '__my__' }, metrics },
  { name: 'sentinel_their', cond: { metric: 'suspicion', op: '<', value: '__their__' }, metrics },
  { name: 'unknown_metric', cond: { metric: 'nope', op: '>=', value: 1 }, metrics },
  { name: 'unknown_op', cond: { metric: 'truth', op: '~=', value: 1 }, metrics },
];
const condition = conditionCases.map((c) => ({
  name: c.name,
  in: { cond: c.cond, metrics: c.metrics },
  out: F.evalCondition(c.cond, c.metrics),
}));

const golden = { hash, fatigue, duration, fingerprint, roundDelta, score, condition };
const outPath = path.join(__dirname, 'golden.json');
fs.writeFileSync(outPath, JSON.stringify(golden, null, 2) + '\n');
console.log('wrote ' + outPath);
console.log('hash samples=' + hash.length + ' fatigue=' + fatigue.length + ' duration=' + duration.length +
  ' delta=' + roundDelta.length + ' score=' + score.length + ' condition=' + condition.length);
console.log('fingerprint=' + fingerprint.out);
