"use strict";
// The UI reflects server-issued capabilities; hiding controls is not authorization.
function applyClientCapabilities(){
 const caps=state.me?.capabilities||{};
 for(const el of document.querySelectorAll("[data-capability]"))el.hidden=!caps[el.dataset.capability];
 for(const el of document.querySelectorAll("[data-owner-only]"))el.hidden=!caps.manageServer;
 $("#clients-title").textContent=caps.manageServer?"客户端管理":"我的连接";
 $("#add-principal").textContent=caps.manageIdentities?"＋ 添加参与者":"＋ 添加我的 Bot";
 const source=state.me?.source;
 $("#client-origin").textContent=caps.manageServer?"服务端本机管理会话":`${state.me?.principal?.name||"当前成员"} · ${source?.kind||"browser"} · ${source?.ip||"—"}`;
}
window.applyClientCapabilities=applyClientCapabilities;
window.clientsReset=()=>{$("#client-list").replaceChildren();$("#client-bind-code").value="";$("#client-bind-command").textContent="";$("#client-origin").textContent="";};
async function refreshClients(){
 if(!state.me)return;const epoch=state.authEpoch;
 try{const data=await api("/api/v1/clients");if(epoch!==state.authEpoch)return;const list=$("#client-list");list.replaceChildren();
  for(const c of data.clients){const row=textEl("div","","session-card");const who=state.peers.find(p=>p.id===c.principalId)?.name||c.principalId;row.append(textEl("strong",`${c.name}${c.id===data.currentClientId?" · 当前连接":""}`),textEl("p",`${c.kind} · ${who} · ${c.revokedAt?"已撤销":"有效至 "+new Date(c.expiresAt).toLocaleDateString()}`,"muted"));
   if(!c.revokedAt)row.append(actionButton("撤销此连接",async()=>{if(!confirm("撤销这一连接？其他客户端和 Bot 的凭证不会一起撤销。"))return;const r=await api(`/api/v1/clients/${encodeURIComponent(c.id)}`,"DELETE");if(r.currentRevoked)showLogin();else await refreshClients();}));list.append(row);
  }if(!data.clients.length)list.append(textEl("p","暂无客户端连接记录。","muted"));
 }catch(e){if(epoch===state.authEpoch)toast(e.message);}
}
$("#refresh-clients").addEventListener("click",refreshClients);
$("#bind-cli").addEventListener("click",async()=>{
 if(!state.me?.capabilities?.bindCLI)return;const epoch=state.authEpoch;
 try{const link=await api("/api/v1/client-links","POST",{});if(epoch!==state.authEpoch)return;
 $("#client-bind-title").textContent="绑定本机 CLI";$("#client-bind-hint").textContent="在这台电脑执行命令并输入绑定码。CLI 将获得独立凭证，绑定到当前身份。绑定码 2 分钟内有效。";
 $("#client-bind-command").textContent=`seven-air-link client bind ${location.origin} --fingerprint ${state.info.fingerprint} --name "My CLI" --config client.json`;
 $("#client-bind-code").value=link.code;$("#client-bind-dialog").showModal();}catch(e){toast(e.message);}
});
$("#enroll-node").addEventListener("click",async()=>{
 if(!state.me?.capabilities?.enrollNode)return;const epoch=state.authEpoch;
 try{const link=await api("/api/v1/client/device-pairings","POST",{});if(epoch!==state.authEpoch)return;
 $("#client-bind-title").textContent="连接本机 Node";$("#client-bind-hint").textContent="仅在需要共享这台电脑的本地端口时使用。配对后运行 node start；不要再启动一个 Server。配对码 5 分钟有效。";
 $("#client-bind-command").textContent=`seven-air-link join ${location.origin} --fingerprint ${state.info.fingerprint} --name "My PC" --config node.json`;
 $("#client-bind-code").value=link.code;$("#client-bind-dialog").showModal();}catch(e){toast(e.message);}
});
$("#copy-client-bind").addEventListener("click",()=>copy($("#client-bind-code").value));
$("#client-bind-dialog").addEventListener("close",()=>{$("#client-bind-code").value="";$("#client-bind-command").textContent="";});
document.addEventListener("slb-ready",()=>{applyClientCapabilities();refreshClients();});
document.addEventListener("slb-view",e=>{if(e.detail==="settings")refreshClients();});
Object.assign(labels,{"client.joined":"客户端已加入","client.bound":"CLI 已绑定","client.revoked":"客户端已撤销","client.node_invited":"创建本机 Node 配对","api.request":"发起操作"});
if(state.me){applyClientCapabilities();refreshClients();}
