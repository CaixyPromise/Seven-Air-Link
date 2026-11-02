"use strict";
window.loginMode="ticket";
let passwordEnabled=false,authRequest=0,localManagement=false;
function setLoginMode(mode){
 window.loginMode=mode;const password=mode==="password";$("#client-name").required=password;$("#password-login-fields").hidden=!password;$("#ticket-login-fields").hidden=password;$("#web-password").required=password;$("#ticket").required=!password;
 $("#mode-password").classList.toggle("active",password);$("#mode-ticket").classList.toggle("active",!password);
 $("#login-hint-title").textContent=password?(passwordEnabled?"加入协同空间":"尚未开启密码加入"):"使用自己的身份配置生成凭证";
 $("#login-command").textContent=password?"seven-air-link auth password set --config ./server-data/admin.json":"seven-air-link login create --config /path/to/identity.json";
 $("#login-hint-description").textContent=password?"输入服务提供者设置的加入密码。你的浏览器会获得自己的身份。":"凭证 2 分钟有效、只能使用一次。Human / Bot 使用各自的身份配置。";$("#login-submit").disabled=password&&!passwordEnabled;if(password){$("#login-command").textContent=localManagement?"本机管理：seven-air-link login create --config ./server-data/admin.json":"";}
}
for(const b of document.querySelectorAll("[data-login-mode]"))b.addEventListener("click",()=>{setLoginMode(b.dataset.loginMode);$("#login-error").textContent="";});
async function authRefreshMethods(){const request=++authRequest;try{const data=await api("/api/v1/auth/methods");if(request!==authRequest)return;passwordEnabled=!!data.password;localManagement=!!data.localManagement;setLoginMode(passwordEnabled?"password":"ticket");}catch{if(request===authRequest)$("#login-error").textContent="无法读取登录方式，请检查 Server 连接。";}}
window.authRefreshMethods=authRefreshMethods;
function authReset(){authRequest++;for(const id of ["web-password","current-web-password","new-web-password","confirm-web-password"])$("#"+id).value="";$("#session-list").replaceChildren();$("#password-status").textContent="尚未读取";}
window.authReset=authReset;
async function refreshAuthSettings(){
 const epoch=state.authEpoch;if(!state.me)return;
 try{
  if(state.me.capabilities?.manageServer){const status=await api("/api/v1/auth/password");if(epoch!==state.authEpoch)return;passwordEnabled=status.enabled;$("#password-status").textContent=status.enabled?"密码加入已启用 · 已有客户端凭证保持不变":"密码加入未启用 · 管理凭证仍可使用";$("#disable-password").disabled=!status.enabled;$("#current-web-password").disabled=!status.enabled;}
  const data=await api("/api/v1/sessions");if(epoch!==state.authEpoch)return;const list=$("#session-list");list.replaceChildren();
  for(const session of data.sessions){const card=textEl("div","","session-card");card.append(textEl("strong",`${session.current?"当前会话 · ":""}${session.method==="password"?"成员登录":"身份凭证"}`),textEl("p",`${session.ip} · 有效至 ${new Date(session.expiresAt).toLocaleString()}`,"muted"),textEl("p",session.userAgent,"session-agent"));card.append(actionButton("退出此会话",async()=>{if(!confirm("退出此网页会话？"))return;const r=await api(`/api/v1/sessions/${encodeURIComponent(session.id)}`,"DELETE");if(r.currentRevoked)showLogin();else refreshAuthSettings();}));list.append(card);}
  if(!data.sessions.length)list.append(textEl("p","没有有效网页会话。","muted"));
 }catch(e){if(epoch===state.authEpoch)toast(e.message);}
}
document.addEventListener("slb-ready",refreshAuthSettings);document.addEventListener("slb-view",e=>{if(e.detail==="settings")refreshAuthSettings();});$("#refresh-sessions").addEventListener("click",refreshAuthSettings);
$("#password-change-form").addEventListener("submit",async e=>{
 e.preventDefault();const password=$("#new-web-password").value,confirmation=$("#confirm-web-password").value;if(password!==confirmation){toast("两次输入的密码不一致。");return;}if([...password].length<12||new TextEncoder().encode(password).length>1024){toast("密码需至少 12 个字符，最多 1024 字节。");return;}
 const b=e.target.querySelector("button[type=submit]");b.disabled=true;
 try{await api("/api/v1/auth/password","PUT",{password,currentPassword:$("#current-web-password").value});await refreshAuthSettings();toast("加入密码已保存，已有客户端保持独立连接。");}catch(err){toast(err.code==="invalid_current_password"?"当前密码不正确。":err.message);}finally{b.disabled=false;for(const id of ["current-web-password","new-web-password","confirm-web-password"])$("#"+id).value="";}
});
$("#disable-password").addEventListener("click",async()=>{if(!confirm("关闭新客户端的密码加入？已有客户端不会被撤销。"))return;try{await api("/api/v1/auth/password","DELETE",{currentPassword:$("#current-web-password").value});await refreshAuthSettings();toast("密码加入已关闭。");}catch(e){toast(e.message);}finally{$("#current-web-password").value="";}});
$("#revoke-all-sessions").addEventListener("click",async()=>{if(!confirm("退出当前身份的所有网页会话（包括本页）？设备 / Bot 凭证不受影响。"))return;try{await api("/api/v1/sessions/all","DELETE");showLogin();}catch(e){toast(e.message);}});
authRefreshMethods();if(state.me)refreshAuthSettings();
