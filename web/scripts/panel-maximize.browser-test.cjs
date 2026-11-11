/** source: 用户要求将 BeeHub 通用特性 port 到 NexBox，summary: 验证迁移后的浏览器交互与状态保持 **/
/** source: 用户要求合并 Git 工具栏并修复仓库选择宽度, summary: 浏览器验证单栏顺序、仓库宽度与菜单、操作作用域、刷新及最大化还原 **/
const {test} = require('node:test');
const assert = require('node:assert/strict');
const {spawn} = require('node:child_process');
const {mkdtemp, rm, readFile, mkdir, writeFile} = require('node:fs/promises');
const os = require('node:os');
const http = require('node:http');
const path = require('node:path');
const {pathToFileURL} = require('node:url');
const {build} = require('esbuild');
const postcss = require('postcss');
const tailwindcss = require('tailwindcss');

// Exercise the real workspace, layout store and TerminalPanel. Only external
// data, xterm rendering and unrelated content are replaced with local fixtures.
const mocks = {
  '@/lib/api': `
    export const project = {id:'p', name:'Project', git_repo_path:'/fixture', is_git_repo:true};
    const repo=(id,name)=>({id,name,display_path:'packages/'+name,branch:'feature/a-long-branch-name',status:{total:1},supports_mr:true});
    export const repositories = {repositories:[repo('r1','ai-insight-src'),repo('r2','another-repository-with-a-very-long-name')],default_repository_id:'r1',warnings:[]};
    export const state={scans:[],refreshes:0,nextScan:null,mutations:[]};
    export const snapshot={repositories,diff:{files:[{path:'doc.txt',status:'modified',insertions:1,deletions:0}]},
      status:{staged:[],unstaged:[{path:'doc.txt'}],untracked:[],ahead:1},branches:{current:'feature/a-long-branch-name',branches:[{name:'feature/a-long-branch-name',is_current:true},{name:'main'}]},commits:[]};
    export class ApiError extends Error {}
    export const projectsApi = {list:async()=>[project]};
    export const gitApi = {repositories:async(project,session,refresh)=>{state.scans.push({project,session,refresh});if(state.nextScan){const next=state.nextScan;state.nextScan=null;return await next;}return repositories;},
      ...Object.fromEntries(['checkout','pull','push'].map(name=>[name,async(scope,args)=>state.mutations.push({name,scope,args})]))};
    export const sessionsApi = {killTerminal:async()=>{}}, gitlabApi = {}, mrWatchApi = {};
  `,
  '@/lib/ws': `export const globalWS = {subscribeGlobal:()=>()=>{}};`,
  '@/hooks/useGitReview': `import {snapshot,state} from '@/lib/api';const refresh=()=>state.refreshes++;export const useGitReview = () => ({snapshot,refresh,loading:false});`,
  'sonner': `export const toast = {error:()=>{}, warning:()=>{}, success:()=>{}};`,
  'react-router-dom': `export const Link = 'a';`,
  '@/components/chat/ChatPanel': `
    import React from 'react';
    export function ChatPanel() {
      return <div data-chat className="flex flex-col flex-1 min-h-0">
        <div data-chat-scroll className="flex-1 overflow-auto"><div style={{height:2000}}>Messages</div></div>
        <textarea data-draft defaultValue="Chat draft" />
      </div>;
    }
  `,
  '@/components/files/FileBrowser': `
    import React from 'react';
    import {createPortal} from 'react-dom';
    export function FileBrowser({refreshTarget,projectName}) {
      return <div data-files className="h-full overflow-auto">
        {refreshTarget&&createPortal(<button title="Refresh files and current file">Refresh</button>,refreshTarget)}
        <span data-project-name>{projectName}</span>
        <textarea data-editor defaultValue="Unsaved file" /><div style={{height:2000}}>Files</div>
      </div>;
    }
  `,
  '@/components/files/OpenInVSCodeButton': `import React from 'react'; export const OpenInVSCodeButton = () => <button title="Open in VS Code">VS Code</button>;`,
  './DiffFileCard': `import React from 'react';export const DiffFileCard=()=> <div data-diff-card>Diff content</div>;`,
  '@xterm/xterm': `
    export class Terminal {
      cols=80; rows=24; options={};
      open(el) {this.el=document.createElement('div');this.el.dataset.terminal='';el.append(this.el);}
      loadAddon(addon) {addon.activate?.(this);}
      focus() {} write() {} refresh() {} onData() {}
      onResize(fn) {this.resizeListener=fn;}
      dispose() {this.el.remove();}
    }
  `,
  '@xterm/addon-fit': `
    export class FitAddon {
      activate(term) {this.term=term;}
      fit() {
        const rect=this.term.el.parentElement.getBoundingClientRect();
        if(!rect.width || !rect.height) return;
        this.term.cols=Math.floor(rect.width/8);this.term.rows=Math.floor(rect.height/20);
        this.term.resizeListener?.({cols:this.term.cols,rows:this.term.rows});
      }
    }
  `,
  '@xterm/addon-web-links': `export class WebLinksAddon {}`,
};
for (const [module, name] of Object.entries({
  '@/plugins/task/TaskPlugin':'TaskPlugin',
  '@/plugins/asset/AssetPanel':'AssetPanel',
  '@/components/workflow/WorkflowPanel':'WorkflowPanel',
  '@/components/ralph/RalphPanel':'RalphPanel',
  '@/plugins/okone/ChangeOrderMenuItem':'ChangeOrderMenuItem',
  '@/plugins/okone/ChangeOrderResult':'ChangeOrderResult',
  '@/components/files/QuickOpen':'QuickOpen',
  '@/components/project/ProjectDialog':'ProjectDialog',
})) mocks[module] = `export const ${name} = () => null;`;

mocks['@/components/workflow/WorkflowPanel'] = `
 import React from 'react';
 export const WorkflowPanel = ({onEditStateChange}) => <button title="Open workflow editor" onClick={()=>onEditStateChange(true,'Example flow',()=>{},()=>onEditStateChange(false,'',null,null))}>Edit workflow</button>;
`;

const fixture = `
import React from 'react';
import {createRoot} from 'react-dom/client';
import {flushSync} from 'react-dom';
import {SessionWorkspace} from './src/pages/SessionWorkspace';
import {useFileBrowserStore} from './src/stores/fileBrowser';
import {useAppStore} from './src/stores/app';
import {useLayoutStore} from './src/stores/layout';
import {project,state,repositories,snapshot} from '@/lib/api';
localStorage.clear();
const sockets=[];
window.WebSocket=class {
  static OPEN=1;readyState=1;sent=[];closed=false;
  constructor() {sockets.push(this);setTimeout(()=>this.onopen?.(),0);}
  send(value) {this.sent.push(value);}
  close() {this.closed=true;this.readyState=3;this.onclose?.();}
};
const sessions=['s1','s2'].map(id=>({id,name:id,project_id:'p',agent:'test'}));
useAppStore.setState({sessions,projects:[project],activeSessionId:'s1'});
useLayoutStore.setState({rightPanel:'files',panelWidth:513,terminalPosition:'right',terminalStates:{}});
const root=createRoot(document.getElementById('workspace'));
flushSync(()=>root.render(<SessionWorkspace/>));
const check=(ok,message)=>{if(!ok) throw new Error(message);};
const settle=()=>new Promise(resolve=>setTimeout(resolve,60));
const visible=el=>!!el?.getClientRects().length;
const button=title=>Array.from(document.querySelectorAll('button')).find(el=>el.title===title && visible(el));
const click=async title=>{const el=button(title);check(el,'Missing button: '+title);el.click();await settle();};
const panel=()=>document.querySelector('[style*="--panel-width"]');
const chat=()=>document.querySelector('[data-chat]');
const width=el=>el.getBoundingClientRect().width;
const closeTo=(a,b)=>Math.abs(a-b)<2;
const savedWidth=()=>{check(useLayoutStore.getState().panelWidth===513,'Maximize changed saved width');};
async function checkGitActions() {
  const fileLabel=Array.from(document.querySelectorAll('span')).find(el=>el.textContent==='doc.txt');
  check(fileLabel,'Missing diff file list');fileLabel.closest('.cursor-pointer').click();await settle();
  const refresh=button('Refresh'),maximize=button('Maximize panel')||button('Restore panel');
  const pull=button('Pull');
  const repo=document.querySelector('button[aria-haspopup="listbox"]'),branch=document.querySelector('[aria-label="Switch branch"]');
  check(refresh&&pull&&repo&&branch&&button('Previous')&&button('Next'),'Missing Git actions');
  check(document.querySelectorAll('button[title="Refresh"]').length===1&&!button('Rescan repositories'),'Duplicate Git refresh');
  check(width(repo)>=(matchMedia('(min-width:768px)').matches?96:88)&&width(repo)<=241,'Repository selector width regressed: '+width(repo));
  const controls=[repo,branch,pull,button('Push'),button('Previous'),button('Next'),refresh,maximize].filter(Boolean);
  const bounds=panel().getBoundingClientRect();
  function checkRow() {
    for(let i=0;i<controls.length;i++) {
      const r=controls[i].getBoundingClientRect(),first=controls[0].getBoundingClientRect();
      check(Math.abs(r.top+r.height/2-first.top-first.height/2)<2,'Git controls are not in one row');
      check(r.left>=bounds.left&&r.right<=bounds.right,'Git controls overflow panel');
      if(i) check(controls[i-1].getBoundingClientRect().right<=r.left+1,'Git controls overlap or are out of order');
    }
  }
  checkRow();
  const originalProjects=useAppStore.getState().projects;
  flushSync(()=>useAppStore.getState().setProjects([{...project,okone:{application_id:'fixture'}}]));await settle();
  checkRow();
  flushSync(()=>useAppStore.getState().setProjects(originalProjects));await settle();
  const repoWidth=width(repo);repo.click();await settle();
  const menu=document.querySelector('[role="listbox"]'),menuBounds=menu.getBoundingClientRect();
  check(width(menu)>=Math.max(repoWidth,300)&&menuBounds.left>=0&&menuBounds.right<=innerWidth,'Repository dropdown width/position regressed');
  document.querySelectorAll('[role="option"]')[1].click();await settle();
  const selected=document.querySelector('button[aria-haspopup="listbox"]');
  check(width(selected)>=(matchMedia('(min-width:768px)').matches?96:88)&&width(selected)<=241&&selected.title.includes('another-repository-with-a-very-long-name'),'Long repository name exceeds selector bounds');
  await click('Pull');await click('Push');
  check(state.mutations.slice(-2).every(call=>call.scope.repositoryId==='r2'),'Moved actions use wrong repository');
  selected.click();await settle();document.querySelectorAll('[role="option"]')[0].click();await settle();
  document.querySelector('[aria-label="Switch branch"]').click();await settle();
  const branchMenu=document.querySelector('button[title="main"]').closest('[data-radix-popper-content-wrapper]').getBoundingClientRect();
  check(branchMenu.left>=0&&branchMenu.right<=innerWidth,'Branch dropdown is clipped');
  await click('main');check(state.mutations.at(-1).scope.repositoryId==='r1','Branch checkout uses wrong repository');
  const before=state.refreshes,scans=state.scans.length,card=document.querySelector('[data-diff-card]');
  let resolve;state.nextScan=new Promise(r=>{resolve=r;});
  refresh.click();await settle();check(refresh.disabled,'Refresh is not disabled during scan');
  check(document.querySelector('[data-diff-card]')===card,'Unified refresh unmounted diff');
  resolve(repositories);await settle();
  check(state.scans.length===scans+1&&state.scans.at(-1).refresh===true&&state.refreshes===before+1,'Refresh did not combine repository scan and review');
}
async function run() {
  for(let attempt=0;attempt<30&&!document.querySelector('[data-files]');attempt++) await settle();
  if(location.pathname==='/preview') {
    repositories.repositories.splice(1);
    Object.assign(repositories.repositories[0],{name:'nexbox',display_path:'.',branch:'eric'});
    Object.assign(snapshot.branches,{current:'eric',branches:[{name:'eric',is_current:true}]});snapshot.status.ahead=2;
    flushSync(()=>useLayoutStore.setState({rightPanel:'git',panelWidth:480}));await settle();
    if(location.search.includes('maximized')) await click('Maximize panel');
    const repo=document.querySelector('[title="nexbox · ."]'),branch=document.querySelector('[aria-label="Switch branch"]'),pull=button('Pull');
    check(width(repo)<=140,'Short repository reserves excessive blank width: '+width(repo));
    check(width(branch)<=100&&pull.getBoundingClientRect().left-branch.getBoundingClientRect().right<=8,'Short branch reserves excessive blank width');
    const actions=[button('Refresh'),button('Maximize panel')||button('Restore panel')];
    check(actions.every(el=>visible(el)&&el.getBoundingClientRect().right<=panel().getBoundingClientRect().right),'Right actions are clipped');
    window.previewReady=true;return 'PREVIEW';
  }
  check(document.querySelector('[data-files]'),'Files did not mount');
  window.dispatchEvent(new CustomEvent('open-file',{detail:{path:'src/app.ts',line:42,safeLink:true}}));await settle();
  const fileState=useFileBrowserStore.getState();
  check(fileState.projects['p:local']?.selectedPath==='src/app.ts'&&fileState.navigation['p:local']?.line===42&&fileState.navigation['p:local']?.safeLink,'Chat file link did not seed checkout-scoped navigation');
  window.dispatchEvent(new CustomEvent('open-file',{detail:{path:'src/app.ts',line:7,safeLink:true}}));await settle();
  check(useFileBrowserStore.getState().navigation['p:local']?.line===7,'Repeated file navigation lost the new line');
  check(document.querySelector('[data-project-name]').textContent===project.name,'Workspace did not pass project name to Files');
  const fileRefresh=button('Refresh files and current file');
  check(fileRefresh&&fileRefresh.parentElement.nextElementSibling?.title==='Maximize panel','Files refresh is not before maximize');
  await click('Workflow');await click('Open workflow editor');await click('Edit name');
  check(document.querySelector('input[placeholder="Workflow Name"]')?.value==='Example flow','Workflow name editing was lost during toolbar migration');
  await click('Cancel');check(button('Edit name'),'Cancel did not restore workflow rename');
  await click('Files');
  if(matchMedia('(max-width:767px)').matches) {
    check(!button('Maximize panel'),'Mobile exposes maximize control');
    check(!visible(chat()),'Mobile chat overlaps panel');
    await click('Hide panel');check(visible(chat()),'Mobile hide did not restore chat');
    await click('Terminal');check(!button('Maximize panel'),'Mobile terminal exposes maximize');
    await click('Hide panel');check(visible(chat()),'Mobile terminal hide did not restore chat');
    await click('Git');await checkGitActions();await click('Hide panel');
    return 'mobile layout and controls';
  }
  const sidebar=document.querySelector('[data-sidebar]'),sidebarBefore=sidebar.getBoundingClientRect();
  const chatNode=chat(),chatScroll=document.querySelector('[data-chat-scroll]');
  chatScroll.scrollTop=240;document.querySelector('[data-draft]').value='Draft stays';
  const files=document.querySelector('[data-files]'),editor=document.querySelector('[data-editor]');
  editor.value='Unsaved edit stays';files.scrollTop=160;
  const before=width(panel()),chatBefore=width(chat());
  check(closeTo(before,513),'Initial width incorrect: '+before);
  await click('Maximize panel');
  check(!visible(chat()),'Maximize leaves chat visible');
  check(width(panel())>before+200,'Panel did not expand into chat');
  check(closeTo(panel().getBoundingClientRect().left,sidebarBefore.right),'Maximize leaves a chat gap or covers sidebar');
  check(closeTo(width(sidebar),sidebarBefore.width),'Maximize resized sidebar');
  check(!visible(document.querySelector('.cursor-col-resize')),'Maximize leaves divider enabled');
  check(button('Restore panel').getAttribute('aria-pressed')==='true','Restore button state incorrect');
  check(editor===document.querySelector('[data-editor]')&&editor.value==='Unsaved edit stays','Maximize remounted file editor');
  await click('Restore panel');savedWidth();
  check(closeTo(width(panel()),before)&&closeTo(width(chat()),chatBefore),'Restore lost original widths');
  check(chat()===chatNode&&chatScroll.scrollTop===240&&files.scrollTop===160,'Restore lost reading position');
  check(document.querySelector('[data-draft]').value==='Draft stays','Restore lost draft');
  await click('Maximize panel');await click('Hide panel');
  check(visible(chat()),'Hide while maximized failed');
  await click('Files');check(button('Maximize panel')&&closeTo(width(panel()),before),'Reopening retained maximize');
  await click('Maximize panel');await click('Git');
  check(button('Restore panel')&&!visible(chat()),'Files to Git lost maximize');
  await checkGitActions();
  await click('Restore panel');await checkGitActions();
  flushSync(()=>useLayoutStore.getState().setPanelWidth(480));await settle();await checkGitActions();
  flushSync(()=>useLayoutStore.getState().setPanelWidth(513));await settle();await click('Maximize panel');
  await click('Files');check(button('Restore panel'),'Git to Files lost maximize');
  await click('Terminal');check(button('Restore panel'),'Files to terminal lost maximize');
  const terminal=document.querySelector('[data-terminal]'),terminalSocket=sockets.at(-1);
  check(sockets.length===1&&terminal,'Terminal did not establish one connection');
  const expandedCols=JSON.parse(terminalSocket.sent.at(-1)).cols;
  await click('Restore panel');
  for(let attempt=0;attempt<10&&JSON.parse(terminalSocket.sent.at(-1)).cols>=expandedCols;attempt++) await settle();
  check(JSON.parse(terminalSocket.sent.at(-1)).cols<expandedCols,'Terminal did not fit restored width: '+expandedCols+' -> '+JSON.parse(terminalSocket.sent.at(-1)).cols+', width '+width(terminal.parentElement));
  await click('Maximize panel');await click('Git');await click('Terminal');
  check(terminal===document.querySelector('[data-terminal]')&&!terminalSocket.closed&&sockets.length===1,'Maximize/switch reconnected terminal');
  await click('Hide panel');check(visible(chat()),'Terminal hide did not restore chat');
  await click('Terminal');check(button('Maximize panel')&&!terminalSocket.closed,'Terminal reopening retained maximize or lost connection');
  await click('Maximize panel');
  flushSync(()=>useAppStore.getState().setActiveSessionId('s2'));await settle();
  check(visible(chat()),'Session switch retained maximized terminal');
  flushSync(()=>useAppStore.getState().setActiveSessionId('s1'));await settle();
  check(button('Maximize panel')&&!terminalSocket.closed,'Returning session restored stale maximize or lost PTY');
  await click('Maximize panel');await click('Move to bottom');
  check(visible(chat())&&button('Move to right'),'Move to bottom retained maximize');
  const bottomTerminal=document.querySelector('[data-terminal]'),bottomSocket=sockets.at(-1);
  await click('Files');await click('Maximize panel');
  check(!visible(bottomTerminal)&&!bottomSocket.closed,'Maximize did not hide bottom terminal while preserving connection');
  await click('Restore panel');
  check(visible(bottomTerminal)&&document.querySelector('[data-terminal]')===bottomTerminal&&!bottomSocket.closed,'Restore lost bottom terminal');
  await click('Move to right');
  check(button('Maximize panel'),'Moving bottom terminal to right retained maximize');
  await click('Maximize panel');await click('Kill terminal');
  check(visible(chat()),'Killing maximized terminal did not restore chat');
  await click('Files');await click('Maximize panel');
  await click('Files');check(visible(chat()),'Activity button did not exit maximized panel');
  await click('Files');check(button('Maximize panel'),'Activity button reopening retained maximize');
  savedWidth();
  return 'expand/restore geometry, sidebar, width, drafts/scroll/editor identity, hide/reopen, Files/Git/Terminal switches, terminal fitting/connections, session switch, docking and kill';
}
run().then(result=>{
  if(result==='PREVIEW') return;
  root.unmount();document.body.innerHTML='<pre id="result">PASS: '+result+'</pre>';
}).catch(error=>{document.body.innerHTML='<pre id="result">FAIL: '+error.message+'</pre>';});
`;

// Use a real browser clock: virtual-time --dump-dom can skip animation frames
// and ResizeObserver deliveries, which the actual terminal uses for fitting.
async function runBrowser(url) {
  const profile=await mkdtemp(path.join(os.tmpdir(),'beehub-panel-test-'));
  const child=spawn(process.env.CHROMIUM_PATH||'chromium',[
    '--headless','--no-sandbox','--disable-gpu','--disable-dev-shm-usage',
    '--no-first-run','--no-default-browser-check','--remote-debugging-port=0','--user-data-dir='+profile,
  ],{stdio:['ignore','ignore','pipe']});
  let socket;
  try {
    const endpoint=await new Promise((resolve,reject)=>{
      let output='';
      const timer=setTimeout(()=>reject(new Error('Chromium debugger startup timed out')),10000);
      child.once('error',error=>{clearTimeout(timer);reject(error);});
      child.once('exit',code=>{clearTimeout(timer);reject(new Error('Chromium exited: '+code));});
      child.stderr.on('data',chunk=>{
        output+=chunk;
        const match=output.match(/DevTools listening on (ws:\/\/[^\s]+)/);
        if(match) {clearTimeout(timer);resolve(match[1]);}
      });
    });
    socket=new WebSocket(endpoint);
    await new Promise((resolve,reject)=>{socket.onopen=resolve;socket.onerror=reject;});
    let nextId=0;
    const pending=new Map();
    socket.onmessage=({data})=>{
      const message=JSON.parse(data),request=pending.get(message.id);
      if(!request) return;
      pending.delete(message.id);clearTimeout(request.timer);
      if(message.error) request.reject(new Error(message.error.message));else request.resolve(message.result);
    };
    const send=(method,params={},sessionId)=>new Promise((resolve,reject)=>{
      const id=++nextId,timer=setTimeout(()=>{pending.delete(id);reject(new Error('CDP timeout: '+method));},5000);
      pending.set(id,{resolve,reject,timer});socket.send(JSON.stringify({id,method,params,sessionId}));
    });
    for(const width of [1440,375]) {
      const {targetId}=await send('Target.createTarget',{url:'about:blank'});
      const {sessionId}=await send('Target.attachToTarget',{targetId,flatten:true});
      await send('Emulation.setDeviceMetricsOverride',{width,height:1000,deviceScaleFactor:1,mobile:width<768},sessionId);
      await send('Page.navigate',{url},sessionId);
      let result;
      for(let attempt=0;attempt<200;attempt++) {
        const response=await send('Runtime.evaluate',{expression:'document.getElementById("result")?.textContent',returnByValue:true},sessionId);
        result=response.result.value;
        if(result) break;
        await new Promise(resolve=>setTimeout(resolve,50));
      }
      assert.ok(result?.startsWith('PASS:'),result||'No browser result at width '+width);
      console.log(width+': '+result);
      await send('Target.closeTarget',{targetId});
    }
    const artifactDir=path.join(os.tmpdir(),'nexbox-git-toolbar-review');
    await mkdir(artifactDir,{recursive:true});
    for(const mode of ['normal','maximized']) {
      const {targetId}=await send('Target.createTarget',{url:'about:blank'});
      const {sessionId}=await send('Target.attachToTarget',{targetId,flatten:true});
      await send('Emulation.setDeviceMetricsOverride',{width:1440,height:1000,deviceScaleFactor:1,mobile:false},sessionId);
      await send('Page.navigate',{url:url+'/preview?'+mode},sessionId);
      let ready=false;
      for(let attempt=0;attempt<100;attempt++) {
        const {result}=await send('Runtime.evaluate',{expression:'({ready:window.previewReady,error:document.getElementById("result")?.textContent})',returnByValue:true},sessionId);
        if(result.value?.error) throw new Error(result.value.error);
        if(result.value?.ready) {ready=true;break;}
        await new Promise(resolve=>setTimeout(resolve,50));
      }
      assert.ok(ready,'Preview did not become ready');
      const {result}=await send('Runtime.evaluate',{expression:'JSON.stringify((()=>{const r=document.querySelector(\'[style*="--panel-width"]\').getBoundingClientRect();return {x:r.x,y:r.y,width:r.width,height:240,scale:1};})())',returnByValue:true},sessionId);
      const {data}=await send('Page.captureScreenshot',{format:'png',clip:JSON.parse(result.value)},sessionId);
      const screenshotPath=path.join(artifactDir,mode+'.png');await writeFile(screenshotPath,Buffer.from(data,'base64'));
      console.log('PASS: compact single-repository '+mode+'; screenshot '+screenshotPath);
      await send('Target.closeTarget',{targetId});
    }
  } finally {
    socket?.close();
    const exited=new Promise(resolve=>child.once('exit',resolve));
    if(child.exitCode===null) {child.kill();await exited;}
    await rm(profile,{recursive:true,force:true});
  }
}

test('right panel maximization preserves layout and mounted state', {timeout:60000}, async()=>{
  const webDir=path.join(__dirname,'..');
  const config=(await import(pathToFileURL(path.join(webDir,'tailwind.config.js')).href)).default;
  const sourceCSS=await readFile(path.join(webDir,'src/index.css'),'utf8');
  const [bundle, css]=await Promise.all([
    build({
      stdin:{contents:fixture,loader:'tsx',resolveDir:webDir},bundle:true,write:false,
      format:'iife',jsx:'automatic',alias:{'@':path.join(webDir,'src')},
      define:{'process.env.NODE_ENV':'"production"'},loader:{'.css':'empty'},
      plugins:[{name:'fixtures',setup(build){
        build.onResolve({filter:/.*/},args=>args.path in mocks?{path:args.path,namespace:'mock'}:undefined);
        build.onLoad({filter:/.*/,namespace:'mock'},args=>({contents:mocks[args.path],loader:'tsx',resolveDir:webDir}));
      }}],
    }),
    postcss([tailwindcss({...config,content:[path.join(webDir,'src/**/*.{ts,tsx}'),{raw:fixture+Object.values(mocks).join('\n'),extension:'tsx'}]})])
      .process(sourceCSS,{from:undefined}),
  ]);
  const html='<!doctype html><meta name="viewport" content="width=device-width,initial-scale=1"><style>'+css.css+'</style>'
    +'<body class="dark"><style>@media(max-width:767px){[data-sidebar]{display:none}}</style><div style="display:flex;height:800px"><aside data-sidebar style="width:220px;flex-shrink:0">Sessions</aside><main id="workspace" style="flex:1;min-width:0"></main></div>'
    +'<script>'+bundle.outputFiles[0].text.replaceAll('</script','<\\/script')+'</script>';
  const server=http.createServer((req,res)=>{res.writeHead(200,{'Content-Type':'text/html'});res.end(html);});
  await new Promise(resolve=>server.listen(0,'127.0.0.1',resolve));
  try {
    await runBrowser('http://127.0.0.1:'+server.address().port);
  } finally {await new Promise(resolve=>server.close(resolve));}
});
