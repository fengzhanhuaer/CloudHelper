/**
 * manager-api.ts — manager_service REST API 契约客户端
 *
 * 本模块是前端唯一允许的 API 调用入口。
 * 所有函数仅调用 manager_service 后端暴露的端点 (127.0.0.1:16033)。
 * 禁止在此模块中引入任何直连主控/WS-RPC 逻辑。
 *
 * PKG-FE-R02 / RQ-003 / FC-FE-03 / FC-FE-05
 */
import { fetchJson } from "./api";
/** POST /api/auth/login */
export async function apiLogin(username, password) {
    return fetchJson("/auth/login", {
        method: "POST",
        body: JSON.stringify({ username, password }),
    });
}
/** POST /api/auth/logout */
export async function apiLogout() {
    await fetchJson("/auth/logout", { method: "POST" }).catch(() => { });
}
/** POST /api/auth/password/change */
export async function apiChangePassword(oldPassword, newUsername, newPassword) {
    return fetchJson("/auth/password/change", {
        method: "POST",
        body: JSON.stringify({ old_password: oldPassword, new_username: newUsername, new_password: newPassword }),
    });
}
/** POST /api/auth/password/reset-local (localhost-only) */
export async function apiResetLocal() {
    return fetchJson("/auth/password/reset-local", { method: "POST" });
}
/** GET /api/system/version */
export async function apiGetVersion() {
    return fetchJson("/system/version");
}
/** GET /api/probe/nodes */
export async function apiListNodes() {
    return fetchJson("/probe/nodes");
}
/** POST /api/probe/nodes */
export async function apiCreateNode(nodeName) {
    return fetchJson("/probe/nodes", {
        method: "POST",
        body: JSON.stringify({ node_name: nodeName }),
    });
}
/** PUT /api/probe/nodes/:node_no */
export async function apiUpdateNode(nodeNo, settings) {
    return fetchJson(`/probe/nodes/${nodeNo}`, {
        method: "PUT",
        body: JSON.stringify(settings),
    });
}
/** DELETE /api/probe/nodes/:node_no */
export async function apiDeleteNode(nodeNo) {
    await fetchJson(`/probe/nodes/${nodeNo}`, { method: "DELETE" });
}
/** POST /api/probe/nodes/:node_no/restore */
export async function apiRestoreNode(nodeNo) {
    return fetchJson(`/probe/nodes/${nodeNo}/restore`, { method: "POST" });
}
/** POST /api/probe/nodes/:node_no/upgrade */
export async function apiUpgradeNode(nodeNo) {
    return fetchJson(`/probe/nodes/${nodeNo}/upgrade`, { method: "POST" });
}
/** POST /api/probe/nodes/upgrade-all */
export async function apiUpgradeAllNodes() {
    return fetchJson("/probe/nodes/upgrade-all", { method: "POST" });
}
/** GET /api/probe/nodes/status */
export async function apiGetNodesStatus(nodeNo) {
    const qs = nodeNo !== undefined ? `?node_no=${nodeNo}` : "";
    return fetchJson(`/probe/nodes/status${qs}`);
}
/** GET /api/probe/nodes/:node_no/logs */
export async function apiGetNodeLogs(nodeNo, opts) {
    const p = new URLSearchParams();
    if (opts.lines)
        p.set("lines", String(opts.lines));
    if (opts.sinceMinutes)
        p.set("since_minutes", String(opts.sinceMinutes));
    if (opts.minLevel)
        p.set("min_level", opts.minLevel);
    return fetchJson(`/probe/nodes/${nodeNo}/logs?${p.toString()}`);
}
/** GET /api/probe/nodes/report-interval */
export async function apiGetReportInterval() {
    return fetchJson("/probe/nodes/report-interval");
}
/** POST /api/probe/nodes/report-interval */
export async function apiSetReportInterval(intervalSec) {
    return fetchJson("/probe/nodes/report-interval", {
        method: "POST",
        body: JSON.stringify({ interval_sec: intervalSec }),
    });
}
/** POST /api/probe/nodes/:node_no/shell/start */
export async function apiStartShell(nodeNo) {
    return fetchJson(`/probe/nodes/${nodeNo}/shell/start`, { method: "POST" });
}
/** POST /api/probe/nodes/:node_no/shell/exec */
export async function apiExecShell(nodeNo, sessionId, command, timeoutSec) {
    return fetchJson(`/probe/nodes/${nodeNo}/shell/exec`, {
        method: "POST",
        body: JSON.stringify({ session_id: sessionId, command, timeout_sec: timeoutSec }),
    });
}
/** POST /api/probe/nodes/:node_no/shell/stop */
export async function apiStopShell(nodeNo, sessionId) {
    return fetchJson(`/probe/nodes/${nodeNo}/shell/stop`, {
        method: "POST",
        body: JSON.stringify({ session_id: sessionId }),
    });
}
/** GET /api/probe/nodes/shell/shortcuts */
export async function apiGetShellShortcuts() {
    return fetchJson("/probe/nodes/shell/shortcuts");
}
/** POST /api/probe/nodes/shell/shortcuts */
export async function apiSaveShellShortcut(name, command) {
    return fetchJson("/probe/nodes/shell/shortcuts", { method: "POST", body: JSON.stringify({ name, command }) });
}
/** DELETE /api/probe/nodes/shell/shortcuts/:name */
export async function apiDeleteShellShortcut(name) {
    return fetchJson(`/probe/nodes/shell/shortcuts/${encodeURIComponent(name)}`, { method: "DELETE" });
}
/** POST /api/probe/link/test */
export async function apiTestLink(nodeId, endpointType, scheme, host, port) {
    return fetchJson("/probe/link/test", {
        method: "POST",
        body: JSON.stringify({ node_id: nodeId, endpoint_type: endpointType, scheme, host, port }),
    });
}
/** GET /api/link/chains */
export async function apiGetLinkChains() {
    return fetchJson("/link/chains");
}
/** POST /api/link/chains */
export async function apiUpsertLinkChain(payload) {
    return fetchJson("/link/chains", { method: "POST", body: JSON.stringify(payload) });
}
/** DELETE /api/link/chains/:chain_id */
export async function apiDeleteLinkChain(chainId) {
    return fetchJson(`/link/chains/${encodeURIComponent(chainId)}`, { method: "DELETE" });
}
/** GET /api/link/users */
export async function apiGetLinkUsers() {
    return fetchJson("/link/users");
}
/** GET /api/link/users/:username/pubkey */
export async function apiGetLinkUserPubKey(username) {
    return fetchJson(`/link/users/${encodeURIComponent(username)}/pubkey`);
}
/** POST /api/link/nodes/update */
export async function apiUpdateNodeLink(payload) {
    return fetchJson("/link/nodes/update", { method: "POST", body: JSON.stringify(payload) });
}
/** POST /api/link/test/start */
export async function apiStartLinkTest(payload) {
    return fetchJson("/link/test/start", { method: "POST", body: JSON.stringify(payload) });
}
/** POST /api/link/test/stop */
export async function apiStopLinkTest(payload) {
    return fetchJson("/link/test/stop", { method: "POST", body: JSON.stringify(payload) });
}
/** POST /api/link/dns/refresh */
export async function apiRefreshProbeDNS(payload) {
    return fetchJson("/link/dns/refresh", { method: "POST", body: payload ? JSON.stringify(payload) : undefined });
}
/**
 * GET /api/network-assistant/status
 */
export async function apiGetNetworkAssistantStatus() {
    return fetchJson("/network-assistant/status");
}
/**
 * POST /api/network-assistant/mode
 */
export async function apiSwitchNetworkAssistantMode(mode) {
    return fetchJson("/network-assistant/mode", {
        method: "POST",
        body: JSON.stringify({ mode }),
    });
}
/**
 * GET /api/network-assistant/logs?lines=N
 */
export async function apiGetNetworkAssistantLogs(lines = 200) {
    return fetchJson(`/network-assistant/logs?lines=${lines}`);
}
/**
 * GET /api/network-assistant/dns/cache?query=Q
 */
export async function apiGetNetworkAssistantDNSCache(query = "") {
    const qs = query ? `?query=${encodeURIComponent(query)}` : "";
    return fetchJson(`/network-assistant/dns/cache${qs}`);
}
/**
 * GET /api/network-assistant/processes
 */
export async function apiGetNetworkAssistantProcesses() {
    return fetchJson("/network-assistant/processes");
}
/**
 * POST /api/network-assistant/monitor/start
 */
export async function apiStartNetworkMonitor() {
    return fetchJson("/network-assistant/monitor/start", { method: "POST" });
}
/**
 * POST /api/network-assistant/monitor/stop
 */
export async function apiStopNetworkMonitor() {
    return fetchJson("/network-assistant/monitor/stop", { method: "POST" });
}
/**
 * POST /api/network-assistant/monitor/clear
 */
export async function apiClearNetworkMonitorEvents() {
    return fetchJson("/network-assistant/monitor/clear", { method: "POST" });
}
/**
 * GET /api/network-assistant/monitor/events?since=N
 */
export async function apiGetNetworkMonitorEvents(since = 0) {
    return fetchJson(`/network-assistant/monitor/events?since=${since}`);
}
/**
 * POST /api/network-assistant/tun/install
 */
export async function apiInstallTUN() {
    return fetchJson("/network-assistant/tun/install", { method: "POST" });
}
/**
 * POST /api/network-assistant/tun/enable
 */
export async function apiEnableTUN() {
    return fetchJson("/network-assistant/tun/enable", { method: "POST" });
}
/**
 * POST /api/network-assistant/direct/restore
 */
export async function apiRestoreDirect() {
    return fetchJson("/network-assistant/direct/restore", { method: "POST" });
}
/**
 * GET /api/network-assistant/rules
 */
export async function apiGetNetworkRuleConfig() {
    return fetchJson("/network-assistant/rules");
}
/**
 * POST /api/network-assistant/rules/policy
 */
export async function apiSetNetworkRulePolicy(group, action, tunnelNodeID = "") {
    return fetchJson("/network-assistant/rules/policy", {
        method: "POST",
        body: JSON.stringify({ group, action, tunnelNodeID }),
    });
}
/**
 * GET /api/upgrade/release?project=...
 * 查询最新 GitHub Release 信息。
 */
export async function apiGetRelease(project) {
    return fetchJson(`/upgrade/release?project=${encodeURIComponent(project)}`);
}
/**
 * POST /api/upgrade/manager
 * 管理端升级（当前 Web 模式返回 supported=false 及指引）。
 */
export async function apiUpgradeManager() {
    return fetchJson("/upgrade/manager", { method: "POST" });
}
/**
 * GET /api/logs/manager?lines=&since_minutes=&min_level=
 */
export async function apiGetLogs(opts) {
    const params = new URLSearchParams();
    if (opts.lines)
        params.set("lines", String(opts.lines));
    if (opts.sinceMinutes)
        params.set("since_minutes", String(opts.sinceMinutes));
    if (opts.minLevel)
        params.set("min_level", opts.minLevel);
    return fetchJson(`/logs/manager?${params.toString()}`);
}
/**
 * POST /api/controller/session/login
 * 获取 manager_service 缓存的 controller 会话 token。
 */
export async function apiGetControllerSession() {
    return fetchJson("/controller/session/login", { method: "POST" });
}
/** GET /api/cloudflare/api-key */
export async function apiGetCloudflareAPIKey() {
    return fetchJson("/cloudflare/api-key");
}
/** POST /api/cloudflare/api-key */
export async function apiSetCloudflareAPIKey(apiKey) {
    return fetchJson("/cloudflare/api-key", { method: "POST", body: JSON.stringify({ api_key: apiKey }) });
}
/** GET /api/cloudflare/zone */
export async function apiGetCloudflareZone() {
    return fetchJson("/cloudflare/zone");
}
/** POST /api/cloudflare/zone */
export async function apiSetCloudflareZone(zoneName) {
    return fetchJson("/cloudflare/zone", { method: "POST", body: JSON.stringify({ zone_name: zoneName }) });
}
/** GET /api/cloudflare/ddns/records */
export async function apiGetCloudflareDDNSRecords() {
    return fetchJson("/cloudflare/ddns/records");
}
/** POST /api/cloudflare/ddns/apply */
export async function apiApplyCloudflareDDNS(zoneName) {
    return fetchJson("/cloudflare/ddns/apply", { method: "POST", body: JSON.stringify({ zone_name: zoneName }) });
}
/** GET /api/cloudflare/zerotrust/whitelist */
export async function apiGetZeroTrustWhitelist() {
    return fetchJson("/cloudflare/zerotrust/whitelist");
}
/** POST /api/cloudflare/zerotrust/whitelist */
export async function apiSetZeroTrustWhitelist(payload) {
    return fetchJson("/cloudflare/zerotrust/whitelist", { method: "POST", body: JSON.stringify(payload) });
}
/** POST /api/cloudflare/zerotrust/sync */
export async function apiRunZeroTrustSync(force) {
    return fetchJson("/cloudflare/zerotrust/sync", { method: "POST", body: JSON.stringify({ force: force ?? true }) });
}
/** GET /api/tg/api-key */
export async function apiGetTGAPIKey() { return fetchJson("/tg/api-key"); }
/** POST /api/tg/api-key */
export async function apiSetTGAPIKey(payload) {
    return fetchJson("/tg/api-key", { method: "POST", body: JSON.stringify(payload) });
}
/** GET /api/tg/accounts */
export async function apiListTGAccounts() { return fetchJson("/tg/accounts"); }
/** POST /api/tg/accounts/refresh */
export async function apiRefreshTGAccounts() {
    return fetchJson("/tg/accounts/refresh", { method: "POST" });
}
/** POST /api/tg/accounts/add */
export async function apiAddTGAccount(payload) {
    return fetchJson("/tg/accounts/add", { method: "POST", body: JSON.stringify(payload) });
}
/** POST /api/tg/accounts/send-code */
export async function apiTGSendCode(payload) {
    return fetchJson("/tg/accounts/send-code", { method: "POST", body: JSON.stringify(payload) });
}
/** POST /api/tg/accounts/sign-in */
export async function apiTGSignIn(payload) {
    return fetchJson("/tg/accounts/sign-in", { method: "POST", body: JSON.stringify(payload) });
}
/** POST /api/tg/accounts/logout */
export async function apiLogoutTGAccount(payload) {
    return fetchJson("/tg/accounts/logout", { method: "POST", body: JSON.stringify(payload) });
}
/** POST /api/tg/accounts/remove */
export async function apiRemoveTGAccount(payload) {
    return fetchJson("/tg/accounts/remove", { method: "POST", body: JSON.stringify(payload) });
}
/** GET /api/tg/schedules */
export async function apiListTGSchedules() { return fetchJson("/tg/schedules"); }
/** POST /api/tg/schedules */
export async function apiAddTGSchedule(payload) {
    return fetchJson("/tg/schedules", { method: "POST", body: JSON.stringify(payload) });
}
/** PUT /api/tg/schedules/:id */
export async function apiUpdateTGSchedule(id, payload) {
    return fetchJson(`/tg/schedules/${encodeURIComponent(id)}`, { method: "PUT", body: JSON.stringify(payload) });
}
/** DELETE /api/tg/schedules/:id */
export async function apiRemoveTGSchedule(id) {
    return fetchJson(`/tg/schedules/${encodeURIComponent(id)}`, { method: "DELETE" });
}
/** POST /api/tg/schedules/:id/enable */
export async function apiEnableTGSchedule(id) {
    return fetchJson(`/tg/schedules/${encodeURIComponent(id)}/enable`, { method: "POST" });
}
/** POST /api/tg/schedules/:id/disable */
export async function apiDisableTGSchedule(id) {
    return fetchJson(`/tg/schedules/${encodeURIComponent(id)}/disable`, { method: "POST" });
}
/** POST /api/tg/schedules/:id/send-now */
export async function apiTGSendNow(id) {
    return fetchJson(`/tg/schedules/${encodeURIComponent(id)}/send-now`, { method: "POST" });
}
/** GET /api/tg/schedules/pending */
export async function apiGetTGPendingTasks() { return fetchJson("/tg/schedules/pending"); }
/** GET /api/tg/schedules/history */
export async function apiGetTGHistory(payload) {
    return fetchJson("/tg/schedules/history", payload ? { method: "GET" } : { method: "GET" });
}
/** GET /api/tg/targets */
export async function apiListTGTargets() { return fetchJson("/tg/targets"); }
/** POST /api/tg/targets/refresh */
export async function apiRefreshTGTargets() {
    return fetchJson("/tg/targets/refresh", { method: "POST" });
}
/** GET /api/tg/bot */
export async function apiGetTGBot() { return fetchJson("/tg/bot"); }
/** POST /api/tg/bot */
export async function apiSetTGBot(payload) {
    return fetchJson("/tg/bot", { method: "POST", body: JSON.stringify(payload) });
}
/** POST /api/tg/bot/test-send */
export async function apiTGBotTestSend(payload) {
    return fetchJson("/tg/bot/test-send", { method: "POST", body: JSON.stringify(payload) });
}
/** GET /api/system/backup-settings */
export async function apiGetBackupSettings() {
    return fetchJson("/system/backup-settings");
}
/** POST /api/system/backup-settings */
export async function apiSetBackupSettings(enabled, rcloneRemote) {
    return fetchJson("/system/backup-settings", { method: "POST", body: JSON.stringify({ enabled, rclone_remote: rcloneRemote }) });
}
/** POST /api/system/backup-settings/test */
export async function apiTestBackupSettings(rcloneRemote) {
    return fetchJson("/system/backup-settings/test", { method: "POST", body: JSON.stringify({ rclone_remote: rcloneRemote }) });
}
/** GET /api/system/controller-logs?lines=200&since_minutes=0&min_level=normal */
export async function apiGetControllerLogs(params) {
    const qs = new URLSearchParams();
    if (params?.lines)
        qs.set("lines", String(params.lines));
    if (params?.sinceMinutes)
        qs.set("since_minutes", String(params.sinceMinutes));
    if (params?.minLevel)
        qs.set("min_level", params.minLevel);
    const q = qs.toString();
    return fetchJson(`/system/controller-logs${q ? `?${q}` : ""}`);
}
/** GET /api/system/controller-version */
export async function apiGetControllerVersion() {
    return fetchJson("/system/controller-version");
}
/** POST /api/system/controller-upgrade */
export async function apiUpgradeController() {
    return fetchJson("/system/controller-upgrade", { method: "POST" });
}
/** GET /api/system/controller-upgrade-progress */
export async function apiGetControllerUpgradeProgress() {
    return fetchJson("/system/controller-upgrade-progress");
}
/** POST /api/system/rule-routes/upload */
export async function apiUploadRuleRoutes(content) {
    return fetchJson("/system/rule-routes/upload", { method: "POST", body: JSON.stringify({ file_name: "rule_routes.txt", content }) });
}
/** POST /api/system/rule-routes/download */
export async function apiDownloadRuleRoutes() {
    return fetchJson("/system/rule-routes/download", { method: "POST", body: JSON.stringify({ file_name: "rule_routes.txt" }) });
}
