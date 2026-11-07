const { test } = require('node:test');
const assert = require('node:assert/strict');
const fs = require('node:fs');
const path = require('node:path');
const vm = require('node:vm');
const ts = require('typescript');

const source = fs.readFileSync(path.join(__dirname, '../src/lib/oauthWindow.ts'), 'utf8');
const compiled = ts.transpileModule(source, { compilerOptions: { module: ts.ModuleKind.CommonJS } }).outputText;

function setup({ blocked = false, active = true } = {}) {
  const events = [], notices = [], errors = [];
  const popup = {
    closed: false,
    opener: {},
    document: { title: '', body: {}, head: { append() {} }, createElement: () => ({}) },
    location: { replace(url) {
      assert.equal(popup.opener, null, 'external authorization must have no opener');
      events.push(['navigate', url]);
    } },
    close() { popup.closed = true; },
  };
  const window = {
    navigator: { userActivation: { isActive: active } },
    open(...args) {
      assert.equal(window.navigator.userActivation.isActive, true, 'popup opened outside user activation');
      events.push(['open', ...args]);
      return blocked ? null : popup;
    },
  };
  const exports = {};
  vm.runInNewContext(compiled, {
    exports, window, URL,
    require(name) {
      assert.equal(name, 'sonner');
      return { toast: {
        info: (title, options) => notices.push({ title, ...options }),
        error: (title, options) => errors.push({ title, ...options }),
      } };
    },
  });
  return { openOAuthWindow: exports.openOAuthWindow, window, popup, events, notices, errors };
}

test('reserve the tab during the click and navigate after activation expires', async () => {
  const f = setup();
  let resolve;
  const pending = new Promise((r) => { resolve = r; });
  const task = f.openOAuthWindow(() => { f.events.push(['request']); return pending; });
  assert.deepEqual(f.events, [['open', 'about:blank', '_blank'], ['request']]);
  assert.equal(f.popup.opener, null);
  f.window.navigator.userActivation.isActive = false;
  resolve('https://example.com/authorize?state=fixture');
  await task;
  assert.deepEqual(f.events.at(-1), ['navigate', 'https://example.com/authorize?state=fixture']);
  assert.equal(f.notices.length, 0);
  assert.equal(f.errors.length, 0);
});

test('a blocked popup leaves a ready link that opens on a fresh click', async () => {
  const f = setup({ blocked: true });
  await f.openOAuthWindow(async () => 'https://example.com/authorize');
  assert.equal(f.notices.length, 1);
  assert.equal(f.notices[0].duration, Infinity);
  f.notices[0].action.onClick();
  assert.deepEqual(f.events.at(-1), ['open', 'https://example.com/authorize', '_blank', 'noopener,noreferrer']);
});

test('asynchronous authorization discovery never attempts an unprompted popup', async () => {
  const f = setup({ active: false });
  await f.openOAuthWindow(async () => 'https://example.com/authorize');
  assert.equal(f.events.length, 0);
  f.window.navigator.userActivation.isActive = true;
  f.notices[0].action.onClick();
  assert.equal(f.events.length, 1);
});

test('an API failure closes the reserved tab and reports the failure', async () => {
  const f = setup();
  await f.openOAuthWindow(async () => { throw new Error('OAuth unavailable'); }, 'Cannot authorize MCP');
  assert.equal(f.popup.closed, true);
  assert.equal(f.errors[0].title, 'Cannot authorize MCP');
  assert.match(f.errors[0].description, /OAuth unavailable/);
  assert.equal(f.notices.length, 0);
});

test('closing the tab while loading does not lose the authorization link', async () => {
  const f = setup();
  await f.openOAuthWindow(async () => { f.popup.closed = true; return 'https://example.com/authorize'; });
  assert.equal(f.notices.length, 1);
  assert.equal(f.events.filter((e) => e[0] === 'navigate').length, 0);
});

test('invalid authorization schemes cannot navigate the reserved tab', async () => {
  const f = setup();
  await f.openOAuthWindow(async () => 'javascript:alert(1)');
  assert.equal(f.popup.closed, true);
  assert.equal(f.errors.length, 1);
  assert.equal(f.events.filter((e) => e[0] === 'navigate').length, 0);
});
