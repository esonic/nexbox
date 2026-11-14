/** source: 用户要求消息详情懒加载，summary: 验证加载、重试及最新消息文件变更区的折叠状态 **/
const {test} = require('node:test');
const assert = require('node:assert/strict');
const {execFile} = require('node:child_process');
const {promisify} = require('node:util');
const http = require('node:http');
const path = require('node:path');
const {build} = require('esbuild');
const mocks = {
 '@/lib/api': `export const calls=[]; let fail=true; export const filesApi={}, sessionsApi={}; export const messagesApi={getDetails:async(id,section)=>{calls.push([id,section||'work']); if(id==='old'&&!section&&fail){fail=false;throw Error('offline')} return {parts:section?[{type:'tool_use',toolCallId:'e',toolName:'Edit',input:JSON.stringify({path:id+'.txt'})},{type:'tool_result',toolCallId:'e',diff:'diff'}]:id==='bare'?[{type:'text',content:'Answer bare'},{type:'tool_use',toolCallId:'e',toolName:'Edit',state:'complete',input:JSON.stringify({path:'bare.txt',new_string:'full contents'})},{type:'tool_result',toolCallId:'e',diff:'diff'}]:[{type:'thinking',content:'secret work'},{type:'text',content:'Answer '+id}]}}};`,
 '@/stores/app': `export const useAppStore=(s)=>s({currentProject:null}); useAppStore.getState=()=>({});`,
 '@/stores/preferences': `export const CHAT_FONT_SIZE_PX={md:14}; export const usePreferences=(s)=>s({chatFontSize:'md'});`,
 '@/hooks/useThemeMode': `export const useThemeMode=()=> 'light';`,
 '@/lib/highlight': `export const resolveLang=()=>null, ensureLangRegistered=()=>false, highlightCode=()=>'';`,
 '@/components/chat/WidgetBlock': `export const WidgetBlock=()=>null;`,
 '@/plugins/task/IssueRow': `export const IssueRow=()=>null;`,
};
const fixture = `
import React from 'react'; import {createRoot} from 'react-dom/client'; import {flushSync} from 'react-dom';
import {ChatMessage} from './src/components/chat/ChatMessage'; import {calls} from '@/lib/api';
const root=createRoot(document.getElementById('root'));
const msg=id=>({id,session_id:'s',role:'assistant',created_at:'2026-01-01T00:00:00Z',metadata:{durationMs:1000},lazy_work:true,file_change_count:1,parts:[{type:'tool_group',count:1},{type:'text',content:'Answer '+id}]});
const old=msg('old'), latest=msg('latest'), newest=msg('newest');
const bare={...msg('bare'),lazy_work:false,lazy_files:true,parts:[{type:'text',content:'Answer bare'},{type:'tool_use',toolCallId:'e',toolName:'Edit',state:'complete',lazyDetails:true,input:JSON.stringify({path:'bare.txt'})},{type:'tool_result',toolCallId:'e'}]};
const render=third=>flushSync(()=>root.render(<>{[bare,old,latest,...(third?[newest]:[])].map((m,i,a)=><div id={m.id} key={m.id}><ChatMessage message={m} isLatest={i===a.length-1} latestMessageId={a[a.length-1].id}/></div>)}</>));
const wait=()=>new Promise(r=>setTimeout(r,60)); const check=(v,m)=>{if(!v)throw Error(m)};
const button=(id,text)=>[...document.querySelectorAll('#'+id+' button')].find(b=>b.textContent.includes(text));
async function run(){render(false); await wait();
check(calls.length===1&&calls[0][0]==='latest'&&calls[0][1]==='files','latest file load');
check(!document.querySelector('#old').textContent.includes('old.txt')&&document.querySelector('#latest').textContent.includes('latest.txt'),'initial expansion');
button('bare','1 file').click(); await wait(); check(calls.some(c=>c[0]==='bare'&&c[1]==='files')&&!calls.some(c=>c[0]==='bare'&&c[1]==='work'),'file-only lazy load');
button('bare','Edited').click(); await wait(); check(calls.some(c=>c[0]==='bare'&&c[1]==='work'),'tool details lazy load');
button('old','1 file').click(); await wait(); check(calls.some(c=>c[0]==='old'&&c[1]==='files')&&!calls.some(c=>c[0]==='old'&&c[1]==='work'),'file request should not fetch work');
button('old','Worked for').click(); await wait(); check(document.querySelector('#old').textContent.includes('Retry'),'work error retry');
button('old','Retry').click(); await wait(); check(calls.filter(c=>c[0]==='old'&&c[1]==='work').length===2&&document.querySelector('#old').textContent.includes('Thought'),'work retry');
render(true); await wait(); check(!document.querySelector('#old').textContent.includes('old.txt')&&!document.querySelector('#latest').textContent.includes('latest.txt')&&document.querySelector('#newest').textContent.includes('newest.txt'),'automatic collapse');
document.body.innerHTML='<pre id="result">PASS: lazy details and expansion</pre>';}
run().catch(e=>{document.body.innerHTML='<pre id="result">FAIL: '+e.message+'</pre>'});`;
test('chat details load independently and follow the latest message',{timeout:45000},async()=>{
 const bundled=await build({stdin:{contents:fixture,loader:'tsx',resolveDir:path.join(__dirname,'..')},bundle:true,write:false,format:'iife',jsx:'automatic',alias:{'@':path.join(__dirname,'../src')},define:{'process.env.NODE_ENV':'"production"'},plugins:[{name:'fixtures',setup(b){b.onResolve({filter:/^@\//},a=>a.path in mocks?{path:a.path,namespace:'mock'}:undefined);b.onLoad({filter:/.*/,namespace:'mock'},a=>({contents:mocks[a.path],loader:'tsx',resolveDir:path.join(__dirname,'..')}));}}]});
 const html='<!doctype html><div id="root"></div><script>'+bundled.outputFiles[0].text.replaceAll('</script','<\\/script')+'</script>';
 const server=http.createServer((req,res)=>{res.writeHead(200,{'Content-Type':'text/html'});res.end(html)}); await new Promise(r=>server.listen(0,'127.0.0.1',r));
 try {const {stdout,stderr}=await promisify(execFile)(process.env.CHROMIUM_PATH||'chromium',['--headless','--no-sandbox','--disable-gpu','--disable-dev-shm-usage','--dump-dom','--virtual-time-budget=5000','http://127.0.0.1:'+server.address().port],{timeout:35000,maxBuffer:8*1024*1024}); const result=stdout.match(/<pre id="result">([^<]*)<\/pre>/)?.[1]; assert.ok(result?.startsWith('PASS:'),result||'No result: '+stderr.slice(-1500));} finally {await new Promise(r=>server.close(r))}
});
