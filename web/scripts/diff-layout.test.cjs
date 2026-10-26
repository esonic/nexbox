const { test } = require('node:test');
const assert = require('node:assert/strict');
const fs = require('node:fs');
const path = require('node:path');

const source = fs.readFileSync(path.join(__dirname, '../src/components/diff/DiffView.tsx'), 'utf8');

test('keeps the commit controls visible while a long file list scrolls independently', () => {
  assert.match(source, /shrink-0 min-h-0 overflow-hidden border-r flex flex-col/);
  assert.match(source, /flex-1 min-h-0 overflow-y-auto/);
  assert.match(source, /shrink-0 border-t p-2 space-y-2/);
});

test('file navigation scrolls only the diff pane, not ancestor panels', () => {
  assert.match(source, /scrollWithinContainer\(container, target, "start"\)/);
  assert.doesNotMatch(source, /scrollIntoView\(/);
});
