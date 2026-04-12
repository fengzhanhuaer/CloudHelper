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

// ─── 认证域 ───────────────────────────────────────────────────────────────────

export type LoginResponse = {
  token: string;
  username: string;
};

/** POST /api/auth/login */
export async function apiLogin(username: string, password: string): Promise<LoginResponse> {
  return fetchJson<LoginResponse>("/auth/login", {
    method: "POST",
    body: JSON.stringify({ username, password }),
  });
}

/** POST /api/auth/logout */
export async function apiLogout(): Promise<void> {
  await fetchJson<unknown>("/auth/logout", { method: "POST" }).catch(() => {});
}

/** POST /api/auth/password/change */
export async function apiChangePassword(
  oldPassword: string,
  newUsername: string,
  newPassword: string
): Promise<{ message: string }> {
  return fetchJson<{ message: string }>("/auth/password/change", {
    method: "POST",
    body: JSON.stringify({ old_password: oldPassword, new_username: newUsername, new_password: newPassword }),
  });
}

/** POST /api/auth/password/reset-local (localhost-only) */
export async function apiResetLocal(): Promise<{ message: string }> {
  return fetchJson<{ message: string }>("/auth/password/reset-local", { method: "POST" });
}

// ─── 系统域 ───────────────────────────────────────────────────────────────────

export type SystemVersion = {
  version: string;
};

/** GET /api/system/version */
export async function apiGetVersion(): Promise<SystemVersion> {
  return fetchJson<SystemVersion>("/system/version");
}

// ─── 节点域 ───────────────────────────────────────────────────────────────────

export type ProbeNode = {
  node_no: number;
  node_name: string;
  remark?: string;
  target_system?: string;
  direct_connect?: boolean;
  payment_cycle?: string;
  cost?: string;
  expire_at?: string;
  vendor_name?: string;
  vendor_url?: string;
};

/** GET /api/probe/nodes */
export async function apiListNodes(): Promise<ProbeNode[]> {
  return fetchJson<ProbeNode[]>("/probe/nodes");
}

/** POST /api/probe/nodes */
export async function apiCreateNode(nodeName: string): Promise<ProbeNode> {
  return fetchJson<ProbeNode>("/probe/nodes", {
    method: "POST",
    body: JSON.stringify({ node_name: nodeName }),
  });
}

/** PUT /api/probe/nodes/:node_no */
export async function apiUpdateNode(nodeNo: number, settings: Partial<Omit<ProbeNode, "node_no">>): Promise<ProbeNode> {
  return fetchJson<ProbeNode>(`/probe/nodes/${nodeNo}`, {
    method: "PUT",
    body: JSON.stringify(settings),
  });
}

/** DELETE /api/probe/nodes/:node_no */
export async function apiDeleteNode(nodeNo: number): Promise<void> {
  await fetchJson<unknown>(`/probe/nodes/${nodeNo}`, { method: "DELETE" });
}

/** POST /api/probe/nodes/:node_no/restore */
export async function apiRestoreNode(nodeNo: number): Promise<ProbeNode> {
  return fetchJson<ProbeNode>(`/probe/nodes/${nodeNo}/restore`, { method: "POST" });
}

/** POST /api/probe/nodes/:node_no/upgrade */
export async function apiUpgradeNode(nodeNo: number): Promise<{ ok: boolean; message: string; node_id?: string; node_no?: number; node_name?: string; mode?: string; timestamp?: string }> {
  return fetchJson(`/probe/nodes/${nodeNo}/upgrade`, { method: "POST" });
}

/** POST /api/probe/nodes/upgrade-all */
export async function apiUpgradeAllNodes(): Promise<{ ok: boolean; message: string; node_id?: string; node_no?: number; node_name?: string; mode?: string }[]> {
  return fetchJson("/probe/nodes/upgrade-all", { method: "POST" });
}

export type ProbeNodeStatus = {
  node_no: number;
  node_name?: string;
  node_id?: string;
  online?: boolean;
  last_seen?: string;
  version?: string;
  ipv4?: string[];
  system?: Record<string, unknown>;
  runtime?: {
    node_id?: string;
    online?: boolean;
    last_seen?: string;
    version?: string;
    ipv4?: string[];
    ipv6?: string[];
    ip_locations?: Record<string, string>;
    system?: Record<string, unknown>;
  };
};

/** GET /api/probe/nodes/status */
export async function apiGetNodesStatus(nodeNo?: number): Promise<ProbeNodeStatus[]> {
  const qs = nodeNo !== undefined ? `?node_no=${nodeNo}` : "";
  return fetchJson<ProbeNodeStatus[]>(`/probe/nodes/status${qs}`);
}

export type ProbeNodeLogsResponse = {
  content: string;
  file_path?: string;
  source?: string;
  fetched_at?: string;
  lines?: number;
  node_name?: string;
};

/** GET /api/probe/nodes/:node_no/logs */
export async function apiGetNodeLogs(nodeNo: number, opts: { lines?: number; sinceMinutes?: number; minLevel?: string }): Promise<ProbeNodeLogsResponse> {
  const p = new URLSearchParams();
  if (opts.lines) p.set("lines", String(opts.lines));
  if (opts.sinceMinutes) p.set("since_minutes", String(opts.sinceMinutes));
  if (opts.minLevel) p.set("min_level", opts.minLevel);
  return fetchJson<ProbeNodeLogsResponse>(`/probe/nodes/${nodeNo}/logs?${p.toString()}`);
}

export type ProbeReportInterval = { current_sec: number; default_sec: number; override_expires_at?: string };

/** GET /api/probe/nodes/report-interval */
export async function apiGetReportInterval(): Promise<ProbeReportInterval> {
  return fetchJson<ProbeReportInterval>("/probe/nodes/report-interval");
}

/** POST /api/probe/nodes/report-interval */
export async function apiSetReportInterval(intervalSec: number): Promise<ProbeReportInterval> {
  return fetchJson<ProbeReportInterval>("/probe/nodes/report-interval", {
    method: "POST",
    body: JSON.stringify({ interval_sec: intervalSec }),
  });
}

export type ProbeShellResp = { session_id: string; output: string; stdout?: string; stderr?: string; exit_code?: number; ok?: boolean; error?: string };

/** POST /api/probe/nodes/:node_no/shell/start */
export async function apiStartShell(nodeNo: number): Promise<ProbeShellResp> {
  return fetchJson<ProbeShellResp>(`/probe/nodes/${nodeNo}/shell/start`, { method: "POST" });
}

/** POST /api/probe/nodes/:node_no/shell/exec */
export async function apiExecShell(nodeNo: number, sessionId: string, command: string, timeoutSec: number): Promise<ProbeShellResp> {
  return fetchJson<ProbeShellResp>(`/probe/nodes/${nodeNo}/shell/exec`, {
    method: "POST",
    body: JSON.stringify({ session_id: sessionId, command, timeout_sec: timeoutSec }),
  });
}

/** POST /api/probe/nodes/:node_no/shell/stop */
export async function apiStopShell(nodeNo: number, sessionId: string): Promise<ProbeShellResp> {
  return fetchJson<ProbeShellResp>(`/probe/nodes/${nodeNo}/shell/stop`, {
    method: "POST",
    body: JSON.stringify({ session_id: sessionId }),
  });
}

/** GET /api/probe/nodes/shell/shortcuts */
export async function apiGetShellShortcuts(): Promise<{ name: string; command: string; updated_at?: string }[]> {
  return fetchJson("/probe/nodes/shell/shortcuts");
}

/** POST /api/probe/nodes/shell/shortcuts */
export async function apiSaveShellShortcut(name: string, command: string): Promise<{ name: string; command: string; updated_at?: string }[]> {
  return fetchJson("/probe/nodes/shell/shortcuts", { method: "POST", body: JSON.stringify({ name, command }) });
}

/** DELETE /api/probe/nodes/shell/shortcuts/:name */
export async function apiDeleteShellShortcut(name: string): Promise<{ name: string; command: string; updated_at?: string }[]> {
  return fetchJson(`/probe/nodes/shell/shortcuts/${encodeURIComponent(name)}`, { method: "DELETE" });
}


// ─── 链路探测域 ───────────────────────────────────────────────────────────────

export type LinkTestResult = {
  ok: boolean;
  duration_ms: number;
  message?: string;
};

/** POST /api/probe/link/test */
export async function apiTestLink(
  nodeId: string,
  endpointType: string,
  scheme: string,
  host: string,
  port: number
): Promise<LinkTestResult> {
  return fetchJson<LinkTestResult>("/probe/link/test", {
    method: "POST",
    body: JSON.stringify({ node_id: nodeId, endpoint_type: endpointType, scheme, host, port }),
  });
}

// ─── R4-BE: 链路管理域代理 ────────────────────────────────────────────────────

export type ProbeLinkChain = Record<string, unknown>;
export type ProbeLinkUser = { username: string; user_role?: string; cert_type?: string };
export type ProbeLinkUserPubKey = { username: string; user_role?: string; cert_type?: string; public_key: string };
export type ProbeLinkTestResult = { ok: boolean; node_id: string; action: string; protocol?: string; listen_host?: string; internal_port?: number; message?: string; timestamp?: string };

/** GET /api/link/chains */
export async function apiGetLinkChains(): Promise<{ items: ProbeLinkChain[] }> {
  return fetchJson("/link/chains");
}

/** POST /api/link/chains */
export async function apiUpsertLinkChain(payload: unknown): Promise<{ item?: ProbeLinkChain; items: ProbeLinkChain[]; apply_ok?: boolean; apply_error?: string }> {
  return fetchJson("/link/chains", { method: "POST", body: JSON.stringify(payload) });
}

/** DELETE /api/link/chains/:chain_id */
export async function apiDeleteLinkChain(chainId: string): Promise<{ items: ProbeLinkChain[]; apply_ok?: boolean; apply_error?: string }> {
  return fetchJson(`/link/chains/${encodeURIComponent(chainId)}`, { method: "DELETE" });
}

/** GET /api/link/users */
export async function apiGetLinkUsers(): Promise<{ users: ProbeLinkUser[] }> {
  return fetchJson("/link/users");
}

/** GET /api/link/users/:username/pubkey */
export async function apiGetLinkUserPubKey(username: string): Promise<ProbeLinkUserPubKey> {
  return fetchJson(`/link/users/${encodeURIComponent(username)}/pubkey`);
}

/** POST /api/link/nodes/update */
export async function apiUpdateNodeLink(payload: unknown): Promise<{ node?: unknown }> {
  return fetchJson("/link/nodes/update", { method: "POST", body: JSON.stringify(payload) });
}

/** POST /api/link/test/start */
export async function apiStartLinkTest(payload: unknown): Promise<ProbeLinkTestResult> {
  return fetchJson("/link/test/start", { method: "POST", body: JSON.stringify(payload) });
}

/** POST /api/link/test/stop */
export async function apiStopLinkTest(payload: unknown): Promise<ProbeLinkTestResult> {
  return fetchJson("/link/test/stop", { method: "POST", body: JSON.stringify(payload) });
}

/** POST /api/link/dns/refresh */
export async function apiRefreshProbeDNS(payload?: unknown): Promise<unknown> {
  return fetchJson("/link/dns/refresh", { method: "POST", body: payload ? JSON.stringify(payload) : undefined });
}

// ─── 网络助手域 ───────────────────────────────────────────────────────────────

export type NetworkAssistantStatusRaw = Record<string, unknown>;

/**
 * GET /api/network-assistant/status
 */
export async function apiGetNetworkAssistantStatus(): Promise<NetworkAssistantStatusRaw> {
  return fetchJson<NetworkAssistantStatusRaw>("/network-assistant/status");
}

/**
 * POST /api/network-assistant/mode
 */
export async function apiSwitchNetworkAssistantMode(mode: string): Promise<NetworkAssistantStatusRaw> {
  return fetchJson<NetworkAssistantStatusRaw>("/network-assistant/mode", {
    method: "POST",
    body: JSON.stringify({ mode }),
  });
}

/**
 * GET /api/network-assistant/logs?lines=N
 */
export async function apiGetNetworkAssistantLogs(lines = 200): Promise<unknown> {
  return fetchJson<unknown>(`/network-assistant/logs?lines=${lines}`);
}

/**
 * GET /api/network-assistant/dns/cache?query=Q
 */
export async function apiGetNetworkAssistantDNSCache(query = ""): Promise<unknown[]> {
  const qs = query ? `?query=${encodeURIComponent(query)}` : "";
  return fetchJson<unknown[]>(`/network-assistant/dns/cache${qs}`);
}

/**
 * GET /api/network-assistant/processes
 */
export async function apiGetNetworkAssistantProcesses(): Promise<unknown[]> {
  return fetchJson<unknown[]>("/network-assistant/processes");
}

/**
 * POST /api/network-assistant/monitor/start
 */
export async function apiStartNetworkMonitor(): Promise<unknown> {
  return fetchJson<unknown>("/network-assistant/monitor/start", { method: "POST" });
}

/**
 * POST /api/network-assistant/monitor/stop
 */
export async function apiStopNetworkMonitor(): Promise<unknown> {
  return fetchJson<unknown>("/network-assistant/monitor/stop", { method: "POST" });
}

/**
 * POST /api/network-assistant/monitor/clear
 */
export async function apiClearNetworkMonitorEvents(): Promise<unknown> {
  return fetchJson<unknown>("/network-assistant/monitor/clear", { method: "POST" });
}

/**
 * GET /api/network-assistant/monitor/events?since=N
 */
export async function apiGetNetworkMonitorEvents(since = 0): Promise<unknown[]> {
  return fetchJson<unknown[]>(`/network-assistant/monitor/events?since=${since}`);
}

/**
 * POST /api/network-assistant/tun/install
 */
export async function apiInstallTUN(): Promise<NetworkAssistantStatusRaw> {
  return fetchJson<NetworkAssistantStatusRaw>("/network-assistant/tun/install", { method: "POST" });
}

/**
 * POST /api/network-assistant/tun/enable
 */
export async function apiEnableTUN(): Promise<NetworkAssistantStatusRaw> {
  return fetchJson<NetworkAssistantStatusRaw>("/network-assistant/tun/enable", { method: "POST" });
}

/**
 * POST /api/network-assistant/direct/restore
 */
export async function apiRestoreDirect(): Promise<NetworkAssistantStatusRaw> {
  return fetchJson<NetworkAssistantStatusRaw>("/network-assistant/direct/restore", { method: "POST" });
}

/**
 * GET /api/network-assistant/rules
 */
export async function apiGetNetworkRuleConfig(): Promise<unknown> {
  return fetchJson<unknown>("/network-assistant/rules");
}

/**
 * POST /api/network-assistant/rules/policy
 */
export async function apiSetNetworkRulePolicy(group: string, action: string, tunnelNodeID = ""): Promise<unknown> {
  return fetchJson<unknown>("/network-assistant/rules/policy", {
    method: "POST",
    body: JSON.stringify({ group, action, tunnelNodeID }),
  });
}

// ─── 升级域 ───────────────────────────────────────────────────────────────────

export type ReleaseInfo = {
  tag_name: string;
  name: string;
  published_at: string;
  html_url: string;
  assets: { name: string; download_url: string; size: number }[];
};

/**
 * GET /api/upgrade/release?project=...
 * 查询最新 GitHub Release 信息。
 */
export async function apiGetRelease(project: string): Promise<ReleaseInfo> {
  return fetchJson<ReleaseInfo>(`/upgrade/release?project=${encodeURIComponent(project)}`);
}

export type ManagerUpgradeInfo = {
  supported: boolean;
  reason: string;
  docs_url?: string;
};

/**
 * POST /api/upgrade/manager
 * 管理端升级（当前 Web 模式返回 supported=false 及指引）。
 */
export async function apiUpgradeManager(): Promise<ManagerUpgradeInfo> {
  return fetchJson<ManagerUpgradeInfo>("/upgrade/manager", { method: "POST" });
}

// ─── 日志域 ───────────────────────────────────────────────────────────────────

export type LogEntry = {
  time: string;
  level: string;
  message: string;
  line?: string;
};

export type LogsResponse = {
  entries: LogEntry[];
  total: number;
};

/**
 * GET /api/logs/manager?lines=&since_minutes=&min_level=
 */
export async function apiGetLogs(opts: {
  lines?: number;
  sinceMinutes?: number;
  minLevel?: string;
}): Promise<LogsResponse> {
  const params = new URLSearchParams();
  if (opts.lines) params.set("lines", String(opts.lines));
  if (opts.sinceMinutes) params.set("since_minutes", String(opts.sinceMinutes));
  if (opts.minLevel) params.set("min_level", opts.minLevel);
  return fetchJson<LogsResponse>(`/logs/manager?${params.toString()}`);
}

// ─── 主控会话域 (代理) ────────────────────────────────────────────────────────

export type ControllerSessionInfo = {
  ok: boolean;
  token?: string;
  message: string;
  base_url: string;
};

/**
 * POST /api/controller/session/login
 * 获取 manager_service 缓存的 controller 会话 token。
 */
export async function apiGetControllerSession(): Promise<ControllerSessionInfo> {
  return fetchJson<ControllerSessionInfo>("/controller/session/login", { method: "POST" });
}

// ─── R5-BE: Cloudflare 域代理 ────────────────────────────────────────────────

export type CloudflareAPIKeyInfo = { api_key: string; zone_name: string; configured: boolean };
export type CloudflareDDNSRecord = Record<string, unknown>;
export type CloudflareDDNSApplyResult = { zone_name: string; applied: number; skipped: number; items?: unknown[]; records?: unknown[] };
export type CloudflareZeroTrustWhitelistState = Record<string, unknown>;

/** GET /api/cloudflare/api-key */
export async function apiGetCloudflareAPIKey(): Promise<CloudflareAPIKeyInfo> {
  return fetchJson("/cloudflare/api-key");
}
/** POST /api/cloudflare/api-key */
export async function apiSetCloudflareAPIKey(apiKey: string): Promise<CloudflareAPIKeyInfo> {
  return fetchJson("/cloudflare/api-key", { method: "POST", body: JSON.stringify({ api_key: apiKey }) });
}
/** GET /api/cloudflare/zone */
export async function apiGetCloudflareZone(): Promise<{ zone_name: string }> {
  return fetchJson("/cloudflare/zone");
}
/** POST /api/cloudflare/zone */
export async function apiSetCloudflareZone(zoneName: string): Promise<{ zone_name: string }> {
  return fetchJson("/cloudflare/zone", { method: "POST", body: JSON.stringify({ zone_name: zoneName }) });
}
/** GET /api/cloudflare/ddns/records */
export async function apiGetCloudflareDDNSRecords(): Promise<{ records?: CloudflareDDNSRecord[] }> {
  return fetchJson("/cloudflare/ddns/records");
}
/** POST /api/cloudflare/ddns/apply */
export async function apiApplyCloudflareDDNS(zoneName?: string): Promise<CloudflareDDNSApplyResult> {
  return fetchJson("/cloudflare/ddns/apply", { method: "POST", body: JSON.stringify({ zone_name: zoneName }) });
}
/** GET /api/cloudflare/zerotrust/whitelist */
export async function apiGetZeroTrustWhitelist(): Promise<CloudflareZeroTrustWhitelistState> {
  return fetchJson("/cloudflare/zerotrust/whitelist");
}
/** POST /api/cloudflare/zerotrust/whitelist */
export async function apiSetZeroTrustWhitelist(payload: unknown): Promise<CloudflareZeroTrustWhitelistState> {
  return fetchJson("/cloudflare/zerotrust/whitelist", { method: "POST", body: JSON.stringify(payload) });
}
/** POST /api/cloudflare/zerotrust/sync */
export async function apiRunZeroTrustSync(force?: boolean): Promise<CloudflareZeroTrustWhitelistState> {
  return fetchJson("/cloudflare/zerotrust/sync", { method: "POST", body: JSON.stringify({ force: force ?? true }) });
}

// ─── R6-BE: TG 助手域代理 ────────────────────────────────────────────────────

export type TGAPIKey = { app_id?: number; app_hash?: string; configured?: boolean };
export type TGAccount = Record<string, unknown>;
export type TGSchedule = Record<string, unknown>;
export type TGTarget = Record<string, unknown>;
export type TGBot = Record<string, unknown>;

/** GET /api/tg/api-key */
export async function apiGetTGAPIKey(): Promise<TGAPIKey> { return fetchJson("/tg/api-key"); }
/** POST /api/tg/api-key */
export async function apiSetTGAPIKey(payload: unknown): Promise<TGAPIKey> {
  return fetchJson("/tg/api-key", { method: "POST", body: JSON.stringify(payload) });
}
/** GET /api/tg/accounts */
export async function apiListTGAccounts(): Promise<{ accounts?: TGAccount[] }> { return fetchJson("/tg/accounts"); }
/** POST /api/tg/accounts/refresh */
export async function apiRefreshTGAccounts(): Promise<{ accounts?: TGAccount[] }> {
  return fetchJson("/tg/accounts/refresh", { method: "POST" });
}
/** POST /api/tg/accounts/add */
export async function apiAddTGAccount(payload: unknown): Promise<unknown> {
  return fetchJson("/tg/accounts/add", { method: "POST", body: JSON.stringify(payload) });
}
/** POST /api/tg/accounts/send-code */
export async function apiTGSendCode(payload: unknown): Promise<unknown> {
  return fetchJson("/tg/accounts/send-code", { method: "POST", body: JSON.stringify(payload) });
}
/** POST /api/tg/accounts/sign-in */
export async function apiTGSignIn(payload: unknown): Promise<unknown> {
  return fetchJson("/tg/accounts/sign-in", { method: "POST", body: JSON.stringify(payload) });
}
/** POST /api/tg/accounts/logout */
export async function apiLogoutTGAccount(payload: unknown): Promise<unknown> {
  return fetchJson("/tg/accounts/logout", { method: "POST", body: JSON.stringify(payload) });
}
/** POST /api/tg/accounts/remove */
export async function apiRemoveTGAccount(payload: unknown): Promise<unknown> {
  return fetchJson("/tg/accounts/remove", { method: "POST", body: JSON.stringify(payload) });
}
/** GET /api/tg/schedules */
export async function apiListTGSchedules(): Promise<{ schedules?: TGSchedule[] }> { return fetchJson("/tg/schedules"); }
/** POST /api/tg/schedules */
export async function apiAddTGSchedule(payload: unknown): Promise<unknown> {
  return fetchJson("/tg/schedules", { method: "POST", body: JSON.stringify(payload) });
}
/** PUT /api/tg/schedules/:id */
export async function apiUpdateTGSchedule(id: string, payload: unknown): Promise<unknown> {
  return fetchJson(`/tg/schedules/${encodeURIComponent(id)}`, { method: "PUT", body: JSON.stringify(payload) });
}
/** DELETE /api/tg/schedules/:id */
export async function apiRemoveTGSchedule(id: string): Promise<unknown> {
  return fetchJson(`/tg/schedules/${encodeURIComponent(id)}`, { method: "DELETE" });
}
/** POST /api/tg/schedules/:id/enable */
export async function apiEnableTGSchedule(id: string): Promise<unknown> {
  return fetchJson(`/tg/schedules/${encodeURIComponent(id)}/enable`, { method: "POST" });
}
/** POST /api/tg/schedules/:id/disable */
export async function apiDisableTGSchedule(id: string): Promise<unknown> {
  return fetchJson(`/tg/schedules/${encodeURIComponent(id)}/disable`, { method: "POST" });
}
/** POST /api/tg/schedules/:id/send-now */
export async function apiTGSendNow(id: string): Promise<unknown> {
  return fetchJson(`/tg/schedules/${encodeURIComponent(id)}/send-now`, { method: "POST" });
}
/** GET /api/tg/schedules/pending */
export async function apiGetTGPendingTasks(): Promise<{ tasks?: unknown[] }> { return fetchJson("/tg/schedules/pending"); }
/** GET /api/tg/schedules/history */
export async function apiGetTGHistory(payload?: unknown): Promise<{ records?: unknown[] }> {
  return fetchJson("/tg/schedules/history", payload ? { method: "GET" } : { method: "GET" });
}
/** GET /api/tg/targets */
export async function apiListTGTargets(): Promise<{ targets?: TGTarget[] }> { return fetchJson("/tg/targets"); }
/** POST /api/tg/targets/refresh */
export async function apiRefreshTGTargets(): Promise<{ targets?: TGTarget[] }> {
  return fetchJson("/tg/targets/refresh", { method: "POST" });
}
/** GET /api/tg/bot */
export async function apiGetTGBot(): Promise<TGBot> { return fetchJson("/tg/bot"); }
/** POST /api/tg/bot */
export async function apiSetTGBot(payload: unknown): Promise<TGBot> {
  return fetchJson("/tg/bot", { method: "POST", body: JSON.stringify(payload) });
}
/** POST /api/tg/bot/test-send */
export async function apiTGBotTestSend(payload: unknown): Promise<unknown> {
  return fetchJson("/tg/bot/test-send", { method: "POST", body: JSON.stringify(payload) });
}

// ─── R8-BE: 系统备份设置域代理 ───────────────────────────────────────────────

export type BackupSettings = { enabled: boolean; rclone_remote: string };
export type BackupSettingsTestResult = { ok: boolean; rclone_remote: string; message: string };

/** GET /api/system/backup-settings */
export async function apiGetBackupSettings(): Promise<BackupSettings> {
  return fetchJson("/system/backup-settings");
}
/** POST /api/system/backup-settings */
export async function apiSetBackupSettings(enabled: boolean, rcloneRemote: string): Promise<BackupSettings> {
  return fetchJson("/system/backup-settings", { method: "POST", body: JSON.stringify({ enabled, rclone_remote: rcloneRemote }) });
}
/** POST /api/system/backup-settings/test */
export async function apiTestBackupSettings(rcloneRemote: string): Promise<BackupSettingsTestResult> {
  return fetchJson("/system/backup-settings/test", { method: "POST", body: JSON.stringify({ rclone_remote: rcloneRemote }) });
}

// ─── W4: 主控日志 ─────────────────────────────────────────────────────────────

export type ControllerLogEntry = { time?: string; level?: string; message?: string; line?: string };
export type ControllerLogsResponse = {
  source?: string;
  file_path?: string;
  lines?: number;
  content?: string;
  fetched?: string;
  entries?: ControllerLogEntry[];
};

/** GET /api/system/controller-logs?lines=200&since_minutes=0&min_level=normal */
export async function apiGetControllerLogs(params?: { lines?: number; sinceMinutes?: number; minLevel?: string }): Promise<ControllerLogsResponse> {
  const qs = new URLSearchParams();
  if (params?.lines) qs.set("lines", String(params.lines));
  if (params?.sinceMinutes) qs.set("since_minutes", String(params.sinceMinutes));
  if (params?.minLevel) qs.set("min_level", params.minLevel);
  const q = qs.toString();
  return fetchJson(`/system/controller-logs${q ? `?${q}` : ""}`);
}

// ─── W4: 主控版本与升级 ──────────────────────────────────────────────────────

export type ControllerVersionInfo = {
  current_version?: string;
  latest_version?: string;
  release_repo?: string;
  upgrade_available?: boolean;
  message?: string;
};

export type ControllerUpgradeProgress = {
  active?: boolean;
  phase?: string;
  percent?: number;
  message?: string;
};

/** GET /api/system/controller-version */
export async function apiGetControllerVersion(): Promise<ControllerVersionInfo> {
  return fetchJson("/system/controller-version");
}

/** POST /api/system/controller-upgrade */
export async function apiUpgradeController(): Promise<{ ok?: boolean; message?: string }> {
  return fetchJson("/system/controller-upgrade", { method: "POST" });
}

/** GET /api/system/controller-upgrade-progress */
export async function apiGetControllerUpgradeProgress(): Promise<ControllerUpgradeProgress> {
  return fetchJson("/system/controller-upgrade-progress");
}

// ─── W4: Rule Routes 备份同步 ─────────────────────────────────────────────────

export type RuleRoutesUploadResult = { message?: string; file_name?: string; target_path?: string; size?: number; updated_at?: string };
export type RuleRoutesDownloadResult = { file_name?: string; target_path?: string; size?: number; content_base64?: string };

/** POST /api/system/rule-routes/upload */
export async function apiUploadRuleRoutes(content: string): Promise<RuleRoutesUploadResult> {
  return fetchJson("/system/rule-routes/upload", { method: "POST", body: JSON.stringify({ file_name: "rule_routes.txt", content }) });
}

/** POST /api/system/rule-routes/download */
export async function apiDownloadRuleRoutes(): Promise<RuleRoutesDownloadResult> {
  return fetchJson("/system/rule-routes/download", { method: "POST", body: JSON.stringify({ file_name: "rule_routes.txt" }) });
}
