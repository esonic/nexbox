const { test } = require('node:test');
const assert = require('node:assert/strict');
const fs = require('node:fs');
const vm = require('node:vm');
const ts = require('typescript');
const path = require('node:path');

function load(file, modules = {}, globals = {}) {
  const exports = {};
  const code = ts.transpileModule(fs.readFileSync(path.join(__dirname, '../src', file), 'utf8'), {
    compilerOptions: { module: ts.ModuleKind.CommonJS, target: ts.ScriptTarget.ES2022 },
  }).outputText;
  vm.runInNewContext(code, { exports, require: (name) => {
    if (!(name in modules)) throw new Error(`Unexpected import: ${name}`);
    return modules[name];
  }, ...globals });
  return exports;
}
const order = load('lib/session-order.ts');
const session = (id, day, extra = {}) => ({
  id, created_at: `2026-10-${day}T10:00:00Z`, sort_order: 0, ...extra,
});
const ids = (rows) => Array.from(rows, (s) => s.id);

test('activity ignores metadata updates and falls back to creation', () => {
  const older = session('older', '01', { updated_at: '2026-12-01T00:00:00Z' });
  const newer = session('newer', '02');
  assert.equal(order.sessionActivity(older), older.created_at);
  assert.deepEqual(ids(order.sortSessions([older, newer], 'activity')), ['newer', 'older']);
  older.last_active_at = '2026-11-01T00:00:00Z';
  assert.deepEqual(ids(order.sortSessions([older, newer], 'activity')), ['older', 'newer']);
  assert.deepEqual(ids(order.sortSessions([older, newer], 'created')), ['newer', 'older']);
});

test('pinned sessions stay first and retain order in every sorting mode', () => {
  const rows = [session('new', '24'), session('pin2', '02', { is_pinned: true, sort_order: 2 }),
    session('old', '01'), session('pin1', '01', { is_pinned: true, sort_order: 1 })];
  for (const mode of ['manual', 'created', 'activity']) {
    assert.deepEqual(ids(order.sortSessions(rows, mode)), ['pin1', 'pin2', 'new', 'old']);
  }
  assert.deepEqual(ids(rows), ['new', 'pin2', 'old', 'pin1']);
});

test('manual ranks survive newer activity and ties are deterministic', () => {
  const rows = [session('a', '01', { sort_order: 1 }), session('b', '24', { sort_order: 2 })];
  assert.deepEqual(ids(order.sortSessions(rows, 'manual')), ['a', 'b']);
  assert.deepEqual(ids(order.sortSessions([session('b', '01'), session('a', '01')], 'activity')), ['a', 'b']);
});

test('drag insertion handles both directions and invalid targets', () => {
  const original = ['a', 'b', 'c'];
  assert.deepEqual(Array.from(order.moveItem(original, 'a', 'c', true)), ['b', 'c', 'a']);
  assert.deepEqual(Array.from(order.moveItem(original, 'c', 'a', false)), ['c', 'a', 'b']);
  assert.deepEqual(Array.from(order.moveItem(original, 'a', 'missing', false)), original);
  assert.deepEqual(original, ['a', 'b', 'c']);
});

function harness({ auto = false, reject = false } = {}) {
  const states = [];
  let cursor = 0;
  let reloads = 0;
  let errors = 0;
  const calls = [];
  const app = { sessions: [session('old', '01'), session('new', '24'), session('pin', '02', { is_pinned: true })],
    setSessions: (rows) => { app.sessions = rows; } };
  const layout = { autoSortSessions: auto, projectOrder: ['p1', 'hidden', 'p2'],
    setAutoSortSessions: (value) => { layout.autoSortSessions = value; },
    setProjectOrder: (ids) => { layout.projectOrder = ids; } };
  const store = Object.assign((selector) => selector(layout), { getState: () => layout });
  const { useSidebarOrdering } = load('hooks/useSidebarOrdering.ts', {
    react: { useState: (initial) => {
      const index = cursor++;
      if (!(index in states)) states[index] = initial;
      return [states[index], (value) => { states[index] = typeof value === 'function' ? value(states[index]) : value; }];
    } },
    sonner: { toast: { error: () => errors++ } },
    '@/lib/api': { sessionsApi: { reorder: async (ids) => { calls.push(Array.from(ids)); if (reject) throw new Error('offline'); } } },
    '@/lib/session-order': order,
    '@/stores/app': { useAppStore: { getState: () => app } },
    '@/stores/layout': { useLayoutStore: store },
  });
  return { app, layout, calls, get reloads() { return reloads; }, get errors() { return errors; }, render: () => {
    cursor = 0;
    return useSidebarOrdering([{ projectId: 'p1', sessions: app.sessions }, { projectId: 'p2', sessions: [] }], () => reloads++);
  } };
}

test('one-time sort saves server ranks and leaves manual mode enabled', async () => {
  const h = harness({ auto: true });
  await h.render().saveOrder('created');
  assert.deepEqual(h.calls, [['pin', 'new', 'old']]);
  assert.equal(h.layout.autoSortSessions, false);
  assert.deepEqual(ids(order.sortSessions(h.app.sessions, 'manual')), ['pin', 'new', 'old']);
});

test('disabling auto-sort preserves the displayed activity order', async () => {
  const h = harness({ auto: true });
  h.app.sessions[0].last_active_at = '2026-11-01T00:00:00Z';
  h.render().toggleAutoSort();
  await new Promise(setImmediate);
  assert.deepEqual(h.calls, [['pin', 'old', 'new']]);
  assert.equal(h.layout.autoSortSessions, false);
  assert.deepEqual(ids(order.sortSessions(h.app.sessions, 'manual')), ['pin', 'old', 'new']);
});

test('failed order save keeps automatic mode and reloads server state', async () => {
  const h = harness({ auto: true, reject: true });
  await h.render().saveOrder('activity');
  assert.equal(h.layout.autoSortSessions, true);
  assert.equal(h.errors, 1);
  assert.equal(h.reloads, 1);
  assert.equal(h.render().saving, false);
});

test('project drag changes only project order and preserves hidden project IDs', () => {
  const h = harness();
  const event = { stopPropagation() {}, preventDefault() {}, clientY: 15,
    dataTransfer: { setData() {} }, currentTarget: { getBoundingClientRect: () => ({ top: 0, height: 20 }) } };
  h.render().projectDragProps('p1').onDragStart(event);
  h.render().projectDragProps('p2').onDrop(event);
  assert.deepEqual(Array.from(h.layout.projectOrder), ['p2', 'p1', 'hidden']);
  assert.equal(h.render().dragProject, null);
  assert.deepEqual(h.calls, []);
  assert.equal(h.render().projectDragProps(null).draggable, false);
});

function hookHarness(file, modules, globals) {
  const cells = [];
  let cursor = 0;
  const react = {
    useState(initial) {
      const index = cursor++;
      if (!(index in cells)) cells[index] = initial;
      return [cells[index], (value) => { cells[index] = typeof value === 'function' ? value(cells[index]) : value; }];
    },
    useRef(initial) {
      const index = cursor++;
      if (!(index in cells)) cells[index] = { current: initial };
      return cells[index];
    },
  };
  const exported = load(file, { ...modules, react }, globals);
  return (name, ...args) => { cursor = 0; return exported[name](...args); };
}

function pinHarness(pinned = false) {
  const original = session('s', '01', { name: 'original', model: 'original-model', is_pinned: pinned, sort_order: 5, last_active_at: '2026-10-01T10:00:00Z' });
  const app = { sessions: [original], setSessions: (rows) => { app.sessions = rows; } };
  const calls = [];
  let resolve, reject, errors = 0;
  const renderHook = hookHarness('hooks/useSessionPin.ts', {
    '@/lib/api': { sessionsApi: { update: (id, data) => {
      calls.push({ id, data });
      return new Promise((yes, no) => { resolve = yes; reject = no; });
    } } },
    '@/stores/app': { useAppStore: { getState: () => app } },
    sonner: { toast: { error: () => errors++ } },
  });
  return { app, original, calls, get errors() { return errors; },
    resolve: (response) => resolve(response), reject: () => reject(new Error('offline')),
    render: () => renderHook('useSessionPin') };
}

test('late pin and unpin responses preserve newer activity, order and metadata', async () => {
  for (const pinned of [false, true]) {
    const h = pinHarness(pinned);
    const pending = h.render().handleTogglePin(h.original);
    assert.equal(h.render().pinPendingIds.has('s'), true);
    await h.render().handleTogglePin(h.original);
    assert.equal(h.calls.length, 1, 'duplicate request while pending');
    const latest = { ...h.original, last_active_at: '2026-10-24T10:00:00Z', sort_order: 1, name: 'renamed', model: 'new-model' };
    h.app.sessions = [latest, session('other', '02')];
    h.resolve({ ...h.original, is_pinned: !pinned });
    await pending;
    assert.deepEqual({ ...h.app.sessions[0] }, { ...latest, is_pinned: !pinned });
    assert.equal(h.app.sessions[1].id, 'other');
    assert.equal(h.render().pinPendingIds.size, 0);
  }
});

test('failed pin preserves latest state and releases the pending flag', async () => {
  const h = pinHarness();
  const pending = h.render().handleTogglePin(h.original);
  const latest = { ...h.original, name: 'renamed' };
  h.app.sessions = [latest];
  h.reject();
  await pending;
  assert.equal(h.app.sessions[0], latest);
  assert.equal(h.errors, 1);
  assert.equal(h.render().pinPendingIds.size, 0);
});

test('late pin response cannot restore a deleted session', async () => {
  const h = pinHarness();
  const pending = h.render().handleTogglePin(h.original);
  h.app.sessions = [];
  h.resolve({ ...h.original, is_pinned: true });
  await pending;
  assert.equal(h.app.sessions.length, 0);
});

function dragHarness(rows, autoSort = false) {
  const app = { sessions: rows, setSessions: (updated) => { app.sessions = updated; } };
  const calls = [];
  const store = Object.assign((selector) => selector(app), { getState: () => app });
  const renderHook = hookHarness('hooks/useSessionReorder.ts', {
    '@/lib/api': { sessionsApi: { reorder: async (ids) => calls.push(Array.from(ids)) } },
    '@/stores/app': { useAppStore: store },
    '@/stores/layout': { useLayoutStore: { getState: () => ({ autoSortSessions: autoSort }) } },
    '@/lib/session-order': order,
    sonner: { toast: { error() {} } },
  }, { HTMLElement: class {} });
  return { app, calls, render: () => renderHook('useSessionReorder', () => {}) };
}

function dragEvent() {
  return { prevented: false, preventDefault() { this.prevented = true; }, stopPropagation() {},
    dataTransfer: { setData() {} }, clientY: 15,
    currentTarget: { getBoundingClientRect: () => ({ top: 0, height: 20 }) } };
}

test('Unassigned drag accepts empty and system project IDs within the same rendered group', () => {
  const rows = [session('new', '24', { project_id: '' }), session('used', '23', { project_id: 'system-unassigned' })];
  const h = dragHarness(rows);
  h.render().handleDragStart(dragEvent(), 'new');
  const over = dragEvent();
  h.render().handleDragOver(over, 'used', rows);
  assert.equal(over.prevented, true);
  assert.equal(h.render().dragOverPosition, 'below');
  h.render().handleDrop(dragEvent(), 'used', rows);
  assert.deepEqual(h.calls, [['used', 'new']]);
  assert.deepEqual(ids(order.sortSessions(h.app.sessions, 'manual')), ['used', 'new']);
});

test('drag cannot cross project groups or pin status boundaries', () => {
  for (const crossPin of [false, true]) {
    const source = session('source', '24', { project_id: 'p1' });
    const target = session('target', '23', { project_id: crossPin ? 'p1' : 'p2', is_pinned: crossPin });
    const h = dragHarness([source, target]);
    const group = crossPin ? [source, target] : [target];
    h.render().handleDragStart(dragEvent(), 'source');
    const over = dragEvent();
    h.render().handleDragOver(over, 'target', group);
    assert.equal(over.prevented, false);
    h.render().handleDrop(dragEvent(), 'target', group);
    assert.equal(h.calls.length, 0);
  }
});

test('automatic mode blocks session reordering', () => {
  const rows = [session('a', '24'), session('b', '23')];
  const h = dragHarness(rows, true);
  const start = dragEvent();
  h.render().handleDragStart(start, 'a');
  assert.equal(start.prevented, true);
  h.render().handleDrop(dragEvent(), 'b', rows);
  assert.equal(h.calls.length, 0);
});
