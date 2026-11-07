/** source: 用户要求将 BeeHub 通用特性 port 到 NexBox，summary: 验证迁移后的浏览器交互与状态保持 **/
/** source: 用户要求按钮加载历史消息，summary: 浏览器验证分页按钮、重试、缓存与滚动位置 **/
const {test} = require('node:test');
const assert = require('node:assert/strict');
const {execFile} = require('node:child_process');
const {promisify} = require('node:util');
const http = require('node:http');
const path = require('node:path');
const {build} = require('esbuild');
const mocks = {
 '@/lib/api': `
 export const calls = [], responses = [];
 export const messagesApi = {list: async (...args) => {
 calls.push(args); const res = responses.shift();
 if (res instanceof Error) throw res;
 if (!res) throw new Error('Unexpected request'); return await res;
 }};
 export const serverApi = {info:async()=>({agent:'test'}),availableAgents:async()=>({}),availableModels:async()=>({})};
 export const queueApi = {list:async()=>[]};
 export const sessionsApi = {list:async()=>({sessions:[]})}, tasksApi = {}, gitApi = {};
 export const projectsApi = {list:async()=>[]}, marketplaceApi = {};
 `,
 '@/lib/ws': `
 const unrouted = new Set();
 export const emitUnrouted = id => unrouted.forEach(fn=>fn(id));
 export const globalWS = {connected:true,subscribe:()=>()=>{},onConnection:()=>()=>{},
 subscribeGlobal:()=>()=>{},subscribeUnrouted:fn=>{unrouted.add(fn);return ()=>unrouted.delete(fn);}};
 `,
 'react-router-dom': `export const useLocation = () => ({pathname:'/'});`,
 '@/components/chat/QuestionCard': `export const parseAskQuestionInput = () => null;`,
 '@/components/chat/ToolCallCard': `export const TODO_TOOLS = new Set();`,
 '@/components/chat/ChatInput': `export const ChatInput = () => null;`,
 '@/components/chat/ChatMessage': `
 import React from 'react';
 export const ChatMessage = ({message}) => <div data-id={message.id} style={{height:150}}>Message {message.id}</div>;
 export const CopyButton = () => null, ForkButton = () => null;
 export const extractPlainText = () => '', stripAllNextActions = s => s;
 `,
};
const fixture = `
import React from 'react';
import {createRoot} from 'react-dom/client';
import {flushSync} from 'react-dom';
import {ChatPanel} from './src/components/chat/ChatPanel';
import {useSidebarData} from './src/hooks/useSidebarData';
import {emitUnrouted} from '@/lib/ws';
import {calls,responses} from '@/lib/api';
import {useAppStore} from './src/stores/app';
const message = (id,visible=true) => ({id,role:'assistant',session_id:'s',parts:visible?[{type:'text',content:id}]:[],metadata:{},created_at:'2026-01-01T00:00:00Z'});
const session = {id:'s',name:'History',agent:'test',model:'test'};
const root = createRoot(document.getElementById('root'));
const check = (ok,text) => {if (!ok) throw new Error(text);};
const settle = async () => {await new Promise(r=>setTimeout(r,40));};
function Sidebar() {useSidebarData(); return null;}
const render = (showChat=true) => flushSync(()=>root.render(<><Sidebar/>{showChat && <ChatPanel session={session} project={null}/>}</>));
const button = () => Array.from(document.querySelectorAll('button')).find(b=>/earlier messages/.test(b.textContent));
const scroll = () => document.querySelector('[tabindex="0"]');
async function run() {
 responses.push({messages:[message('new1'),message('new2'),message('new3')],has_more:true});
 render(); await settle();
 check(calls.length===1 && calls[0][1]===20,'Initial load must request 20');
 scroll().scrollTop=0; scroll().dispatchEvent(new Event('scroll')); await settle();
 check(calls.length===1 && button(),'Scrolling should only expose the button');
 let resolve; responses.push(new Promise(r=>resolve=r));
 const before=document.querySelector('[data-id="new1"]').getBoundingClientRect().top;
 button().click(); button().click(); await settle();
 check(calls.length===2 && button().disabled,'Duplicate click issued multiple requests');
 resolve({messages:[message('old1'),message('old2')],has_more:true}); await settle();
 const after=document.querySelector('[data-id="new1"]').getBoundingClientRect().top;
 check(Math.abs(before-after)<2,'Reading position moved: '+before+' -> '+after);
 check(calls[1][2]==='new1','Wrong initial cursor');
 flushSync(()=>root.render(null)); render(); await settle();
 check(calls.length===2 && button(),'Remount lost pagination or refetched cache');
 responses.push({messages:[message('hidden',false)],has_more:true}); button().click(); await settle();
 check(!document.querySelector('[data-id="hidden"]') && button(),'Hidden page blocked pagination');
 responses.push(new Error('offline')); button().click(); await settle();
 check(calls.at(-1)[2]==='hidden' && /Retry/.test(button().textContent),'Raw cursor or retry missing');
 responses.push({messages:[message('oldest'),message('old1')],has_more:false}); button().click(); await settle();
 check(!button(),'Button should disappear at end');
 check(document.querySelectorAll('[data-id="old1"]').length===1,'Duplicate message after prepend');
 check(document.querySelector('[data-id="oldest"]'),'Retry did not load messages');
 // Reconnect may replace the current page while an older-page request runs.
 const store=useAppStore.getState();
 store.setChatHistoryPage('s',[message('oldest')],true); await settle();
 let staleResolve; responses.push(new Promise(r=>staleResolve=r));
 button().click(); await settle();
 store.setChatMessages('s',[message('reconnected')]);
 store.setChatHistoryPage('s',[message('reconnected')],true);
 staleResolve({messages:[message('stale')],has_more:false}); await settle();
 check(!document.querySelector('[data-id="stale"]') && button(),'Stale request overwrote reconnect history');
 check(useAppStore.getState().chatHistoryPages.s.beforeId==='reconnected','Stale request replaced cursor');
 // An event for an unviewed session invalidates both transcript and cursor.
 render(false); await settle();
 const other=[message('other')];
 store.setChatMessages('other',other); store.setChatHistoryPage('other',other,true);
 emitUnrouted('s');
 check(!useAppStore.getState().chatMessages.s?.length,'Background update retained stale transcript');
 check(!useAppStore.getState().chatHistoryPages.s,'Background update retained stale pagination');
 check(useAppStore.getState().chatMessages.other===other,'Invalidation affected another session');
 const count=calls.length;
 responses.push({messages:[message('latest')],has_more:true}); render(); await settle();
 check(calls.length===count+1 && !calls.at(-1)[2],'Returning must fetch latest page');
 check(document.querySelector('[data-id="latest"]'),'Returning session requires a manual click');
 // Recover a legacy empty cache whose pagination metadata was left behind.
 render(false); await settle();
 store.setChatMessages('s',[]); store.setChatHistoryPage('s',[message('orphan')],true);
 responses.push({messages:[message('recovered')],has_more:false}); render(); await settle();
 check(document.querySelector('[data-id="recovered"]'),'Empty cache with orphan cursor was not recovered');
 document.body.innerHTML='<pre id="result">PASS: manual loading, scroll preservation, duplicate clicks, remount, hidden cursor, retry and end of history</pre>';
}
run().catch(e=>{document.body.innerHTML='<pre id="result">FAIL: '+e.message+'</pre>';});
`;
test('chat pagination retains reading position and survives remounts', {timeout:45000}, async()=>{
 const bundled=await build({
 stdin:{contents:fixture,loader:'tsx',resolveDir:path.join(__dirname,'..')},
 bundle:true,write:false,format:'iife',jsx:'automatic',alias:{'@':path.join(__dirname,'../src')},
 define:{'process.env.NODE_ENV':'"production"'},
 plugins:[{name:'fixtures',setup(build){
 build.onResolve({filter:/^(?:@\/|react-router-dom$)/},args=>args.path in mocks?{path:args.path,namespace:'mock'}:undefined);
 build.onLoad({filter:/.*/,namespace:'mock'},args=>({contents:mocks[args.path],loader:'tsx',resolveDir:path.join(__dirname,'..')}));
 }}],
 });
 const css='#root{height:400px;display:flex}.flex{display:flex}.flex-col{flex-direction:column}.flex-1{flex:1}.min-h-0{min-height:0}.overflow-y-auto{overflow-y:auto}.gap-6{gap:24px}button{height:32px}';
 const html='<!doctype html><style>'+css+'</style><div id="root"></div><script>'+bundled.outputFiles[0].text.replaceAll('</script','<\\/script')+'</script>';
 const server=http.createServer((req,res)=>{res.writeHead(200,{'Content-Type':'text/html'});res.end(html);});
 await new Promise(r=>server.listen(0,'127.0.0.1',r));
 try {
 const {stdout,stderr}=await promisify(execFile)(process.env.CHROMIUM_PATH||'chromium',[
 '--headless','--no-sandbox','--disable-gpu','--disable-dev-shm-usage','--dump-dom','--virtual-time-budget=5000',
 'http://127.0.0.1:'+server.address().port,
 ],{timeout:35000,maxBuffer:8*1024*1024});
 const result=stdout.match(/<pre id="result">([^<]*)<\/pre>/)?.[1];
 assert.ok(result?.startsWith('PASS:'),result||'No result: '+stderr.slice(-1500));
 console.log(result);
 } finally {await new Promise(r=>server.close(r));}
});
