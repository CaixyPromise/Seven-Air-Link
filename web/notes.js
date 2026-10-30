"use strict";
// Drafts stay in this authenticated page; never reused by another identity.
let board=null;
const noteInput=$("#note-text");
function notesStatus(text,bad=false){$("#note-status").textContent=text;$("#note-status").classList.toggle("error",bad);}
function noteSize(){const bytes=new TextEncoder().encode(noteInput.value).length;$("#note-size").textContent=`${(bytes/1024).toFixed(1)} / 64 KiB`;return bytes;}
function notesReset(){if(board){clearTimeout(board.timer);board.stream?.close();}board=null;noteInput.value="";noteInput.disabled=true;$("#note-remote").value="";$("#note-conflict").hidden=true;$("#note-meta").textContent="";notesStatus("正在载入…");noteSize();}
window.notesReset=notesReset;
window.notesHasUnsaved=()=>!!(board&&(board.dirty||board.saving));
function currentBoard(b){return board===b&&b.epoch===state.authEpoch&&!!state.me;}
function renderNoteMeta(b){$("#note-meta").textContent=`版本 ${b.revision}${b.updatedBy?" · "+(state.peers.find(p=>p.id===b.updatedBy)?.name||b.updatedBy):""}${b.updatedAt?" · "+timeText(b.updatedAt):""}`;noteSize();}
function noteConflict(b,remote){b.remote=remote;clearTimeout(b.timer);$("#note-conflict").hidden=false;$("#note-remote").value=remote.text;$("#note-conflict-description").textContent=`服务器当前版本为 ${remote.revision}。请选择采用服务器内容，或明确确认将本机草稿保存为新版本。`;notesStatus("存在并发冲突 · 草稿尚未保存",true);}
function acceptNote(b,n,snapshot=false){
 if(!currentBoard(b)||!n||typeof n.text!=="string"||!Number.isSafeInteger(n.revision))return;
 if(b.loaded&&n.revision<b.revision)return;if(b.remote&&n.revision<b.remote.revision)return;if(b.loaded&&n.revision===b.revision&&!b.remote)return;
 const mine=b.pending&&n.revision===b.pending.base+1&&n.text===b.pending.text&&n.updatedBy===state.me.id;
 const localDraft=b.dirty||(b.pending&&noteInput.value!==b.pending.text);
 if(b.loaded&&localDraft&&n.text!==noteInput.value&&!mine){noteConflict(b,n);return;}
 if(!b.loaded||(!localDraft&&!mine)){noteInput.value=n.text;}
 b.loaded=true;noteInput.disabled=false;b.revision=n.revision;b.baseText=n.text;b.updatedBy=n.updatedBy;b.updatedAt=n.updatedAt;b.dirty=noteInput.value!==n.text;b.remote=null;$("#note-conflict").hidden=true;$("#note-remote").value="";
 notesStatus(b.dirty?"正在编辑 · 稍后自动保存":"已保存 · 实时同步");renderNoteMeta(b);if(b.dirty&&!b.saving)scheduleNote(b);
}
function scheduleNote(b){clearTimeout(b.timer);if(!currentBoard(b)||!b.loaded||!b.dirty||b.remote||b.saving||b.composing)return;if(noteSize()>65536){notesStatus("内容超过 64 KiB，尚未保存",true);return;}b.timer=setTimeout(()=>saveNote(b),800);}
async function saveNote(b){
 if(!currentBoard(b)||!b.loaded||!b.dirty||b.remote||b.saving||b.composing)return;if(noteSize()>65536){notesStatus("内容超过 64 KiB，尚未保存",true);return;}
 const sent=noteInput.value,base=b.revision;b.pending={text:sent,base};b.saving=true;notesStatus("正在保存…");let retry=false;
 try{const n=await api("/api/v1/note","PUT",{text:sent,baseRevision:base});if(!currentBoard(b))return;
  if(!b.remote||b.remote.revision<=n.revision){b.revision=n.revision;b.baseText=n.text;b.updatedAt=n.updatedAt;b.updatedBy=n.updatedBy;b.dirty=noteInput.value!==n.text;b.remote=null;$("#note-conflict").hidden=true;renderNoteMeta(b);notesStatus(b.dirty?"继续编辑中 · 待保存":"已保存 · 实时同步");retry=b.dirty;}
 }catch(e){if(!currentBoard(b))return;if(e.code==="note_revision_conflict"&&e.data?.note){if(e.data.note.text===noteInput.value){b.dirty=false;acceptNote(b,e.data.note,true);}else noteConflict(b,e.data.note);}else notesStatus("保存失败，草稿已保留："+e.message,true);
 }finally{if(currentBoard(b)){b.saving=false;b.pending=null;if(retry)scheduleNote(b);}}
}
async function fetchNote(b){try{const n=await api("/api/v1/note");if(currentBoard(b))acceptNote(b,n,true);}catch(e){if(currentBoard(b))notesStatus("读取失败："+e.message,true);}}
function startNotes(){
 notesReset();const b={epoch:state.authEpoch,revision:0,baseText:"",dirty:false,saving:false,loaded:false,pending:null,remote:null,timer:null,stream:null};board=b;fetchNote(b);
 const stream=new EventSource("/api/v1/note/events");b.stream=stream;
 for(const kind of ["note.snapshot","note.updated"])stream.addEventListener(kind,e=>{try{acceptNote(b,JSON.parse(e.data),kind==="note.snapshot");}catch{if(currentBoard(b))notesStatus("便签同步数据异常，请重新同步。",true);}});
 stream.onopen=()=>{if(currentBoard(b)&&b.loaded&&!b.dirty&&!b.remote)notesStatus("已保存 · 实时同步");};
 stream.onerror=()=>{if(currentBoard(b))notesStatus(b.dirty?"同步连接中断 · 本机草稿已保留":"已保存 · 正在重连",b.dirty);};
 stream.addEventListener("session.expired",()=>{if(currentBoard(b)){showLogin();toast("会话已失效，请重新登录。");}});
}
document.addEventListener("slb-ready",startNotes);
noteInput.addEventListener("compositionstart",()=>{if(board){board.composing=true;clearTimeout(board.timer);}});
noteInput.addEventListener("compositionend",()=>{if(board){board.composing=false;scheduleNote(board);}});
noteInput.addEventListener("input",()=>{const b=board;if(!b||!b.loaded)return;b.dirty=noteInput.value!==b.baseText;noteSize();if(!b.remote){notesStatus(b.dirty?"正在编辑 · 稍后自动保存":"已保存 · 实时同步");scheduleNote(b);}});
$("#note-retry").addEventListener("click",async()=>{const b=board;if(!b)return;await fetchNote(b);if(currentBoard(b)&&b.dirty&&!b.remote)saveNote(b);});
$("#note-copy").addEventListener("click",()=>copy(noteInput.value));$("#note-copy-draft").addEventListener("click",()=>copy(noteInput.value));
$("#note-use-remote").addEventListener("click",()=>{const b=board;if(!b?.remote)return;if(!confirm("放弃当前未保存草稿，采用服务器版本？可先复制本机草稿。"))return;const n=b.remote;b.dirty=false;acceptNote(b,n,true);});
$("#note-keep-local").addEventListener("click",()=>{const b=board;if(!b?.remote||b.saving)return;if(!confirm("确认将本机草稿保存到服务器？这会替换当前显示的服务器内容，并重新校验版本。"))return;const n=b.remote;b.revision=n.revision;b.baseText=n.text;b.remote=null;$("#note-conflict").hidden=true;b.dirty=noteInput.value!==b.baseText;if(b.dirty)saveNote(b);else acceptNote(b,n,true);});
window.addEventListener("beforeunload",e=>{if(board&&(board.dirty||board.saving)){e.preventDefault();e.returnValue="";}});
Object.assign(labels,{"note.updated":"更新共享便签","auth.password_changed":"修改网页登录密码","auth.password_disabled":"关闭密码登录","auth.password_denied":"密码登录失败","browser.sessions_revoked":"撤销网页会话"});
if(state.me&&!board)startNotes();
