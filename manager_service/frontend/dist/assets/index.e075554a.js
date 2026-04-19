(function(){const t=document.createElement("link").relList;if(t&&t.supports&&t.supports("modulepreload"))return;for(const d of document.querySelectorAll('link[rel="modulepreload"]'))f(d);new MutationObserver(d=>{for(const c of d)if(c.type==="childList")for(const p of c.addedNodes)p.tagName==="LINK"&&p.rel==="modulepreload"&&f(p)}).observe(document,{childList:!0,subtree:!0});function i(d){const c={};return d.integrity&&(c.integrity=d.integrity),d.referrerpolicy&&(c.referrerPolicy=d.referrerpolicy),d.crossorigin==="use-credentials"?c.credentials="include":d.crossorigin==="anonymous"?c.credentials="omit":c.credentials="same-origin",c}function f(d){if(d.ep)return;d.ep=!0;const c=i(d);fetch(d.href,c)}})();const S={AUTH_CHANGED:"auth:changed",TAB_CHANGED:"tab:changed",STATUS_MESSAGE:"status:message",ERROR_MESSAGE:"error:message",NETWORK_STATUS_REFRESH_REQUESTED:"network:status:refresh:requested",NETWORK_STATUS_REFRESHED:"network:status:refreshed",LOG_VIEWER_REFRESH_REQUESTED:"log-viewer:refresh:requested",LOG_VIEWER_REFRESHED:"log-viewer:refreshed"};function he(){const s=new Map;function t(c,p){return s.has(c)||s.set(c,new Set),s.get(c).add(p),()=>i(c,p)}function i(c,p){const g=s.get(c);!g||(g.delete(p),g.size===0&&s.delete(c))}function f(c,p){const g=s.get(c);if(!(!g||g.size===0))for(const h of g)try{h(p)}catch(y){console.error("[event-bus] listener failed",c,y)}}function d(){s.clear()}return{on:t,off:i,emit:f,clear:d}}const V="http://127.0.0.1:15030";function L(s,t=""){try{const i=window.localStorage.getItem(s);return i==null?t:i}catch{return t}}function N(){return{ui:{activeTab:"overview",statusMessage:"",errorMessage:""},auth:{sessionToken:L("manager_session_token",""),username:"admin",userRole:"viewer",certType:"viewer",isAuthenticating:!1,loginTone:"info",loginStatus:"Please login"},settings:{baseUrl:L("controller_base_url",V)||V,baseUrlStatus:"",isLoadingBaseUrl:!1,isSavingBaseUrl:!1,controllerIP:L("controller_ip",""),controllerIPStatus:"",isLoadingControllerIP:!1,isSavingControllerIP:!1,upgradeProject:L("cloudhelper.manager.upgrade_project","fengzhanhuaer/CloudHelper"),aiDebugListenEnabled:!1,aiDebugListenStatus:"AI Debug not supported in web mode",isLoadingAIDebugListenEnabled:!1,isSavingAIDebugListenEnabled:!1},connection:{wsStatus:"",serverStatus:"",adminStatus:""},probeManage:{isLoading:!1,status:"\u672A\u52A0\u8F7D\u63A2\u9488\u8282\u70B9",nodes:[],deletedNodes:[],selectedNodeNo:0,selectedNodeStatus:null,nodeLogs:"",nodeLogsStatus:""},upgrade:{managerVersion:"...",controllerVersion:"\u2014",controllerLatestVersion:"\u2014",versionStatus:"\u672A\u68C0\u67E5\u7248\u672C",mergedUpgradeStatus:"\u672A\u5347\u7EA7",mergedUpgradeMessages:[],controllerUpgradeProgress:{active:!1,phase:"idle",percent:0,message:""},managerUpgradeProgress:{active:!1,phase:"idle",percent:0,message:""},isUpgradingController:!1,isUpgradingManager:!1,isCheckingDirect:!1,isCheckingProxy:!1,directRelease:null,proxyRelease:null,backupEnabled:!1,backupRcloneRemote:"",backupSettingsStatus:"\u672A\u52A0\u8F7D",isLoadingBackupSettings:!1,isSavingBackupSettings:!1,isTestingBackupSettings:!1},network:{status:{enabled:!1,mode:"direct",node_id:"direct",available_nodes:["direct"],socks5_listen:"127.0.0.1:10808",tunnel_route:"/api/ws/tunnel/direct",tunnel_status:"\u672A\u542F\u7528",system_proxy_status:"\u672A\u8BBE\u7F6E",last_error:"",mux_connected:!1,mux_active_streams:0,mux_reconnects:0,mux_last_recv:"",mux_last_pong:"",group_keepalive:[],tun_supported:!1,tun_installed:!1,tun_enabled:!1,tun_library_path:"",tun_status:"\u672A\u5B89\u88C5"},isOperating:!1,operateStatus:"\u672A\u64CD\u4F5C",selectedNode:"direct",ruleConfig:null,isLoadingRuleConfig:!1,ruleConfigStatus:"\u89C4\u5219\u7B56\u7565\u672A\u52A0\u8F7D",isSyncingRuleRoutes:!1,ruleRoutesSyncStatus:"\u89C4\u5219\u6587\u4EF6\u4E3B\u63A7\u5907\u4EFD\uFF1A\u672A\u6267\u884C",dnsCacheEntries:[],dnsCacheQuery:"",isDNSCacheLoading:!1,dnsCacheStatus:"",processList:[],isLoadingProcesses:!1,processListStatus:"",monitorProcessName:"",isMonitoring:!1,processEvents:[],processEventsStatus:"",logLines:200,isLoadingLogs:!1,logStatus:"\u672A\u52A0\u8F7D\u7F51\u7EDC\u52A9\u624B\u65E5\u5FD7",logCopyStatus:"",logContent:"",logSourceFilter:"all",logCategoryFilter:"all",logCategories:[],logVisibleCount:0,logTotalCount:0,logAutoScroll:!0},logViewer:{source:"local",lines:200,sinceMinutes:0,minLevel:"normal",autoScroll:!0,isLoading:!1,status:"\u672A\u52A0\u8F7D\u65E5\u5FD7",copyStatus:"",logFilePath:"",content:""},cloudflare:{apiKey:"",zoneName:"",records:[],status:"\u672A\u52A0\u8F7D Cloudflare \u914D\u7F6E",isLoading:!1},tg:{apiId:"",apiHash:"",accounts:[],schedules:[],status:"\u672A\u52A0\u8F7D TG \u914D\u7F6E",isLoading:!1}}}function Se(){let s=N();const t=new Set;function i(){return s}function f(g){s={...s,...g};for(const h of t)try{h(s)}catch(y){console.error("[store] listener failed",y)}}function d(g,h){const y={...s[g]};s={...s,[g]:{...y,...h}};for(const _ of t)try{_(s)}catch(w){console.error("[store] listener failed",w)}}function c(g){return t.add(g),()=>t.delete(g)}function p(){s=N();for(const g of t)try{g(s)}catch(h){console.error("[store] listener failed",h)}}return{getState:i,setState:f,update:d,subscribe:c,reset:p}}const ye=[{key:"overview",label:"\u6982\u8981\u72B6\u6001"},{key:"probe-manage",label:"\u63A2\u9488\u7BA1\u7406"},{key:"network-assistant",label:"\u7F51\u7EDC\u52A9\u624B"},{key:"cloudflare-assistant",label:"Cloudflare\u52A9\u624B"},{key:"tg-assistant",label:"TG\u52A9\u624B"},{key:"log-viewer",label:"\u65E5\u5FD7\u67E5\u770B"},{key:"system-settings",label:"\u7CFB\u7EDF\u8BBE\u7F6E"}],we=[{key:"overview",label:"\u6982\u8981\u72B6\u6001"},{key:"probe-manage",label:"\u63A2\u9488\u7BA1\u7406"},{key:"network-assistant",label:"\u7F51\u7EDC\u52A9\u624B"},{key:"cloudflare-assistant",label:"Cloudflare\u52A9\u624B"},{key:"tg-assistant",label:"TG\u52A9\u624B"},{key:"log-viewer",label:"\u65E5\u5FD7\u67E5\u770B"}],me=[{key:"overview",label:"\u6982\u8981\u72B6\u6001"}];function B(s,t){return String(s||"").trim().toLowerCase()||t}function ke(s,t){return String(s||"").trim()||t}function G(s,t){const i=B(s,"viewer"),f=B(t,i);return i==="admin"||f==="admin"?ye:i==="operator"||f==="operator"||f==="ops"?we:me}const Ee="/api";async function v(s,t={}){const i=localStorage.getItem("manager_session_token"),f=new Headers(t.headers||{});i&&(f.set("X-Session-Token",i),f.set("Authorization",`Bearer ${i}`)),f.set("Content-Type","application/json");const d=await fetch(s.startsWith("http")?s:`${Ee}${s}`,{...t,headers:f});if(!d.ok){d.status===401&&(localStorage.removeItem("manager_session_token"),window.dispatchEvent(new Event("unauthorized")));const p=await d.text();let g="";try{g=JSON.parse(p).message||p}catch{g=p}throw new Error(g||`HTTP error ${d.status}`)}const c=await d.json();if(c&&typeof c.code=="number"){if(c.code!==0)throw new Error(c.message||"Unknown error");return c.data}return c}async function Le(s,t){return v("/auth/login",{method:"POST",body:JSON.stringify({username:s,password:t})})}async function _e(){await v("/auth/logout",{method:"POST"}).catch(()=>{})}async function O(){return v("/system/version")}async function $e(){return v("/probe/nodes")}async function Te(s){const t=s!==void 0?`?node_no=${s}`:"";return v(`/probe/nodes/status${t}`)}async function Ae(s,t){const i=new URLSearchParams;return t.lines&&i.set("lines",String(t.lines)),t.sinceMinutes&&i.set("since_minutes",String(t.sinceMinutes)),t.minLevel&&i.set("min_level",t.minLevel),v(`/probe/nodes/${s}/logs?${i.toString()}`)}async function Ce(){return v("/network-assistant/status")}async function Pe(s){return v("/network-assistant/mode",{method:"POST",body:JSON.stringify({mode:s})})}async function Re(s=200){return v(`/network-assistant/logs?lines=${s}`)}async function Me(s){const t=new URLSearchParams;return s.lines&&t.set("lines",String(s.lines)),s.sinceMinutes&&t.set("since_minutes",String(s.sinceMinutes)),s.minLevel&&t.set("min_level",s.minLevel),v(`/logs/manager?${t.toString()}`)}async function Ue(){return v("/cloudflare/api-key")}async function Ie(s){return v("/cloudflare/api-key",{method:"POST",body:JSON.stringify({api_key:s})})}async function Ve(){return v("/cloudflare/zone")}async function Ne(s){return v("/cloudflare/zone",{method:"POST",body:JSON.stringify({zone_name:s})})}async function Be(){return v("/cloudflare/ddns/records")}async function Ge(){return v("/tg/accounts")}async function Oe(){return v("/tg/schedules")}async function qe(s){const t=new URLSearchParams;s!=null&&s.lines&&t.set("lines",String(s.lines)),s!=null&&s.sinceMinutes&&t.set("since_minutes",String(s.sinceMinutes)),s!=null&&s.minLevel&&t.set("min_level",s.minLevel);const i=t.toString();return v(`/system/controller-logs${i?`?${i}`:""}`)}async function De(){return v("/system/controller-version")}const A="http://127.0.0.1:15030";function a(s){const t=document.createElement("div");return t.innerText=String(s!=null?s:""),t.innerHTML}function q(s,t=""){try{const i=window.localStorage.getItem(s);return i==null?t:i}catch{return t}}function xe(s){const t=Se(),i=he();let f=!1,d=null;function c(){const e=t.getState(),n=G(e.auth.userRole,e.auth.certType);n.some(o=>o.key===e.ui.activeTab)||t.update("ui",{activeTab:n[0].key})}function p(e,n="info"){t.update("auth",{loginStatus:e,loginTone:n})}function g(e){t.update("ui",{statusMessage:e,errorMessage:""}),i.emit(S.STATUS_MESSAGE,{message:e})}function h(e){t.update("ui",{errorMessage:e}),i.emit(S.ERROR_MESSAGE,{message:e})}async function y(e){e.preventDefault();const n=e.currentTarget,o=new FormData(n),r=String(o.get("username")||"").trim(),u=String(o.get("password")||"");if(!r||!u){p("Login failed: username and password required","error");return}t.update("auth",{isAuthenticating:!0}),p("Authenticating...","info");try{const l=await Le(r,u),b=String((l==null?void 0:l.token)||""),E=ke(l==null?void 0:l.username,"admin");if(!b)throw new Error("empty session token");window.localStorage.setItem("manager_session_token",b),t.update("auth",{sessionToken:b,username:E,userRole:"admin",certType:"admin",loginTone:"success",loginStatus:`Login successful: username=${E}`}),c(),i.emit(S.AUTH_CHANGED,{loggedIn:!0,token:b}),await Promise.allSettled([w(),$(),T()]),g("\u767B\u5F55\u6210\u529F")}catch(l){const b=l instanceof Error?l.message:"unknown error";window.localStorage.removeItem("manager_session_token"),t.update("auth",{sessionToken:"",loginTone:"error",loginStatus:`Login failed: ${b}`}),h(`\u767B\u5F55\u5931\u8D25: ${b}`)}finally{t.update("auth",{isAuthenticating:!1}),m()}}async function _(){await _e(),window.localStorage.removeItem("manager_session_token"),t.update("auth",{sessionToken:"",username:"admin",userRole:"viewer",certType:"viewer",loginTone:"info",loginStatus:"Logged out",isAuthenticating:!1}),t.update("connection",{serverStatus:"",adminStatus:"",wsStatus:""}),i.emit(S.AUTH_CHANGED,{loggedIn:!1}),g("\u5DF2\u9000\u51FA\u767B\u5F55"),m()}async function w(){if(!!t.getState().auth.sessionToken){try{const n=await O();t.update("connection",{serverStatus:`manager_service \u5728\u7EBF\uFF0C\u7248\u672C\uFF1A${(n==null?void 0:n.version)||"unknown"}`})}catch(n){const o=n instanceof Error?n.message:"unknown";t.update("connection",{serverStatus:`manager_service \u72B6\u6001\u5F02\u5E38\uFF1A${o}`})}try{await v("/healthz"),t.update("connection",{adminStatus:"manager_service \u5065\u5EB7\u68C0\u67E5\u6B63\u5E38"})}catch{t.update("connection",{adminStatus:"manager_service \u5065\u5EB7\u68C0\u67E5\u5931\u8D25"})}}}async function $(){var e,n,o;t.update("upgrade",{versionStatus:"\u6B63\u5728\u68C0\u67E5\u7248\u672C..."});try{const[r,u]=await Promise.allSettled([O(),De()]),l=r.status==="fulfilled"?((e=r.value)==null?void 0:e.version)||"unknown":"error",b=u.status==="fulfilled"&&((n=u.value)==null?void 0:n.current_version)||"\u2014",E=u.status==="fulfilled"&&((o=u.value)==null?void 0:o.latest_version)||"\u2014";t.update("upgrade",{managerVersion:l,controllerVersion:b,controllerLatestVersion:E,versionStatus:u.status==="fulfilled"?`manager ${l} | controller ${b}`:`manager ${l} | \u4E3B\u63A7\u7248\u672C\u67E5\u8BE2\u5931\u8D25`})}catch(r){const u=r instanceof Error?r.message:"unknown";t.update("upgrade",{versionStatus:`\u7248\u672C\u68C0\u67E5\u5931\u8D25\uFF1A${u}`})}}function C(){const e=q("controller_base_url",A)||A,n=q("controller_ip","");t.update("settings",{baseUrl:e,controllerIP:n,baseUrlStatus:`Controller URL loaded: ${e}`,controllerIPStatus:n?`Using controller IP: ${n}`:"Controller IP not configured"})}function x(e){const n=String(e||"").trim()||A;window.localStorage.setItem("controller_base_url",n),t.update("settings",{baseUrl:n,baseUrlStatus:`Controller URL saved: ${n}`}),g("\u4E3B\u63A7\u5730\u5740\u5DF2\u4FDD\u5B58")}function z(e){const n=String(e||"").trim();window.localStorage.setItem("controller_ip",n),t.update("settings",{controllerIP:n,controllerIPStatus:n?`Controller IP saved: ${n}`:"Controller IP cleared"}),g("\u4E3B\u63A7 IP \u5DF2\u4FDD\u5B58")}async function H(){const e=t.getState(),n=e.logViewer.source,o=e.logViewer.lines,r=e.logViewer.sinceMinutes,u=e.logViewer.minLevel;t.update("logViewer",{isLoading:!0,status:`\u6B63\u5728\u5237\u65B0${n==="local"?"\u672C\u5730":"\u670D\u52A1\u5668"}\u65E5\u5FD7...`});try{if(n==="local"){const l=await Me({lines:o,sinceMinutes:r,minLevel:u}),b=String((l==null?void 0:l.content)||"");t.update("logViewer",{isLoading:!1,status:`\u5DF2\u52A0\u8F7D\u672C\u5730\u65E5\u5FD7 (${o} \u884C)`,content:b,logFilePath:String((l==null?void 0:l.file_path)||""),copyStatus:""})}else{const l=await qe({lines:o,sinceMinutes:r,minLevel:u});t.update("logViewer",{isLoading:!1,status:`\u5DF2\u52A0\u8F7D\u4E3B\u63A7\u65E5\u5FD7 (${o} \u884C)`,content:String((l==null?void 0:l.content)||""),logFilePath:String((l==null?void 0:l.file_path)||""),copyStatus:""})}i.emit(S.LOG_VIEWER_REFRESHED,{source:n})}catch(l){const b=l instanceof Error?l.message:"unknown";t.update("logViewer",{isLoading:!1,status:`\u65E5\u5FD7\u52A0\u8F7D\u5931\u8D25\uFF1A${b}`})}}async function F(){var n;const e=String(t.getState().logViewer.content||"").trim();if(!e){t.update("logViewer",{copyStatus:"\u6682\u65E0\u65E5\u5FD7\u53EF\u590D\u5236"});return}try{if((n=navigator==null?void 0:navigator.clipboard)!=null&&n.writeText)await navigator.clipboard.writeText(e);else{const o=document.createElement("textarea");o.value=e,o.style.cssText="position:fixed;opacity:0",document.body.appendChild(o),o.select(),document.execCommand("copy"),o.remove()}t.update("logViewer",{copyStatus:"\u5DF2\u590D\u5236\u65E5\u5FD7\u5185\u5BB9"})}catch(o){const r=o instanceof Error?o.message:"unknown";t.update("logViewer",{copyStatus:`\u590D\u5236\u5931\u8D25\uFF1A${r}`})}}async function T(){try{const e=await Ce();t.update("network",{status:{...t.getState().network.status,...e},operateStatus:"\u72B6\u6001\u5DF2\u5237\u65B0"}),i.emit(S.NETWORK_STATUS_REFRESHED,{mode:(e==null?void 0:e.mode)||"direct"})}catch(e){const n=e instanceof Error?e.message:"unknown";t.update("network",{operateStatus:`\u72B6\u6001\u5237\u65B0\u5931\u8D25\uFF1A${n}`})}}async function P(e){t.update("network",{isOperating:!0,operateStatus:"\u6B63\u5728\u5207\u6362\u6A21\u5F0F..."});try{const n=await Pe(e);t.update("network",{isOperating:!1,status:{...t.getState().network.status,...n},operateStatus:e==="tun"?"\u5DF2\u5207\u6362\u4E3A TUN \u6A21\u5F0F":"\u5DF2\u5207\u6362\u4E3A\u76F4\u8FDE\u6A21\u5F0F"}),await R()}catch(n){const o=n instanceof Error?n.message:"unknown";t.update("network",{isOperating:!1,operateStatus:`\u6A21\u5F0F\u5207\u6362\u5931\u8D25\uFF1A${o}`})}}async function R(){const e=t.getState().network.logLines;t.update("network",{isLoadingLogs:!0,logStatus:"\u6B63\u5728\u5237\u65B0\u7F51\u7EDC\u52A9\u624B\u65E5\u5FD7..."});try{const n=await Re(e),o=String((n==null?void 0:n.content)||"");t.update("network",{isLoadingLogs:!1,logStatus:"\u7F51\u7EDC\u52A9\u624B\u65E5\u5FD7\u5DF2\u52A0\u8F7D",logContent:o,logTotalCount:o?o.split(/\r?\n/).filter(Boolean).length:0,logVisibleCount:o?o.split(/\r?\n/).filter(Boolean).length:0})}catch(n){const o=n instanceof Error?n.message:"unknown";t.update("network",{isLoadingLogs:!1,logStatus:`\u7F51\u7EDC\u52A9\u624B\u65E5\u5FD7\u52A0\u8F7D\u5931\u8D25\uFF1A${o}`})}}async function M(){t.update("probeManage",{isLoading:!0,status:"\u6B63\u5728\u52A0\u8F7D\u63A2\u9488\u8282\u70B9..."});try{const[e,n]=await Promise.all([$e(),Te()]),o=new Map;(Array.isArray(n)?n:[]).forEach(l=>{o.set(l.node_no,l)});const r=(Array.isArray(e)?e:[]).map(l=>{const b=o.get(l.node_no)||{};return{...l,online:!!b.online,last_seen:b.last_seen||"",version:b.version||""}}),u=r.length>0?r[0].node_no:0;t.update("probeManage",{isLoading:!1,status:`\u5DF2\u52A0\u8F7D\u63A2\u9488\u8282\u70B9: ${r.length}`,nodes:r,selectedNodeNo:u}),u&&await K(u)}catch(e){const n=e instanceof Error?e.message:"unknown";t.update("probeManage",{isLoading:!1,status:`\u63A2\u9488\u8282\u70B9\u52A0\u8F7D\u5931\u8D25: ${n}`})}}async function K(e){try{const n=await Ae(e,{lines:120});t.update("probeManage",{nodeLogs:String((n==null?void 0:n.content)||""),nodeLogsStatus:`\u8282\u70B9 ${e} \u65E5\u5FD7\u5DF2\u52A0\u8F7D`})}catch(n){const o=n instanceof Error?n.message:"unknown";t.update("probeManage",{nodeLogsStatus:`\u8282\u70B9\u65E5\u5FD7\u52A0\u8F7D\u5931\u8D25: ${o}`})}}async function k(){t.update("cloudflare",{isLoading:!0,status:"\u6B63\u5728\u52A0\u8F7D Cloudflare \u914D\u7F6E..."});try{const[e,n,o]=await Promise.all([Ue(),Ve(),Be()]);t.update("cloudflare",{isLoading:!1,apiKey:String((e==null?void 0:e.api_key)||""),zoneName:String((n==null?void 0:n.zone_name)||""),records:Array.isArray(o==null?void 0:o.records)?o.records:[],status:"Cloudflare \u914D\u7F6E\u5DF2\u52A0\u8F7D"})}catch(e){const n=e instanceof Error?e.message:"unknown";t.update("cloudflare",{isLoading:!1,status:`Cloudflare \u914D\u7F6E\u52A0\u8F7D\u5931\u8D25: ${n}`})}}async function j(e){try{await Ie(e),g("Cloudflare API Key \u5DF2\u4FDD\u5B58"),await k()}catch(n){const o=n instanceof Error?n.message:"unknown";h(`\u4FDD\u5B58 Cloudflare API Key \u5931\u8D25: ${o}`)}}async function W(e){try{await Ne(e),g("Cloudflare Zone \u5DF2\u4FDD\u5B58"),await k()}catch(n){const o=n instanceof Error?n.message:"unknown";h(`\u4FDD\u5B58 Cloudflare Zone \u5931\u8D25: ${o}`)}}async function U(){t.update("tg",{isLoading:!0,status:"\u6B63\u5728\u52A0\u8F7D TG \u914D\u7F6E..."});try{const[e,n]=await Promise.all([Ge(),Oe()]),o=Array.isArray(e==null?void 0:e.accounts)?e.accounts:[],r=Array.isArray(n==null?void 0:n.schedules)?n.schedules:[];t.update("tg",{isLoading:!1,accounts:o,schedules:r,status:`TG \u6570\u636E\u5DF2\u52A0\u8F7D: \u8D26\u53F7 ${o.length} / \u4EFB\u52A1 ${r.length}`})}catch(e){const n=e instanceof Error?e.message:"unknown";t.update("tg",{isLoading:!1,status:`TG \u6570\u636E\u52A0\u8F7D\u5931\u8D25: ${n}`})}}function Z(e){return`
      <div id="App">
        <img src="./src/assets/images/site-icon.png" id="logo" alt="logo" />
        <form id="login-form" class="panel login-panel">
          <div class="row">
            <label for="username">Username</label>
            <input id="username" name="username" class="input" value="admin" ${e.auth.isAuthenticating?"disabled":""} />
          </div>
          <div class="row">
            <label for="password">Password</label>
            <input id="password" name="password" class="input" type="password" ${e.auth.isAuthenticating?"disabled":""} />
          </div>
          <div class="btn-row">
            <button class="btn" type="submit" ${e.auth.isAuthenticating?"disabled":""}>
              ${e.auth.isAuthenticating?"Logging in...":"Login"}
            </button>
          </div>
          <div class="status auth-status ${a(e.auth.loginTone)}">${a(e.auth.loginStatus)}</div>
        </form>
      </div>
    `}function J(e){return`
      <section class="content-block">
        <h2>\u6982\u8981\u72B6\u6001</h2>
        <div class="identity-card">
          <div>\u7528\u6237: ${a(e.auth.username)}</div>
          <div>\u89D2\u8272: ${a(e.auth.userRole)}</div>
          <div>\u8BC1\u4E66: ${a(e.auth.certType)}</div>
          <div>\u670D\u52A1\u72B6\u6001: ${a(e.connection.serverStatus||"\u672A\u68C0\u67E5")}</div>
          <div>\u5065\u5EB7\u68C0\u67E5: ${a(e.connection.adminStatus||"\u672A\u68C0\u67E5")}</div>
        </div>
        <div class="content-actions">
          <button class="btn" id="btn-refresh-overview">\u5237\u65B0\u72B6\u6001</button>
        </div>
      </section>
    `}function Q(e){return`
      <section class="content-block">
        <h2>\u7CFB\u7EDF\u8BBE\u7F6E</h2>
        <div class="identity-card">
          <div>Manager \u7248\u672C: ${a(e.upgrade.managerVersion)}</div>
          <div>Controller \u7248\u672C: ${a(e.upgrade.controllerVersion)}</div>
          <div>Controller \u6700\u65B0: ${a(e.upgrade.controllerLatestVersion)}</div>
          <div>${a(e.upgrade.versionStatus)}</div>
        </div>
        <div class="content-actions">
          <button class="btn" id="btn-refresh-versions">\u5237\u65B0\u7248\u672C</button>
        </div>
        <div class="row" style="margin-top:12px;">
          <label for="settings-base-url">Controller URL</label>
          <input id="settings-base-url" class="input" value="${a(e.settings.baseUrl)}" />
        </div>
        <div class="row">
          <label for="settings-controller-ip">Controller IP</label>
          <input id="settings-controller-ip" class="input" value="${a(e.settings.controllerIP)}" />
        </div>
        <div class="content-actions">
          <button class="btn" id="btn-save-base-url">\u4FDD\u5B58\u4E3B\u63A7\u5730\u5740</button>
          <button class="btn" id="btn-save-controller-ip">\u4FDD\u5B58\u4E3B\u63A7IP</button>
          <button class="btn" id="btn-refresh-settings">\u91CD\u65B0\u8BFB\u53D6\u8BBE\u7F6E</button>
        </div>
        <div class="status">${a(e.settings.baseUrlStatus||e.settings.controllerIPStatus||"")}</div>
      </section>
    `}function X(e){return`
      <section class="content-block">
        <h2>\u65E5\u5FD7\u67E5\u770B</h2>
        <div class="row">
          <label for="log-source">\u65E5\u5FD7\u6765\u6E90</label>
          <select id="log-source" class="input">
            <option value="local" ${e.logViewer.source==="local"?"selected":""}>local</option>
            <option value="server" ${e.logViewer.source==="server"?"selected":""}>server</option>
          </select>
        </div>
        <div class="row">
          <label for="log-lines">\u65E5\u5FD7\u884C\u6570</label>
          <input id="log-lines" class="input" type="number" value="${a(e.logViewer.lines)}" />
        </div>
        <div class="content-actions">
          <button class="btn" id="btn-refresh-logs" ${e.logViewer.isLoading?"disabled":""}>\u5237\u65B0\u65E5\u5FD7</button>
          <button class="btn" id="btn-copy-logs">\u590D\u5236\u65E5\u5FD7</button>
        </div>
        <div class="status">${a(e.logViewer.status)}</div>
        <div class="status">${a(e.logViewer.copyStatus)}</div>
        <pre class="log-viewer-output">${a(e.logViewer.content)}</pre>
      </section>
    `}function Y(e){return`
      <section class="content-block">
        <h2>\u7F51\u7EDC\u52A9\u624B</h2>
        <div class="identity-card">
          <div>\u5F53\u524D\u6A21\u5F0F: ${a(e.network.status.mode||"direct")}</div>
          <div>\u8282\u70B9: ${a(e.network.status.node_id||"direct")}</div>
          <div>TUN \u72B6\u6001: ${a(e.network.status.tun_status||"\u672A\u5B89\u88C5")}</div>
          <div>\u7CFB\u7EDF\u4EE3\u7406: ${a(e.network.status.system_proxy_status||"\u672A\u8BBE\u7F6E")}</div>
          <div>${a(e.network.operateStatus)}</div>
        </div>
        <div class="content-actions">
          <button class="btn" id="btn-network-refresh">\u5237\u65B0\u72B6\u6001</button>
          <button class="btn" id="btn-network-direct" ${e.network.isOperating?"disabled":""}>\u5207\u6362\u76F4\u8FDE</button>
          <button class="btn" id="btn-network-tun" ${e.network.isOperating?"disabled":""}>\u5207\u6362TUN</button>
          <button class="btn" id="btn-network-logs">\u5237\u65B0\u7F51\u7EDC\u65E5\u5FD7</button>
        </div>
        <div class="status">${a(e.network.logStatus)}</div>
        <pre class="log-viewer-output">${a(e.network.logContent||"")}</pre>
      </section>
    `}function ee(e){const n=(e.probeManage.nodes||[]).map(o=>`<tr>
        <td>${a(o.node_no)}</td>
        <td>${a(o.node_name)}</td>
        <td>${o.online?"\u5728\u7EBF":"\u79BB\u7EBF"}</td>
        <td>${a(o.version||"")}</td>
        <td>${a(o.last_seen||"")}</td>
      </tr>`).join("");return`
      <section class="content-block">
        <h2>\u63A2\u9488\u7BA1\u7406</h2>
        <div class="content-actions">
          <button class="btn" id="btn-probe-refresh">\u5237\u65B0\u8282\u70B9</button>
        </div>
        <div class="status">${a(e.probeManage.status)}</div>
        <table class="probe-table" style="min-width:unset; margin-top:10px;">
          <thead><tr><th>No</th><th>Name</th><th>\u5728\u7EBF</th><th>\u7248\u672C</th><th>\u6700\u540E\u4E0A\u62A5</th></tr></thead>
          <tbody>${n||'<tr><td colspan="5">\u6682\u65E0\u8282\u70B9</td></tr>'}</tbody>
        </table>
        <div class="status">${a(e.probeManage.nodeLogsStatus||"")}</div>
        <pre class="log-viewer-output">${a(e.probeManage.nodeLogs||"")}</pre>
      </section>
    `}function te(e){const n=(e.cloudflare.records||[]).map(o=>{const r=(o==null?void 0:o.hostname)||(o==null?void 0:o.name)||"",u=(o==null?void 0:o.value)||(o==null?void 0:o.content)||"";return`<tr><td>${a(r)}</td><td>${a(u)}</td></tr>`}).join("");return`
      <section class="content-block">
        <h2>Cloudflare \u52A9\u624B</h2>
        <div class="row">
          <label for="cf-api-key">API Key</label>
          <input id="cf-api-key" class="input" value="${a(e.cloudflare.apiKey||"")}" />
        </div>
        <div class="row">
          <label for="cf-zone">Zone</label>
          <input id="cf-zone" class="input" value="${a(e.cloudflare.zoneName||"")}" />
        </div>
        <div class="content-actions">
          <button class="btn" id="btn-cf-refresh">\u5237\u65B0\u914D\u7F6E</button>
          <button class="btn" id="btn-cf-save-key">\u4FDD\u5B58 API Key</button>
          <button class="btn" id="btn-cf-save-zone">\u4FDD\u5B58 Zone</button>
        </div>
        <div class="status">${a(e.cloudflare.status||"")}</div>
        <table class="probe-table" style="min-width:unset; margin-top:10px;">
          <thead><tr><th>\u8BB0\u5F55</th><th>\u503C</th></tr></thead>
          <tbody>${n||'<tr><td colspan="2">\u6682\u65E0\u8BB0\u5F55</td></tr>'}</tbody>
        </table>
      </section>
    `}function ne(e){const n=(e.tg.accounts||[]).map(r=>`<tr><td>${a((r==null?void 0:r.id)||"")}</td><td>${a((r==null?void 0:r.label)||"")}</td><td>${r!=null&&r.authorized?"\u662F":"\u5426"}</td></tr>`).join(""),o=(e.tg.schedules||[]).map(r=>`<tr><td>${a((r==null?void 0:r.id)||"")}</td><td>${a((r==null?void 0:r.target)||"")}</td><td>${r!=null&&r.enabled?"\u542F\u7528":"\u505C\u7528"}</td></tr>`).join("");return`
      <section class="content-block">
        <h2>TG \u52A9\u624B</h2>
        <div class="content-actions">
          <button class="btn" id="btn-tg-refresh">\u5237\u65B0 TG \u6570\u636E</button>
        </div>
        <div class="status">${a(e.tg.status||"")}</div>
        <h3 style="margin-top:12px;">\u8D26\u53F7</h3>
        <table class="probe-table" style="min-width:unset;">
          <thead><tr><th>ID</th><th>Label</th><th>\u5DF2\u6388\u6743</th></tr></thead>
          <tbody>${n||'<tr><td colspan="3">\u6682\u65E0\u8D26\u53F7</td></tr>'}</tbody>
        </table>
        <h3 style="margin-top:12px;">\u8BA1\u5212\u4EFB\u52A1</h3>
        <table class="probe-table" style="min-width:unset;">
          <thead><tr><th>ID</th><th>Target</th><th>\u72B6\u6001</th></tr></thead>
          <tbody>${o||'<tr><td colspan="3">\u6682\u65E0\u4EFB\u52A1</td></tr>'}</tbody>
        </table>
      </section>
    `}function se(e){const n=e.ui.activeTab;return`
      <section class="content-block">
        <h2>${a(n)}</h2>
        <p>\u8BE5\u6A21\u5757\u5C06\u5728\u540E\u7EED\u8FC1\u79FB\u6279\u6B21\u843D\u5730\uFF0C\u5F53\u524D\u5165\u53E3\u4E0E\u8DEF\u7531\u5DF2\u7A33\u5B9A\u3002</p>
      </section>
    `}function oe(e){switch(e.ui.activeTab){case"overview":return J(e);case"probe-manage":return ee(e);case"network-assistant":return Y(e);case"cloudflare-assistant":return te(e);case"tg-assistant":return ne(e);case"log-viewer":return X(e);case"system-settings":return Q(e);default:return se(e)}}function re(e){const o=G(e.auth.userRole,e.auth.certType).map(u=>`<button class="tab-btn ${u.key===e.ui.activeTab?"active":""}" data-tab="${a(u.key)}">${a(u.label)}</button>`).join(""),r=oe(e);return`
      <div id="App">
        <div class="app-shell">
          <aside class="sidebar">
            <div class="sidebar-title">CloudHelper Manager</div>
            <div class="sidebar-identity">${a(e.auth.username)}</div>
            <div class="tab-list">${o}</div>
            <div class="sidebar-actions">
              <button class="btn" id="btn-logout">\u9000\u51FA\u767B\u5F55</button>
            </div>
          </aside>
          <main class="content">
            ${r}
            <div class="status" style="margin-top:12px;">${a(e.ui.statusMessage)}</div>
            <div class="status" style="margin-top:8px;">${a(e.ui.errorMessage)}</div>
          </main>
        </div>
      </div>
    `}function ae(){s.querySelectorAll("[data-tab]").forEach(n=>{n.addEventListener("click",()=>{const o=n.getAttribute("data-tab")||"overview";t.update("ui",{activeTab:o}),i.emit(S.TAB_CHANGED,{activeTab:o})})});const e=s.querySelector("#btn-logout");e&&e.addEventListener("click",()=>void _())}function ie(){const e=s.querySelector("#btn-refresh-overview");e&&e.addEventListener("click",()=>void w())}function le(){const e=s.querySelector("#btn-refresh-versions");e&&e.addEventListener("click",()=>void $());const n=s.querySelector("#btn-save-base-url");n&&n.addEventListener("click",()=>{const u=s.querySelector("#settings-base-url");!u||x(u.value)});const o=s.querySelector("#btn-save-controller-ip");o&&o.addEventListener("click",()=>{const u=s.querySelector("#settings-controller-ip");!u||z(u.value)});const r=s.querySelector("#btn-refresh-settings");r&&r.addEventListener("click",C)}function ce(){const e=s.querySelector("#log-source");e&&e.addEventListener("change",()=>{t.update("logViewer",{source:e.value})});const n=s.querySelector("#log-lines");n&&n.addEventListener("change",()=>{const u=Number(n.value),l=Number.isFinite(u)?Math.max(1,Math.min(2e3,Math.trunc(u))):200;t.update("logViewer",{lines:l})});const o=s.querySelector("#btn-refresh-logs");o&&o.addEventListener("click",()=>{i.emit(S.LOG_VIEWER_REFRESH_REQUESTED,{}),H()});const r=s.querySelector("#btn-copy-logs");r&&r.addEventListener("click",()=>void F())}function ue(){const e=s.querySelector("#btn-network-refresh");e&&e.addEventListener("click",()=>{i.emit(S.NETWORK_STATUS_REFRESH_REQUESTED,{}),T()});const n=s.querySelector("#btn-network-direct");n&&n.addEventListener("click",()=>void P("direct"));const o=s.querySelector("#btn-network-tun");o&&o.addEventListener("click",()=>void P("tun"));const r=s.querySelector("#btn-network-logs");r&&r.addEventListener("click",()=>void R())}function de(){const e=s.querySelector("#btn-probe-refresh");e&&e.addEventListener("click",()=>void M())}function ge(){const e=s.querySelector("#btn-cf-refresh");e&&e.addEventListener("click",()=>void k());const n=s.querySelector("#btn-cf-save-key");n&&n.addEventListener("click",()=>{const r=s.querySelector("#cf-api-key");!r||j(r.value)});const o=s.querySelector("#btn-cf-save-zone");o&&o.addEventListener("click",()=>{const r=s.querySelector("#cf-zone");!r||W(r.value)})}function fe(){const e=s.querySelector("#btn-tg-refresh");e&&e.addEventListener("click",()=>void U())}function ve(){const e=t.getState();if(!e.auth.sessionToken){const n=s.querySelector("#login-form");n&&n.addEventListener("submit",y);return}switch(ae(),e.ui.activeTab){case"overview":ie();break;case"probe-manage":de();break;case"network-assistant":ue();break;case"cloudflare-assistant":ge();break;case"tg-assistant":fe();break;case"log-viewer":ce();break;case"system-settings":le();break}}function m(){const e=t.getState();s.innerHTML=e.auth.sessionToken?re(e):Z(e),ve()}function I(){t.update("auth",{sessionToken:"",loginTone:"error",loginStatus:"Session expired, please login again"}),h("\u4F1A\u8BDD\u5DF2\u8FC7\u671F\uFF0C\u8BF7\u91CD\u65B0\u767B\u5F55"),m()}function pe(){f||(f=!0,d=t.subscribe(()=>m()),window.addEventListener("unauthorized",I),C(),m(),t.getState().auth.sessionToken&&Promise.allSettled([w(),$(),T(),M(),k(),U()]))}function be(){!f||(f=!1,d&&d(),i.clear(),window.removeEventListener("unauthorized",I),s.innerHTML="")}return{mount:pe,unmount:be,store:t,bus:i}}const D=document.getElementById("root");if(!D)throw new Error("missing #root container");const ze=xe(D);ze.mount();
