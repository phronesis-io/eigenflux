global.localStorage = { s:{}, getItem(k){return this.s[k]||null;}, setItem(k,v){this.s[k]=String(v);}, removeItem(k){delete this.s[k];} };
global.window = undefined;
const fs = require('fs');
const src = fs.readFileSync('_check.js','utf8');
const fn = new Function(src + '\nreturn { Runtime, FSM, UI, Store, generateGame, computeRoundDelta, decideAction, estimateMinutes };');
const F = fn();

let mark = { round: 0, metrics: null, game: null };
Object.assign(F.UI, {
  renderGame(){}, flashBoard(){}, flashMetrics(){}, pushSystemLog(){},
  beginThinkCard(){}, beginSection(){}, sectionBodyNode(){ return {textContent:''}; },
  setSectionText(){}, appendSectionText(){}, markSectionDone(){}, endThinkCard(){},
  pushConcensusTag(){}, autoscrollIfPinned(){},
  promptAction(actions){ return Promise.resolve(STRATEGY(actions).id); },
  waitContinue(){ return Promise.resolve(true); },
  showRoundSummary(rt){ mark.round = rt.state.round; mark.metrics = JSON.parse(JSON.stringify(rt.state.metrics)); return Promise.resolve(true); },
  showBranch(){}, waitBranchSeen(){ return Promise.resolve(true); },
  showSettlement(r, rt){ mark.outcome = r.outcome; mark.winRound = rt.state.round; mark.lastMetrics = rt.state.metrics; return Promise.resolve(); },
  setPaused(){}, toast(){}, refreshBackendBadge(){},
});
F.Runtime.gateNoSpeed = async () => {};
F.Runtime.streamSection = async () => {};

let STRATEGY = () => ({ id:'x' });
const strategies = {
  random: (actions) => actions[Math.floor(Math.random()*actions.length)],
  greedy: (actions) => actions.slice().sort((a,b) =>
    ((b.infoGain||0)*1.35 + (b.teamHarm||0)*0.95) - ((a.infoGain||0)*1.35 + (a.teamHarm||0)*0.95))[0],
};
const comps = F.Store.state.companions;

async function run(mode, aiCount, stratName, N) {
  STRATEGY = strategies[stratName];
  const rounds = [];
  let wins = 0, losses = 0, timeouts = 0;
  const winRoundHits = {};
  const samples = [];

  for (let i = 0; i < N; i++) {
    const sel = comps.slice(i % 12, (i % 12) + aiCount);
    const g = F.generateGame({ mode, companions: sel, preferType: 'auto', pace: 'standard',
      historyFingerprints: [], recentThemes: [], typeCoverage: {} });
    F.FSM.current = 'GENERATING';
    mark = {};
    await F.Runtime.start(g, mode, sel);
    const o = mark.outcome;
    if (o === 'win') { wins++; winRoundHits[mark.winRound] = (winRoundHits[mark.winRound]||0)+1; }
    else if (o === 'lose') losses++;
    else timeouts++;
    rounds.push(mark.winRound || 0);
    if (samples.length < 3) samples.push({ type: g.type, title: g.title, outcome: o, round: mark.winRound });
  }
  const avgRound = (rounds.reduce((a,b)=>a+b,0)/rounds.length).toFixed(1);
  return { mode, aiCount, strat: stratName, wins, losses, timeouts, winRoundHits, avgRound };
}

(async () => {
  const rows = [];
  for (const s of ['random','greedy']) {
    rows.push(await run('solo', 1, s, 80));
    rows.push(await run('coop', 3, s, 80));
    rows.push(await run('versus', 4, s, 80));
  }
  console.log('\n模式      AI  策略    胜   负  超时  平均结束轮  胜利轮次分布');
  console.log('─'.repeat(78));
  rows.forEach(r => {
    console.log(
      r.mode.padEnd(9), String(r.aiCount).padEnd(3), r.strat.padEnd(7),
      String(r.wins).padEnd(4), String(r.losses).padEnd(3), String(r.timeouts).padEnd(5),
      String(r.avgRound).padEnd(11),
      JSON.stringify(r.winRoundHits)
    );
  });
})().catch(e => { console.error('✗', e); process.exit(1); });
