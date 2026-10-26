// Test the scheduler with deterministic React hooks and API promises, without
// a browser or a test-specific dependency in the application bundle.
const { test } = require('node:test');
const assert = require('node:assert/strict');
const fs = require('node:fs');
const vm = require('node:vm');
const ts = require('typescript');
const path = require('node:path');

const tick = () => new Promise(setImmediate);
function harness() {
  const states = [];
  const refs = [];
  let stateIndex = 0;
  let refIndex = 0;
  let effect;
  let poll;
  const listeners = new Map();
  let calls = 0;
  let branch = 'main';
  let block;
  let failure;
  const api = {
    stats: async () => ({ repository_id: 'repo', files: [] }),
    status: async () => {
      if (failure) throw failure;
      return { staged: [], unstaged: [], untracked: [], ahead: branch === 'main' ? 0 : 2 };
    },
    branches: async () => {
      calls++;
      const current = branch;
      if (block) await block;
      return { current, branches: [{ name: current }] };
    },
    log: async () => [{ hash: branch }],
    repositories: async () => ({ repositories: [{ id: 'repo', branch }] }),
  };
  const document = { hidden: false,
    addEventListener: (name, fn) => listeners.set(name, fn),
    removeEventListener: (name) => listeners.delete(name) };
  const react = {
    useState: (initial) => {
      const index = stateIndex++;
      if (!(index in states)) states[index] = initial;
      return [states[index], (value) => { states[index] = typeof value === 'function' ? value(states[index]) : value; }];
    },
    useRef: (initial) => refs[refIndex++] ?? (refs[refIndex - 1] = { current: initial }),
    useCallback: (fn) => fn,
    useEffect: (fn) => { effect = fn; },
  };
  const source = fs.readFileSync(path.join(__dirname, '../src/hooks/useGitReview.ts'), 'utf8');
  const compiled = ts.transpileModule(source, { compilerOptions: { module: ts.ModuleKind.CommonJS, target: ts.ScriptTarget.ES2022 } }).outputText;
  const exports = {};
  vm.runInNewContext(compiled, { exports, require: (name) => name === 'react' ? react : { diffApi: api, gitApi: api },
    document, window: { setInterval: (fn) => { poll = fn; return 1; }, clearInterval: () => { poll = undefined; } } });
  const render = (scope = { projectId: 'project', repositoryId: 'repo' }) => {
    stateIndex = 0;
    refIndex = 0;
    return exports.useGitReview(scope, true);
  };
  render();
  const cleanup = effect();
  return { render, cleanup, document, listeners, poll: () => poll?.(), calls: () => calls,
    fail: (error) => { failure = error; },
    branch: (value) => { branch = value; }, block: (promise) => { block = promise; } };
}

test('publishes branches, ahead, history and repository summaries together; retains unchanged snapshots', async () => {
  const h = harness();
  await tick();
  const first = h.render().snapshot;
  h.poll();
  await tick();
  assert.equal(h.render().snapshot, first);
  h.branch('feature');
  h.poll();
  await tick();
  const next = h.render().snapshot;
  assert.equal(next.branches.current, 'feature');
  assert.equal(next.status.ahead, 2);
  assert.equal(next.commits[0].hash, 'feature');
  assert.equal(next.repositories.repositories[0].branch, 'feature');
  assert.ok(next.fileRevision > first.fileRevision);
  h.cleanup();
});

test('queues a mutation during polling and reloads file previews on every explicit refresh', async () => {
  const h = harness();
  await tick();
  let release;
  h.block(new Promise((resolve) => { release = resolve; }));
  h.poll();
  h.branch('feature');
  h.render().refresh();
  h.block(undefined);
  release();
  await tick();
  assert.equal(h.calls(), 3);
  assert.equal(h.render().snapshot.branches.current, 'feature');
  const revision = h.render().snapshot.fileRevision;
  h.render().refresh();
  await tick();
  assert.ok(h.render().snapshot.fileRevision > revision);
  h.cleanup();
});

test('pauses hidden polling, refreshes on visibility, and discards responses after cleanup', async () => {
  const h = harness();
  await tick();
  h.document.hidden = true;
  h.poll();
  assert.equal(h.calls(), 1);
  h.document.hidden = false;
  h.listeners.get('visibilitychange')();
  await tick();
  assert.equal(h.calls(), 2);
  const before = h.render().snapshot;
  let release;
  h.block(new Promise((resolve) => { release = resolve; }));
  h.branch('feature');
  h.poll();
  h.cleanup();
  release();
  await tick();
  assert.equal(h.render().snapshot, before);
  assert.equal(h.listeners.size, 0);
});

test('preserves the last complete snapshot after a failed read and recovers on the next round', async () => {
  const h = harness();
  await tick();
  const before = h.render().snapshot;
  h.fail(new Error('temporarily unavailable'));
  h.branch('feature');
  h.poll();
  await tick();
  assert.equal(h.render().snapshot, before);
  assert.equal(h.render().error.message, 'temporarily unavailable');
  h.fail(undefined);
  h.poll();
  await tick();
  assert.equal(h.render().error, null);
  assert.equal(h.render().snapshot.branches.current, 'feature');
  h.cleanup();
});

test('never exposes the previous snapshot under a different project, session or repository', async () => {
  const h = harness();
  await tick();
  for (const scope of [
    { projectId: 'other', repositoryId: 'repo' },
    { projectId: 'project', sessionId: 'worktree', repositoryId: 'repo' },
    { projectId: 'project', repositoryId: 'other' },
  ]) assert.equal(h.render(scope).snapshot, null);
  h.cleanup();
});
