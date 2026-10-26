const { test } = require('node:test');
const assert = require('node:assert/strict');
const fs = require('node:fs');
const path = require('node:path');
const vm = require('node:vm');
const ts = require('typescript');

// Exercise the actual dialog and page callbacks with deterministic hooks and API
// responses. No test dependencies are included in the application bundle.
function harness(apiOverrides = {}) {
  const states = [];
  let index = 0;
  const calls = [];
  const react = {
    useState(initial) {
      const i = index++;
      if (!(i in states)) states[i] = typeof initial === 'function' ? initial() : initial;
      return [states[i], value => { states[i] = typeof value === 'function' ? value(states[i]) : value; }];
    },
    useRef: () => ({ current: null }),
    useEffect: () => {},
    useCallback: fn => fn,
  };
  const api = new Proxy(apiOverrides, { get: (target, name) => target[name] || (async (...args) => { calls.push([name, ...args]); return []; }) });
  const jsx = (type, props) => ({ type, props });
  const exports = {};
  const source = fs.readFileSync(path.join(__dirname, '../src/pages/MarketplacePage.tsx'), 'utf8');
  const output = ts.transpileModule(source + '\nexport { InstallDialog, PluginCard };', {
    compilerOptions: { jsx: ts.JsxEmit.ReactJSX, module: ts.ModuleKind.CommonJS, target: ts.ScriptTarget.ES2022 },
  }).outputText;
  vm.runInNewContext(output, {
    exports,
    require(name) {
      if (name === 'react') return react;
      if (name === 'react/jsx-runtime') return { jsx, jsxs: jsx };
      if (name === '@/lib/api') return { marketplaceApi: api, projectsApi: { list: async () => [] } };
      if (name === '@/lib/utils') return { cn: (...args) => args.filter(Boolean).join(' ') };
      if (name === 'sonner') return { toast: { success() {}, error() {} } };
      return new Proxy({}, { get: (_, key) => key });
    },
  });
  return { states, calls, render(name, props) { index = 0; return exports[name](props); } };
}
function nodes(node) {
  if (Array.isArray(node)) return node.flatMap(nodes);
  if (!node || typeof node !== 'object') return [];
  return [node, ...nodes(node.props?.children)];
}
function label(node) {
  if (Array.isArray(node)) return node.map(label).join('');
  if (node && typeof node === 'object') return label(node.props?.children);
  return node == null || node === false ? '' : String(node);
}
function button(tree, text) {
  return nodes(tree).find(n => n.type === 'button' && label(n) === text);
}
function checkbox(tree, text) {
  const row = nodes(tree).find(n => n.type === 'label' && label(n) === text);
  return nodes(row).find(n => n.type === 'input' && n.props.type === 'checkbox');
}
const projects = ['A', 'B', 'C'].map(id => ({ id, name: id, git_repo_path: '/project/' + id }));

test('initial and subsequent installations use the same card action', () => {
  for (const installed of [false, true]) {
    const h = harness();
    const tree = h.render('PluginCard', { plugin: { type: 'skill', name: 'test' }, installed, installedScopes: installed ? ['A'] : [], projects });
    assert.ok(button(tree, 'Install'));
    assert.equal(nodes(tree).filter(n => n.type === 'button').length, installed ? 2 : 1);
  }
});

test('existing scopes remain editable; unchanged Save is disabled; clearing allows uninstall without configuration', async () => {
  const h = harness();
  let saved;
  const props = { plugin: { name: 'test', type: 'mcp', env_vars: ['CONFIG_VALUE'] }, projects, installedScopes: ['A', 'B'], saving: false, onClose() {}, onConfirm: async scopes => { saved = [...scopes]; return true; } };
  const render = () => h.render('InstallDialog', props);
  let tree = render();
  assert.equal(button(tree, 'Save').props.disabled, true);
  for (const id of ['A', 'B']) {
    const option = checkbox(tree, id);
    assert.equal(option.props.checked, true);
    option.props.onChange();
    tree = render();
  }
  assert.equal(button(tree, 'Save').props.disabled, false);
  assert.equal(nodes(tree).filter(n => n.type === 'input' && n.props.type === 'password').length, 0);
  await button(tree, 'Save').props.onClick();
  assert.deepEqual(saved, []);
});

for (const kind of ['mcp', 'skill']) {
  test(`${kind}: saving A,B,C installs only C; removing B leaves A untouched`, async () => {
    const h = harness();
    const plugin = { id: 'item', name: 'test', type: kind, command: 'example' };
    h.render('MarketplacePage');
    h.states[3] = projects;
    h.states[kind === 'mcp' ? 4 : 5] = ['A', 'B'].map(project_id => ({ name: 'test', project_id }));
    h.states[11] = plugin;
    const dialog = () => nodes(h.render('MarketplacePage')).find(n => n.type?.name === 'InstallDialog');
    await dialog().props.onConfirm(['A', 'B', 'C'], {});
    const installs = h.calls.filter(c => c[0] === (kind === 'mcp' ? 'installMCP' : 'installSkill'));
    assert.equal(installs.length, 1);
    assert.equal(installs[0][1].project_id, 'C');
    assert.equal(h.calls.filter(c => String(c[0]).startsWith('uninstall')).length, 0);
    h.states[kind === 'mcp' ? 4 : 5] = ['A', 'B'].map(project_id => ({ name: 'test', project_id }));
    await dialog().props.onConfirm(['A'], {});
    const removals = h.calls.filter(c => String(c[0]).startsWith('uninstall'));
    assert.deepEqual(removals, [[kind === 'mcp' ? 'uninstallMCP' : 'uninstallSkill', 'test', 'B']]);
  });
}

test('failed additions retain previous installations and keep the dialog open for retry', async () => {
  const h = harness({ installSkill: async () => { throw new Error('installation failed'); } });
  h.render('MarketplacePage');
  h.states[3] = projects;
  h.states[5] = [{ name: 'test', project_id: 'A' }];
  h.states[11] = { id: 'item', name: 'test', type: 'skill' };
  const dialog = nodes(h.render('MarketplacePage')).find(n => n.type?.name === 'InstallDialog');
  assert.equal(await dialog.props.onConfirm(['C'], {}), false);
  assert.equal(h.calls.some(c => String(c[0]).startsWith('uninstall')), false);
  assert.equal(h.states[11].name, 'test');
});

test('first install validates configuration; global and project selections are mutually exclusive', () => {
  const h = harness();
  const props = { plugin: { name: 'test', type: 'mcp', env_vars: ['CONFIG_VALUE'] }, projects, installedScopes: [], saving: false };
  const render = () => h.render('InstallDialog', props);
  let tree = render();
  checkbox(tree, 'All ProjectsIncludes current and future projects').props.onChange();
  tree = render();
  assert.equal(button(tree, 'Save').props.disabled, true);
  nodes(tree).find(n => n.type === 'input' && n.props.type === 'password').props.onChange({ target: { value: 'example configuration' } });
  tree = render();
  assert.equal(button(tree, 'Save').props.disabled, false);
  checkbox(tree, 'C').props.onChange();
  tree = render();
  assert.equal(checkbox(tree, 'All ProjectsIncludes current and future projects').props.checked, false);
  assert.equal(checkbox(tree, 'C').props.checked, true);
});

test('global to project switch installs the selected project before removing only global scope', async () => {
  const h = harness();
  h.render('MarketplacePage');
  h.states[3] = projects;
  h.states[5] = [{ name: 'test', project_id: '' }];
  h.states[11] = { id: 'item', name: 'test', type: 'skill' };
  const dialog = nodes(h.render('MarketplacePage')).find(n => n.type?.name === 'InstallDialog');
  assert.equal(await dialog.props.onConfirm(['C'], {}), true);
  const changes = h.calls.filter(c => c[0] === 'installSkill' || c[0] === 'uninstallSkill');
  assert.equal(changes[0][0], 'installSkill');
  assert.equal(changes[0][1].project_id, 'C');
  assert.deepEqual(changes[1], ['uninstallSkill', 'test', '']);
});

test('scope label remove buttons target only their own scope and disable during saving', () => {
  const h = harness();
  const removed = [];
  const props = { plugin: { name: 'test', type: 'mcp' }, installed: true, installedScopes: ['', 'A', 'B'], projects, installing: false, onUninstall: id => removed.push(id) };
  const tree = h.render('PluginCard', props);
  const removals = nodes(tree).filter(n => n.type === 'button' && label(n) === '×');
  for (const remove of removals) remove.props.onClick();
  assert.deepEqual(removed, ['', 'A', 'B']);
  const saving = h.render('PluginCard', { ...props, installing: true });
  assert.ok(nodes(saving).filter(n => n.type === 'button').every(n => n.props.disabled));
});
