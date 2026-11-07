/** source: 用户要求将 BeeHub 通用特性 port 到 NexBox，summary: 验证迁移后的浏览器交互与状态保持 **/
/** source: 用户要求 Files 自动同步目录并复用刷新按钮，summary: 浏览器验证轮询、可见性、滚动、手动重载、未保存保护和竞态 **/
const {test} = require('node:test');
const assert = require('node:assert/strict');
const {execFile} = require('node:child_process');
const {promisify} = require('node:util');
const http = require('node:http');
const path = require('node:path');
const {build} = require('esbuild');
const mocks = {
 '@/lib/api': `
 export class ApiError extends Error {constructor(message,status){super(message);this.status=status;}}
 export const state={directories:{},contents:{},lists:[],reads:[],nextContent:null,nextList:null,failContent:false,failList:false};
 export const filesApi={
 list:async(project,path,session)=>{
 state.lists.push({project,path,session}); const result=structuredClone(state.directories[path]||[]);
 if(state.failList) throw new Error('Unavailable');
 if(state.nextList){const pending=state.nextList;state.nextList=null;return await pending;} return result;
 },
 content:async(project,path,session,repository,safeLink)=>{
 state.reads.push({project,path,session,repository,safeLink});
 if(state.failContent) throw new ApiError('File not found',404);
 if(state.nextContent){const pending=state.nextContent;state.nextContent=null;return await pending;}
 return {path,binary:false,size:state.contents[path].length,content:state.contents[path]};
 },head:async()=>({in_repo:false}),serveUrl:()=>'/image?file=test',
 };
 export const gitApi={workspaceStatus:async()=>({files:[]})};
 `,
 'sonner': `export const notices=[]; export const toast=Object.fromEntries(['error','warning','success'].map(name=>[name,message=>notices.push(message)]));`,
 '@/lib/monaco-setup': '',
 '@monaco-editor/react': `
 import React,{useEffect,useRef} from 'react';
 export default function Editor(props){
 const input=useRef(null),latest=useRef(props);latest.current=props;
 useEffect(()=>{
 const view={top:0,position:0};
 const editor={view,
 getModel:()=>({getValue:()=>input.current.value,getLineCount:()=>input.current.value.split('\\n').length,getFullModelRange:()=>({})}),
 saveViewState:()=>({...view}),restoreViewState:s=>Object.assign(view,s),
 executeEdits:(_source,edits)=>{input.current.value=edits[0].text;view.top=999;view.position=999;latest.current.onChange(edits[0].text);},
 pushUndoStop:()=>{},onMouseDown:()=>{},onKeyDown:()=>{},onDidChangeModelContent:()=>{},onDidLayoutChange:()=>{},
 onDidCompositionStart:()=>{},onDidCompositionEnd:()=>{},addCommand:()=>{},
 };
 window.editorMock=editor;
 latest.current.onMount(editor,{editor:{MouseTargetType:{}},languages:{typescript:{typescriptDefaults:{setDiagnosticsOptions:()=>{}},javascriptDefaults:{setDiagnosticsOptions:()=>{}}}},KeyMod:{},KeyCode:{}});
 },[]);
 return <textarea data-editor defaultValue={props.defaultValue} ref={input} onChange={event=>latest.current.onChange(event.target.value)}/>;
 }
 `,
};
const fixture=String.raw`
import React,{useEffect,useRef} from 'react';
import {createRoot} from 'react-dom/client';
import {flushSync} from 'react-dom';
import {FileBrowser} from './src/components/files/FileBrowser';
import {useFileTree} from './src/components/files/useFileTree';
import {useFileBrowserStore} from './src/stores/fileBrowser';
import {state} from '@/lib/api';
import {notices} from 'sonner';
const check=(ok,message)=>{if(!ok)throw new Error(message);};
const settle=async()=>{await new Promise(resolve=>setTimeout(resolve,100));};
const errors=[];window.addEventListener('error',event=>errors.push(event.message));
const until=async(test)=>{for(let i=0;i<20;i++){if(test())return;await settle();}throw new Error('Timed out: '+errors.join('; '));};
const polls=new Map();
const realInterval=window.setInterval.bind(window),realClear=window.clearInterval.bind(window);
let intervalId=-1;
window.setInterval=(fn,ms,...args)=>{if(ms!==5000)return realInterval(fn,ms,...args);polls.set(intervalId,fn);return intervalId--;};
window.clearInterval=id=>{if(!polls.delete(id))realClear(id);};
const poll=async()=>{for(const fn of [...polls.values()])fn();await settle();};
let hidden=false;
Object.defineProperty(document,'hidden',{configurable:true,get:()=>hidden});
const entry=(path,is_dir=false)=>({path,name:path.split('/').pop(),is_dir});
state.directories={
 '':[entry('open',true),entry('closed',true),entry('doc.md'),entry('other.txt'),entry('picture.png'),...Array.from({length:60},(_,i)=>entry('f'+i))],
 open:[entry('open/a.txt'),entry('open/nested',true)],'open/nested':[entry('open/nested/deep.txt')],closed:[entry('closed/a.txt')],
};
const fence=String.fromCharCode(96).repeat(3);
const markdown='# Original\n\n'+fence+'mermaid\ngraph LR; A-->B\n'+fence+'\n\n'+'paragraph\n\n'.repeat(80);
state.contents={'doc.md':markdown,'other.txt':'Other content','picture.png':'binary image'};
let mermaidCalls=0;
window.mermaid={initialize(){},async render(id){mermaidCalls++;return {svg:'<svg id="'+id+'" width="1200" height="100"></svg>'};}};
const root=createRoot(document.getElementById('root'));
const render=(project='p',session)=>flushSync(()=>root.render(<FileBrowser projectId={project} projectName="Project name" sessionId={session} onOpenQuickOpen={()=>{}} refreshTarget={document.getElementById('refresh-actions')}/>));
const row=path=>document.querySelector('[data-tree-path="'+path+'"]');
const button=text=>[...document.querySelectorAll('button')].find(node=>node.textContent.trim()===text);
const refresh=()=>document.querySelector('button[aria-label="Refresh files and current file"]');
const preview=()=>document.querySelector('.markdown-content');
const contentResult=(path,content)=>({path,content,binary:false,size:content.length});
function edit(value){
 const input=document.querySelector('[data-editor]');
 Object.getOwnPropertyDescriptor(HTMLTextAreaElement.prototype,'value').set.call(input,value);
 input.dispatchEvent(new Event('input',{bubbles:true}));
}
let probe;
function Probe({project='probe',session}){
 const ref=useRef(null);
 const tree=useFileTree(project,session,new Set(['open']),ref);
 useEffect(()=>{probe=tree;});
 return <div ref={ref}>probe</div>;
}
async function run(){
 render();await settle();
 const refreshTarget=document.getElementById('refresh-actions');
 check(refreshTarget.contains(refresh())&&!document.getElementById('root').contains(refresh()),'Files refresh did not move to header');
 check(document.querySelectorAll('[aria-label="Refresh files and current file"]').length===1,'Duplicate Files refresh');
 check(document.querySelector('span[title="Project name"]')?.textContent==='Project name','File tree title is not the project name');
 document.querySelector('[title="Hide file tree"]').click();await settle();
 check(refreshTarget.contains(refresh()),'Collapsing file tree removed header refresh');
 document.querySelector('[title="Show file tree"]').click();await settle();
 row('open').click();await settle();row('open/nested').click();await settle();
 row('closed').click();await settle();row('closed').click();await settle();
 row('doc.md').click();await until(()=>document.querySelector('[data-editor]'));check(document.querySelector('[data-editor]'),'Editor not mounted');
 document.querySelector('button[title="View rendered markdown"]').click();await settle();
 check(preview()&&mermaidCalls===1,'Markdown preview missing');
 const oldPreview=preview(),svg=document.querySelector('svg'),treeScroll=row('doc.md').parentElement;
 treeScroll.scrollTop=350;oldPreview.scrollTop=250;
 const treeTop=treeScroll.scrollTop,previewTop=oldPreview.scrollTop;
 check(treeTop>0&&previewTop>0,'Fixture is not scrollable: '+[treeTop,previewTop,treeScroll.clientHeight,treeScroll.scrollHeight,oldPreview.clientHeight,oldPreview.scrollHeight].join(','));
 state.lists.length=0;state.directories.open=[entry('open/new.txt'),entry('open/nested',true)];await poll();
 check(row('open/new.txt')&&!row('open/a.txt'),'External create/delete did not sync');
 check(state.lists.some(call=>call.path==='open/nested')&&!state.lists.some(call=>call.path==='closed'),'Poll fetched collapsed folders or missed expanded folders');
 check(treeScroll.scrollTop===treeTop&&oldPreview.scrollTop===previewTop,'Poll moved scroll position');
 check(preview()===oldPreview&&document.querySelector('svg')===svg&&mermaidCalls===1,'Poll remounted preview');
 check(useFileBrowserStore.getState().projects['p:local'].selectedPath==='doc.md','Poll changed selection');
 const reads=state.reads.length;await poll();check(state.reads.length===reads,'Poll unexpectedly reloaded file content');
 state.lists.length=0;hidden=true;document.dispatchEvent(new Event('visibilitychange'));await poll();
 check(state.lists.length===0,'Hidden page polled directories');
 hidden=false;document.dispatchEvent(new Event('visibilitychange'));await settle();
 check(state.lists.some(call=>call.path===''),'Returning page did not refresh immediately');
 state.lists.length=0;document.getElementById('root').style.display='none';await poll();
 check(state.lists.length===0,'Invisible panel polled directories');document.getElementById('root').style.display='';
 refresh().click();await settle();
 check(state.reads.length===reads+1&&preview()===oldPreview&&document.querySelector('svg')===svg&&mermaidCalls===1,'Unchanged reload remounted Mermaid');
 state.contents['doc.md']=markdown.replace('Original','Updated');refresh().click();await settle();
 check(preview().textContent.includes('Updated')&&preview()===oldPreview&&preview().scrollTop===previewTop,'Reload failed or lost preview position');
 check(mermaidCalls===1,'Unchanged chart redrew during document reload');
 document.querySelector('button[title="View source markdown"]').click();await settle();
 const editor=document.querySelector('[data-editor]');window.editorMock.view.top=200;window.editorMock.view.position=7;
 state.contents['doc.md']='New external content';refresh().click();await settle();
 check(editor===document.querySelector('[data-editor]')&&editor.value==='New external content','Reload remounted editor or failed');
 check(window.editorMock.view.top===200&&window.editorMock.view.position===7,'Editor reload lost view state');
 edit('Unsaved draft');await settle();state.contents['doc.md']='Newest disk content';
 let before=state.reads.length;refresh().click();await settle();
 check(button('Keep editing')&&state.reads.length===before,'Dirty file reloaded before confirmation');
 button('Keep editing').click();await settle();check(editor.value==='Unsaved draft','Cancel discarded edits');
 refresh().click();await settle();button('Discard and reload').click();await settle();
 check(editor.value==='Newest disk content','Confirmed reload failed');
 let resolveContent;state.nextContent=new Promise(resolve=>{resolveContent=resolve;});
 refresh().click();await settle();edit('Typed while loading');await settle();
 resolveContent(contentResult('doc.md','Stale external value'));await settle();
 check(editor.value==='Typed while loading'&&notices.some(text=>text.includes('Local edits changed')),'Refresh overwrote new edits');
 refresh().click();await settle();button('Discard and reload').click();await settle();
 state.nextContent=new Promise(resolve=>{resolveContent=resolve;});refresh().click();await settle();row('other.txt').click();await settle();
 resolveContent(contentResult('doc.md','Wrong file response'));await settle();
 check(document.querySelector('[data-editor]').value==='Other content','Old file response overwrote new selection');
 state.failContent=true;refresh().click();await settle();
 check(document.querySelector('[data-editor]').value==='Other content'&&notices.some(text=>text.includes('no longer exists')),'Deleted file erased last loaded content');state.failContent=false;
 row('picture.png').click();await settle();const imageURL=document.querySelector('img[alt="picture.png"]').src;
 refresh().click();await settle();check(document.querySelector('img[alt="picture.png"]').src!==imageURL,'Image reload did not bypass cache');
 // Actual hook: identity, request ordering, workspace isolation and failures.
 flushSync(()=>root.render(<Probe/>));await settle();await probe.refreshTree();await settle();
 const fullMap=probe.childrenMap,openEntries=fullMap.open;await probe.refreshTree();await settle();
 check(probe.childrenMap===fullMap&&probe.childrenMap.open===openEntries,'Unchanged poll replaced arrays/map');
 state.directories.open=[entry('open/changed.txt')];await probe.refreshTree();await settle();
 check(probe.childrenMap!==fullMap&&probe.childrenMap['']===fullMap[''],'Changed directory replaced unrelated arrays');
 let resolveList;state.nextList=new Promise(resolve=>{resolveList=resolve;});const older=probe.refreshDir('open');
 state.directories.open=[entry('open/latest.txt')];await probe.refreshDir('open');await settle();
 resolveList([entry('open/stale.txt')]);await older;await settle();
 check(probe.childrenMap.open[0].path==='open/latest.txt','Older directory response won race');
 state.failList=true;const beforeFailure=probe.childrenMap;check(await probe.refreshTree()===false,'Directory failure not reported');await settle();
 check(probe.childrenMap===beforeFailure,'Failed poll cleared cache');state.failList=false;
 state.nextList=new Promise(resolve=>{resolveList=resolve;});const oldScope=probe.refreshTree();
 flushSync(()=>root.render(<Probe project="other-project" session="worktree"/>));await settle();const newScopeMap=probe.childrenMap;
 resolveList([entry('wrong-project.txt')]);await oldScope;await settle();
 check(probe.childrenMap===newScopeMap&&state.lists.some(call=>call.project==='other-project'&&call.session==='worktree'),'Old scope changed new workspace');
 flushSync(()=>root.unmount());check(polls.size===0,'Unmount left polling timers');
 document.body.innerHTML='<pre id="result">PASS: visible-folder polling, visibility resume, unchanged identity, scroll/selection, text/image reload, dirty cancel/confirm, editor view state, races and failure recovery</pre>';
}
run().catch(error=>{document.body.innerHTML='<pre id="result">FAIL: '+error.message+'</pre>';});
`;
test('Files refresh preserves reading and editing state',{timeout:45000},async()=>{
 const bundled=await build({
 stdin:{contents:fixture,loader:'tsx',resolveDir:path.join(__dirname,'..')},
 bundle:true,write:false,format:'iife',jsx:'automatic',alias:{'@':path.join(__dirname,'../src')},define:{'process.env.NODE_ENV':'"production"'},
 plugins:[{name:'fixtures',setup(build){
 build.onResolve({filter:/^(?:@\/|@monaco-editor\/react$|sonner$)/},args=>args.path in mocks?{path:args.path,namespace:'mock'}:undefined);
 build.onLoad({filter:/.*/,namespace:'mock'},args=>({contents:mocks[args.path],loader:'tsx',resolveDir:path.join(__dirname,'..')}));
 }}],
 });
 const css='#root{height:450px;width:900px}.flex{display:flex}.flex-col{flex-direction:column}.flex-1{flex:1;min-height:0}.h-full{height:100%}.min-h-0{min-height:0}.shrink-0{flex-shrink:0}.overflow-auto{overflow:auto}.overflow-y-auto{overflow-y:auto}.overflow-hidden{overflow:hidden}.overflow-x-auto{overflow-x:auto}[data-tree-path]{height:24px}[data-editor]{width:100%;height:350px}.markdown-content{box-sizing:border-box}.markdown-content p{margin:20px 0}svg{display:block}';
 const html='<!doctype html><style>'+css+'</style><div id="refresh-actions"></div><div id="root"></div><script>'+bundled.outputFiles[0].text.replaceAll('</script','<\\/script')+'</script>';
 const server=http.createServer((request,response)=>{
 if(request.url.startsWith('/image?')){response.writeHead(200,{'Content-Type':'image/svg+xml'});response.end('<svg xmlns="http://www.w3.org/2000/svg" width="10" height="10"></svg>');return;}
 response.writeHead(200,{'Content-Type':'text/html'});response.end(html);
 });
 await new Promise(resolve=>server.listen(0,'127.0.0.1',resolve));
 try{
 const {stdout,stderr}=await promisify(execFile)(process.env.CHROMIUM_PATH||'chromium',[
 '--headless','--no-sandbox','--disable-gpu','--disable-dev-shm-usage','--dump-dom','--virtual-time-budget=12000','http://127.0.0.1:'+server.address().port,
 ],{timeout:35000,maxBuffer:8*1024*1024});
 const result=stdout.match(/<pre id="result">([^<]*)<\/pre>/)?.[1];
 assert.ok(result?.startsWith('PASS:'),result||'No result: '+stderr.slice(-2000));console.log(result);
 }finally{await new Promise(resolve=>server.close(resolve));}
});
