const { test } = require('node:test');
const assert = require('node:assert/strict');
const fs = require('node:fs');
const path = require('node:path');
const ts = require('typescript');
const vm = require('node:vm');
const source = fs.readFileSync(path.join(__dirname, '../src/lib/chatFileLinks.ts'), 'utf8');
const code = ts.transpileModule(source, { compilerOptions: { module: ts.ModuleKind.CommonJS } }).outputText;
const sandbox = { exports: {}, URL, decodeURIComponent, require };
vm.runInNewContext(code, sandbox);
const parse = sandbox.exports.parseChatFileLink;
const plain = (value) => value && JSON.parse(JSON.stringify(value));

test('recognizes workspace paths and line references', () => {
  assert.deepEqual(plain(parse('/home/me/project/a%20b.go:42')), { path: '/home/me/project/a b.go', line: 42 });
  assert.deepEqual(plain(parse('src/app.ts#L12-L18')), { path: 'src/app.ts', line: 12 });
  assert.deepEqual(plain(parse('README.md:9')), { path: 'README.md', line: 9 });
  assert.deepEqual(plain(parse('file:///tmp/a%20b.txt:3')), { path: '/tmp/a b.txt', line: 3 });
});
test('does not intercept web links or invalid references', () => {
  for (const link of ['https://example.com/file.go', 'https://example.com:123', 'mailto:a@example.com', '#heading', '//example.com/a', 'file://other-host/a', '/a.go:0']) assert.equal(parse(link), null, link);
});

test('file labels display line numbers once, including formatted Markdown labels', () => {
  const { createElement } = require('react');
  const suffix = sandbox.exports.chatFileLineSuffix;
  for (const label of ['RcaEventRepository.java:115', 'AnalyticsController.java:500', 'src/app.ts:12:3', 'src/app.ts:12-18', 'src/app.ts#L12-L18']) {
    assert.equal(suffix(label, 115), '', label);
    assert.equal(suffix(createElement('code', null, label), 115), '', label);
  }
  assert.equal(suffix([createElement('strong', { key: 'file' }, 'app.ts'), ':', '12'], 12), '');
  assert.equal(suffix('app.ts', 12), ':12');
  assert.equal(suffix(createElement('code', null, 'app.ts'), 12), ':12');
  assert.equal(suffix('app.ts'), '');
});
