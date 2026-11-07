/** source: 用户要求将 BeeHub 通用特性 port 到 NexBox，summary: 验证迁移后的浏览器交互与状态保持 **/
/** source: 用户要求彻底检查 Markdown 定时刷新闪烁，summary: 在独立浏览器验证父组件刷新时的 DOM 和滚动状态，以及内容和主题更新 **/
const { test } = require('node:test');
const assert = require('node:assert/strict');
const { execFile } = require('node:child_process');
const { promisify } = require('node:util');
const http = require('node:http');
const path = require('node:path');
const { build } = require('esbuild');

const fixture = String.raw`
import React from 'react';
import { createRoot } from 'react-dom/client';
import { flushSync } from 'react-dom';
import { MarkdownRenderer } from './src/components/markdown/MarkdownRenderer';
let calls = 0;
const themes = [];
window.mermaid = {
  initialize(config) { themes.push(config.theme); },
  async parse() {},
  async render(id, chart) {
    calls++;
    return { svg: '<svg id="' + id + '" width="1400" height="100"><text>' + chart + '</text></svg>' };
  },
};
const fence = String.fromCharCode(96).repeat(3);
const content = [
  '# Preview', '',
  '| First | Second |', '| --- | --- |', '| wide | table |', '',
  fence + 'mermaid', 'graph LR; A-->B', fence, '',
  fence + 'js', 'const value = 1;', fence, '',
  'Inline ' + String.fromCharCode(96) + 'code' + String.fromCharCode(96) + '.', '',
  '- [ ] Keep checkbox state', '',
  '<details><summary>Expand</summary>Details</details>', '', '<img alt="placeholder">',
].join('\n');
const root = createRoot(document.getElementById('root'));
function Paragraph({ children }) { return <p>{children}</p>; }
function render(value = content, options = {}) {
  // Fresh overrides must not remount the built-in code renderer.
  flushSync(() => root.render(<MarkdownRenderer content={value} components={{ p: Paragraph }} {...options} />));
}
function check(condition, message) { if (!condition) throw new Error(message); }
async function settle() { await new Promise(resolve => setTimeout(resolve, 20)); }
async function run() {
  render();
  await settle();
  check(calls === 1 && document.querySelector('svg'), 'Initial Mermaid render missing');
  const tableScroll = document.querySelector('table').parentElement;
  const diagramScroll = document.querySelector('svg').parentElement.parentElement;
  const code = document.querySelector('code.language-js');
  const inline = document.querySelector('p code');
  const details = document.querySelector('details');
  const image = document.querySelector('img');
  const checkbox = document.querySelector('input[type=checkbox]');
  const svg = document.querySelector('svg');
  tableScroll.scrollLeft = 180;
  diagramScroll.scrollLeft = 220;
  details.open = true;
  checkbox.checked = true;
  const range = document.createRange();
  range.setStart(code.firstChild, 0);
  range.setEnd(code.firstChild, 5);
  window.getSelection().removeAllRanges();
  window.getSelection().addRange(range);
  for (let i = 0; i < 4; i++) {
    render();
    await settle();
    check(calls === 1, 'Parent update rerendered Mermaid');
    check(document.querySelector('svg') === svg, 'SVG was replaced');
    check(document.querySelector('table').parentElement === tableScroll && tableScroll.scrollLeft === 180, 'Table scroll reset');
    check(diagramScroll.scrollLeft === 220, 'Diagram scroll reset');
    check(document.querySelector('code.language-js') === code, 'Code block was remounted');
    check(document.querySelector('p code') === inline, 'Inline code was remounted');
    check(window.getSelection().toString() === 'const', 'Code selection was lost');
    check(document.querySelector('details') === details && details.open, 'Details state reset');
    check(document.querySelector('img') === image, 'Image was remounted');
    check(document.querySelector('input') === checkbox && checkbox.checked, 'Checkbox state reset');
  }
  render(content + '\n\nUnrelated paragraph');
  await settle();
  check(calls === 1 && document.querySelector('svg') === svg, 'Unrelated content remounted diagram');
  const changed = content.replace('A-->B', 'A-->C');
  render(changed);
  await settle();
  check(calls === 2 && document.querySelector('svg').textContent.includes('A-->C'), 'Changed diagram did not update');
  document.documentElement.classList.add('dark');
  await settle();
  check(calls === 3 && themes.at(-1) === 'base', 'Theme change did not update diagram');
  render(changed, { enableMermaid: false });
  await settle();
  check(!document.querySelector('svg') && document.querySelector('code.language-mermaid'), 'Disabling Mermaid failed');
  render(changed);
  await settle();
  check(calls === 4 && document.querySelector('svg'), 'Reenabling Mermaid failed');
  document.body.innerHTML = '<pre id="result">PASS: parent updates preserve Mermaid, code selection, table/diagram scroll, details, images and checkbox state; content/theme/enable updates work</pre>';
}
run().catch(error => { document.body.innerHTML = '<pre id="result">FAIL: ' + error.message + '</pre>'; });
`;

test('Markdown keeps DOM state during parent refreshes and still applies real changes', { timeout: 45000 }, async () => {
  const bundled = await build({
    stdin: { contents: fixture, loader: 'tsx', resolveDir: path.join(__dirname, '..') },
    bundle: true, write: false, format: 'iife', jsx: 'automatic',
    alias: { '@': path.join(__dirname, '../src') },
    define: { 'process.env.NODE_ENV': '"production"' },
  });
  const html = '<!doctype html><style>#root{width:400px}table{width:1400px}.overflow-x-auto,.overflow-auto{overflow:auto}</style><div id="root"></div><script>'
    + bundled.outputFiles[0].text.replaceAll('</script', '<\\/script') + '</script>';
  const server = http.createServer((_request, response) => {
    response.writeHead(200, { 'Content-Type': 'text/html' });
    response.end(html);
  });
  await new Promise(resolve => server.listen(0, '127.0.0.1', resolve));
  try {
    const { stdout, stderr } = await promisify(execFile)(process.env.CHROMIUM_PATH || 'chromium', [
      '--headless', '--no-sandbox', '--disable-gpu', '--disable-dev-shm-usage',
      '--dump-dom', '--virtual-time-budget=5000',
      'http://127.0.0.1:' + server.address().port,
    ], { timeout: 35000, maxBuffer: 8 * 1024 * 1024 });
    const result = stdout.match(/<pre id="result">([^<]*)<\/pre>/)?.[1];
    assert.ok(result?.startsWith('PASS:'), result || 'No browser result: ' + stderr.slice(-2000));
    console.log(result);
  } finally {
    await new Promise(resolve => server.close(resolve));
  }
});
