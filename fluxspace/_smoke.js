// 端到端冒烟测试：mock 掉 DOM 层，让真实状态机跑完整局
global.localStorage = {
  s: {},
  getItem(k) { return this.s[k] || null; },
  setItem(k, v) { this.s[k] = String(v); },
  removeItem(k) { delete this.s[k]; },
};
global.window = undefined;

const fs = require('fs');
const src = fs.readFileSync('_check.js', 'utf8');
const fn = new Function(src + '\nreturn { Runtime, FSM, UI, Store, generateGame, FluxSpaceAPI, estimateMinutes, evalCondition };');
const F = fn();

const calls = { think: 0, sections: 0, settle: 0, branch: 0, summary: 0, actions: 0 };
let lastResult = null;
const mkBody = () => ({ textContent: '', appendChild() {}, insertAdjacentHTML() {}, insertAdjacentText() {}, querySelector() { return null; } });

Object.assign(F.UI, {
  renderGame() {}, flashBoard() {}, flashMetrics() {}, pushSystemLog() {},
  beginThinkCard() { calls.think++; }, beginSection() { calls.sections++; },
  sectionBodyNode() { return mkBody(); }, setSectionText() {}, appendSectionText() {},
  markSectionDone() {}, endThinkCard() {}, pushConcensusTag() {}, autoscrollIfPinned() {},
  promptAction(actions) { calls.actions++; return Promise.resolve(actions[0].id); },
  waitContinue() { return Promise.resolve(true); },
  showRoundSummary() { calls.summary++; return Promise.resolve(true); },
  showBranch() { calls.branch++; }, waitBranchSeen() { return Promise.resolve(true); },
  showSettlement(r) { lastResult = r; calls.settle++; return Promise.resolve(); },
  setPaused() {}, toast() {}, refreshBackendBadge() {},
});

// 跳过所有等待，只验证逻辑链
F.Runtime.gateNoSpeed = async () => {};
F.Runtime.streamSection = async () => {};

const comps = F.Store.state.companions;

(async () => {
  const modes = [
    { mode: 'solo', pick: () => comps.slice(0, 1) },
    { mode: 'coop', pick: () => comps.slice(2, 5) },
    { mode: 'versus', pick: () => comps.slice(6, 10) },
  ];

  for (const m of modes) {
    calls.think = 0; calls.sections = 0; calls.settle = 0; calls.branch = 0; calls.summary = 0;
    const sel = m.pick();
    const g = F.generateGame({
      mode: m.mode, companions: sel, preferType: 'auto', pace: 'standard',
      historyFingerprints: F.Store.fingerprints,
      recentThemes: F.Store.state.history.slice(0, 6).map(h => h.themeId).filter(Boolean),
    });
    F.FSM.current = 'GENERATING';
    const t0 = Date.now();
    await F.Runtime.start(g, m.mode, sel);

    console.log(`\n── ${m.mode.toUpperCase()} ──`);
    console.log('游戏:', g.title, '| 类型:', g.type, '| 轮数:', g.rounds.length, '| 预估:', g.estimatedMinutes, 'min');
    console.log('最终 FSM:', F.FSM.current, '| 结果:', lastResult ? lastResult.outcome : 'null',
                '| 轮次:', F.Runtime.state.round);
    console.log('思维卡:', calls.think, '(应 =', sel.length * g.rounds.length, ')',
                '| 段:', calls.sections, '| 结算提示:', calls.summary, '| 分支:', calls.branch,
                '| 玩家决策次数:', calls.actions);
    console.log('积分 +', lastResult.flux, '| 完成度', lastResult.progress + '%',
                '| 伴侣变化', lastResult.deltas.length,
                '| 逻辑耗时', Date.now() - t0, 'ms');
    const h = F.Store.state.history[0];
    console.log('归档:', h.title === g.title ? '✓' : '✗', '| 指纹', h.fingerprint,
                '| 日志数', h.logCount, '| 有回放', !!h.replay,
                '| 回放timeline', h.replay ? h.replay.timeline.length : 0);
    if (h.replay) {
      const lastEntry = h.replay.logs[h.replay.logs.length - 1];
      console.log('末条日志段数', lastEntry.sections.length, '| 完整度',
        lastEntry.sections.every(s => s.text.length > 10) ? '✓' : '✗');
    }
  }

  console.log('\n── 去重凭据 ──');
  console.log('历史局数:', F.Store.state.history.length);
  const payload = F.FluxSpaceAPI.buildPayload({ companions: comps.slice(0, 2), mode: 'coop', preferType: 'auto', pace: 'standard' });
  console.log('提交给后端的 historyGameIds:', payload.historyGameIds.length);
  console.log('提交给后端的 historyFingerprints:', payload.historyFingerprints.length);
  console.log('recentThemes:', payload.recentThemes);
  console.log('requiredMinMinutes:', payload.requiredMinMinutes);
  console.log('积分余额:', F.Store.flux, '| 存档大小:',
    (JSON.stringify(F.Store.state).length / 1024).toFixed(1), 'KB');
  console.log('\n全部对局结束，无未捕获异常 ✓');
})().catch(e => { console.error('✗ 冒烟测试失败:', e); process.exit(1); });
