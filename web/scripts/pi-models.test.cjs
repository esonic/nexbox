const { test } = require('node:test');
const assert = require('node:assert/strict');
const fs = require('node:fs');
const path = require('node:path');
const vm = require('node:vm');
const ts = require('typescript');

function load(file, modules = {}) {
  const exports = {};
  const code = ts.transpileModule(fs.readFileSync(path.join(__dirname, '../src', file), 'utf8'), {
    compilerOptions: { module: ts.ModuleKind.CommonJS, jsx: ts.JsxEmit.ReactJSX },
  }).outputText;
  vm.runInNewContext(code, { exports, require: key => modules[key] || require(key) });
  return exports;
}

const { modelId } = load('lib/agents.ts');
test('legacy native Pi effort suffixes resolve to selectable catalog entries', () => {
  const id = 'openrouter/deepseek/deepseek-v4.1-flash';
  for (const effort of ['off', 'minimal', 'low', 'medium', 'high', 'xhigh', 'max']) {
    assert.equal(modelId('pi', id + ':' + effort), id);
  }
  assert.equal(modelId('pi', id + ':free'), id + ':free');
  assert.equal(modelId('claude', id + ':high'), id + ':high');
});

test('native Pi keeps provider and model variants intact', () => {
  assert.equal(modelId('pi', 'openrouter/vendor/model:free:high'), 'openrouter/vendor/model:free');
  assert.equal(modelId('pi', 'openrouter/vendor/model:free'), 'openrouter/vendor/model:free');
});
