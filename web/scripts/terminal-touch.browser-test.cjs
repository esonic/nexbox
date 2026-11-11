/** source: 用户要求五项 BeeHub 改进移植到 NexBox, summary: 验证原生终端触摸滚动且保持 NexBox 生命周期 **/
/** source: 用户要求回迁手机终端滚动并验收，summary: 在手机尺寸及触摸模拟下验证真实 xterm 的滚动、捏合、多指、选择、桌面滚轮和卸载清理 **/
const {test}=require('node:test');
const assert=require('node:assert/strict');
const {spawn}=require('node:child_process');
const {mkdtemp,rm,readFile,writeFile}=require('node:fs/promises');
const os=require('node:os');
const path=require('node:path');
const http=require('node:http');
const {build}=require('esbuild');

const fixture=String.raw`
import React from 'react';
import {createRoot} from 'react-dom/client';
import {TerminalPanel} from './src/plugins/terminal/TerminalPanel';
window.sent=[];
class Socket {
 static OPEN=1;readyState=1;
 constructor(){window.socket=this;setTimeout(()=>{this.onopen?.();this.onmessage?.({data:Array.from({length:180},(_,i)=>'Line '+String(i).padStart(3,'0')).join('\r\n')+'\r\n'});},50);}
 send(data){window.sent.push(data);}
 close(){this.readyState=3;this.closed=true;this.onclose?.();}
}
window.WebSocket=Socket;
window.root=createRoot(document.getElementById('root'));
window.root.render(<TerminalPanel sessionId="touch-test"/>);
`;

async function debuggerConnection() {
 const profile=await mkdtemp(path.join(os.tmpdir(),'nexbox-terminal-touch-'));
 const child=spawn(process.env.CHROMIUM_PATH||'chromium',['--headless','--no-sandbox','--disable-gpu','--disable-dev-shm-usage','--no-first-run','--no-default-browser-check','--remote-debugging-port=0','--user-data-dir='+profile],{stdio:['ignore','ignore','pipe']});
 let socket;
 try {
  const endpoint=await new Promise((resolve,reject)=>{
   let data='';const timeout=setTimeout(()=>reject(new Error('Chrome startup timeout')),10000);
   child.once('error',reject);child.once('exit',code=>reject(new Error('Chrome exited '+code)));
   child.stderr.on('data',chunk=>{data+=chunk;const match=data.match(/DevTools listening on (ws:\/\/[^\s]+)/);if(match){clearTimeout(timeout);resolve(match[1]);}});
  });
  socket=new WebSocket(endpoint);await new Promise((resolve,reject)=>{socket.onopen=resolve;socket.onerror=reject;});
  let id=0;const pending=new Map();
  socket.onmessage=({data})=>{const message=JSON.parse(data),request=pending.get(message.id);if(!request)return;pending.delete(message.id);clearTimeout(request.timeout);if(message.error)request.reject(new Error(message.error.message));else request.resolve(message.result);};
  const send=(method,params={},sessionId)=>new Promise((resolve,reject)=>{
   const current=++id,timeout=setTimeout(()=>{pending.delete(current);reject(new Error('CDP timeout: '+method));},10000);
   pending.set(current,{resolve,reject,timeout});socket.send(JSON.stringify({id:current,method,params,...sessionId?{sessionId}:{}}));
  });
  return {send,close:async()=>{socket.close();child.kill();await new Promise(resolve=>child.exitCode!==null?resolve():child.once('exit',resolve));await rm(profile,{recursive:true,force:true});}};
 }catch(error){socket?.close();child.kill();await rm(profile,{recursive:true,force:true});throw error;}
}

const settle=()=>new Promise(resolve=>setTimeout(resolve,80));
test('real xterm supports mobile scrollback without hijacking other gestures',{timeout:60000},async()=>{
 const webDir=path.join(__dirname,'..');
 const actualXterm=require.resolve('@xterm/xterm');
 const bundle=await build({stdin:{contents:fixture,loader:'tsx',resolveDir:webDir},bundle:true,write:false,format:'iife',jsx:'automatic',alias:{'@':path.join(webDir,'src')},define:{'process.env.NODE_ENV':'"production"'},loader:{'.css':'empty'},plugins:[{name:'observe-real-xterm',setup(build){
  build.onResolve({filter:/^@xterm\/xterm$/},()=>({path:'observed-xterm',namespace:'fixture'}));
  build.onLoad({filter:/.*/,namespace:'fixture'},()=>({contents:'import {Terminal as RealTerminal} from '+JSON.stringify(actualXterm)+'; export class Terminal extends RealTerminal {constructor(options){super(options);window.term=this;}}',loader:'js',resolveDir:webDir}));
 }}]});
 const css=await readFile(require.resolve('@xterm/xterm/css/xterm.css'),'utf8');
 const html='<!doctype html><meta name="viewport" content="width=device-width,initial-scale=1"><style>'+css+'*{box-sizing:border-box}body{margin:0;background:#0d0d0d;color:white}#root{width:390px;height:380px}#root>div{width:100%;height:100%}.page-tail{height:1000px}</style><div id="root"></div><div class="page-tail"></div><script>'+bundle.outputFiles[0].text.replaceAll('</script','<\\/script')+'</script>';
 const server=http.createServer((req,res)=>{res.writeHead(200,{'Content-Type':'text/html'});res.end(html);});
 await new Promise(resolve=>server.listen(0,'127.0.0.1',resolve));
 let cdp;
 try {
  cdp=await debuggerConnection();const {send}=cdp;
  const {targetId}=await send('Target.createTarget',{url:'about:blank'});
  const {sessionId}=await send('Target.attachToTarget',{targetId,flatten:true});
  const call=(method,params)=>send(method,params,sessionId);
  const evaluate=async expression=>{const {result,exceptionDetails}=await call('Runtime.evaluate',{expression,returnByValue:true,awaitPromise:true});if(exceptionDetails)throw Error(exceptionDetails.text+': '+result.description);return result.value;};
  await call('Emulation.setDeviceMetricsOverride',{width:390,height:844,deviceScaleFactor:1,mobile:true});
  await call('Emulation.setTouchEmulationEnabled',{enabled:true,maxTouchPoints:5});
  await call('Page.navigate',{url:'http://127.0.0.1:'+server.address().port});
  let ready=false;for(let i=0;i<100;i++){ready=await evaluate('!!window.term && window.term.rows>0 && window.term.buffer.active.baseY>100');if(ready)break;await settle();}assert.ok(ready,'xterm did not render fixture output');
  const state=()=>evaluate('({y:term.buffer.active.viewportY,base:term.buffer.active.baseY,rows:term.rows,height:term.element.querySelector(".xterm-screen").clientHeight,pageY:scrollY})');
  const drag=async(dx,dy,multi=false)=>{
   const x=100,y=130;
   const points=(progress)=>multi?[{x:x+dx*progress,y:y+dy*progress,id:0},{x:x+100-dx*progress,y:y+dy*progress,id:1}]:[{x:x+dx*progress,y:y+dy*progress,id:0}];
   await call('Input.dispatchTouchEvent',{type:'touchStart',touchPoints:points(0)});
   for(let step=1;step<=8;step++){await call('Input.dispatchTouchEvent',{type:'touchMove',touchPoints:points(step/8)});await new Promise(resolve=>setTimeout(resolve,25));}
   await call('Input.dispatchTouchEvent',{type:'touchEnd',touchPoints:[]});await settle();
  };
  const initial=await state();
  await drag(0,144);const after=await state();
  const expected=144/(initial.height/initial.rows),moved=initial.y-after.y;
  assert.ok(moved>=Math.floor(expected)-1&&moved<=Math.ceil(expected)+1,'Vertical drag scrolled an unexpected number of lines: '+JSON.stringify({initial,after,expected}));
  assert.equal(after.pageY,initial.pageY,'Terminal swipe scrolled the outer page');
  const beforeHorizontal=after.y;await drag(140,2);assert.equal((await state()).y,beforeHorizontal,'Horizontal drag changed scrollback');
  await drag(0,-110);assert.ok((await state()).y>beforeHorizontal,'Reverse swipe did not scroll toward the prompt');
  await evaluate('term.select(0,term.buffer.active.baseY,5)');
  const beforeSelection=(await state()).y;await drag(0,80);assert.equal((await state()).y,beforeSelection,'A text selection was converted into a scroll gesture');
  await evaluate('term.clearSelection()');
  const beforeMulti=(await state()).y;await drag(20,60,true);assert.equal((await state()).y,beforeMulti,'Two-finger gesture changed scrollback');
  await evaluate('term.scrollToBottom()');await settle();const beforeWheel=(await state()).y;
  await call('Input.dispatchMouseEvent',{type:'mouseWheel',x:100,y:180,deltaX:0,deltaY:-100});await settle();
  assert.ok((await state()).y<beforeWheel,'Desktop wheel scrolling stopped working');
  assert.ok((await evaluate('getComputedStyle(document.getElementById("root").firstElementChild).touchAction')).includes('pinch-zoom'),'Pinch zoom is disabled');
  const {data}=await call('Page.captureScreenshot',{format:'png'});const screenshot=path.join(os.tmpdir(),'nexbox-terminal-touch.png');await writeFile(screenshot,Buffer.from(data,'base64'));
  assert.equal(await evaluate('window.sent.filter(data=>{try{return JSON.parse(data).type!=="resize"}catch{return true}}).length'),0,'Gestures sent shell input');
  await evaluate('new Promise(resolve=>term.write("\\x1b[?1049h",resolve))');
  await drag(0,100);assert.equal((await state()).y,0,'Alternate-screen application was scrolled as history');
  await evaluate(`window.oldContainer=document.getElementById('root').firstElementChild;window.oldTerm=term;window.oldScrolls=0;oldTerm.scrollLines=()=>oldScrolls++;window.root.unmount();`);
  await evaluate(`(()=>{const touch=(y)=>new Touch({identifier:0,target:oldContainer,clientX:100,clientY:y});oldContainer.dispatchEvent(new TouchEvent('touchstart',{touches:[touch(100)]}));oldContainer.dispatchEvent(new TouchEvent('touchmove',{touches:[touch(200)]}));})()`);
  assert.equal(await evaluate('oldScrolls'),0,'Unmounted terminal retained touch listeners');
  assert.equal(await evaluate('socket.closed'),true,'Unmount did not close the socket');
  console.log('PASS: real xterm, vertical/reverse scroll, selection, multiple fingers, wheel, page isolation, alternate screen and cleanup; screenshot '+screenshot);
  await send('Target.closeTarget',{targetId});
 } finally {await cdp?.close();await new Promise(resolve=>server.close(resolve));}
});
