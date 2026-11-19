const fs = require('fs');
const path = require('path');
const src = fs.readFileSync(path.join(__dirname, '_check.js'), 'utf8');
const fn = new Function(src + '\nreturn { selfTest, generateGame, decideAction, fingerprintOf, lexDict, BUILDERS, Store, COMPANION_SEED, FSM, evalCondition, TUNING, UI, FlxValidate, deriveTalents };');
const F = fn();

console.log('=== 1. 自检 ===');
const r = F.selfTest();
console.log('通过:', r.passed.length, '失败:', r.failed.length);
r.failed.forEach(f => console.log('  ✗', f));
console.log('统计:', JSON.stringify(r.stats, null, 1));

console.log('\n=== 2. 生成 120 局结构 & 时长校准 ===');
let roundCounts = {}, charStats = [], bad = 0;
const fpSet = new Set();
for (let i = 0; i < 120; i++) {
  const g = F.generateGame({ mode: 'coop', companions: [], historyFingerprints: [], recentThemes: [], pace: 'standard' });
  try { F.FlxValidate(g); } catch (e) { bad++; console.log('  结构错误:', e.message); }
  roundCounts[g.rounds.length] = (roundCounts[g.rounds.length] || 0) + 1;
  fpSet.add(g.fingerprint);
  // 用决策引擎跑一次，统计五段日志字数
  const rd = g.rounds[0];
  const metrics = {};
  g.initialScene.metrics.forEach(m => { metrics[m.key] = { ...m }; });
  const c = F.Store.state.companions[i % 20];
  const d = F.decideAction({
    companion: c, mode: 'coop', side: 'A', roundIndex: 0, totalRounds: g.rounds.length,
    metrics, actions: rd.actions, evidenceCount: 1, newClue: rd.reveal[0],
    priorDecision: null, teamIntent: null, teamIntentLabel: null, teamFamily: null,
    opponentPattern: null, playerLastFamily: null, metricsRisk: null,
    affinity: c.affinity, mood: c.mood,
  });
  const chars = d.sections.reduce((s, x) => s + x.text.length, 0);
  charStats.push({ type: g.type, chars, sections: d.sections.length, nonEmpty: d.sections.every(x => x.text.length > 4) });
}
console.log('轮数分布:', roundCounts, '结构错误:', bad);
console.log('指纹唯一:', fpSet.size, '/120');
charStats.sort((a, b) => a.chars - b.chars);
const avg = Math.round(charStats.reduce((s, x) => s + x.chars, 0) / charStats.length);
console.log('日志字数 min/median/max:', charStats[0].chars, charStats[60].chars, charStats[charStats.length-1].chars, 'avg:', avg);
console.log('五段齐全:', charStats.every(x => x.nonEmpty && x.sections === 5));

const CHAR_MS = F.TUNING.THINK_CHAR_MS_STANDARD;
function estimate(aiCount, rounds, secPerAi) {
  const perRound = 20 + aiCount * secPerAi + 35 + 18;
  return (perRound * rounds + 120) / 60;
}
const secPerAi = (avg * CHAR_MS) / 1000 + 4 * 0.26 + 0.42;
console.log('\n每 AI 单条播放秒数:', secPerAi.toFixed(1));
console.log('solo  6轮/1AI:', estimate(1, 6, secPerAi).toFixed(1), 'min');
console.log('coop  5轮/3AI:', estimate(3, 5, secPerAi).toFixed(1), 'min');
console.log('coop  5轮/4AI:', estimate(4, 5, secPerAi).toFixed(1), 'min');
console.log('versus5轮/4AI:', estimate(4, 5, secPerAi).toFixed(1), 'min');

console.log('\n=== 3. 性格分化 ===');
const mk = (v) => ({ id:'t', name:'T', age:20, gender:'f', tags:['冷静'], vector:Object.assign({risk:50,logic:50,empathy:50,coop:50,intuition:50,aggression:50}, v), affinity:30, mood:60, quip:'' });
const bold = mk({risk:95,logic:35,empathy:25,coop:30,intuition:45,aggression:92});
const care = mk({risk:5,logic:92,empathy:92,coop:95,intuition:45,aggression:8});
let diff = 0, N = 300;
const tally = {};
for (let i = 0; i < N; i++) {
  const g = F.generateGame({ mode:'coop', companions:[], historyFingerprints:[], recentThemes:[], pace:'standard' });
  const rd = g.rounds[0];
  const metrics = {}; g.initialScene.metrics.forEach(m => metrics[m.key] = {...m});
  const base = { mode:'coop', side:'A', roundIndex:0, totalRounds:5, metrics, actions:rd.actions, evidenceCount:1, newClue:rd.reveal[0], priorDecision:null, teamIntent:null, teamIntentLabel:null, teamFamily:null, opponentPattern:null, playerLastFamily:null, metricsRisk:null };
  const a = F.decideAction({...base, companion:bold, affinity:30, mood:60});
  const b = F.decideAction({...base, companion:care, affinity:30, mood:60});
  if (a.actionId !== b.actionId) { diff++; tally[g.type] = (tally[g.type]||0)+1; }
}
console.log('极端性格选择差异率:', Math.round(diff/N*100) + '%', tally);

console.log('\n=== 4. 条件判定 ===');
const met = { truth:{value:85,max:100,good:'high'}, suspicion:{value:40,max:100,good:'low'} };
console.log('all 成立:', F.evalCondition({all:[{metric:'truth',op:'>=',value:80},{metric:'suspicion',op:'<',value:70}]}, met));
console.log('any 不成立:', F.evalCondition({any:[{metric:'truth',op:'<=',value:10}]}, met));
console.log('__their__:', F.evalCondition({all:[{metric:'truth',op:'>',value:'__their__'}]}, {...met, their:{value:20,max:100}}));

console.log('\n=== 5. 一条样本日志 ===');
const g = F.generateGame({ mode:'coop', companions:[], historyFingerprints:[], recentThemes:[], pace:'standard' });
const rd = g.rounds[0]; const metrics = {}; g.initialScene.metrics.forEach(m => metrics[m.key] = {...m});
const c = F.Store.state.companions[7];
const d = F.decideAction({ companion:c, mode:'coop', side:'A', roundIndex:0, totalRounds:g.rounds.length, metrics, actions:rd.actions, evidenceCount:1, newClue:rd.reveal[0], priorDecision:null, teamIntent:null, teamIntentLabel:null, teamFamily:null, opponentPattern:null, playerLastFamily:null, metricsRisk:'「X」会继续走向危险区', affinity:c.affinity, mood:c.mood });
console.log('角色:', c.name, c.age, c.tags.join('/'), '| 选:', d.actionLabel, '| 置信', d.confidence);
d.sections.forEach(s => console.log('\n[' + s.label + ']\n' + s.text));
