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

// ─── 网络助手域 ───────────────────────────────────────────────────────────────

export type NetworkAssistantStatusRaw = Record<string, unknown>;

/**
 * GET /api/network-assistant/status
 * 返回 probe_manager 的网络助手状态（透传原始 JSON）。
 */
export async function apiGetNetworkAssistantStatus(): Promise<NetworkAssistantStatusRaw> {
  return fetchJson<NetworkAssistantStatusRaw>("/network-assistant/status");
}

/**
 * POST /api/network-assistant/mode
 * 切换网络助手模式（direct / proxy / tun 等）。
 */
export async function apiSwitchNetworkAssistantMode(mode: string): Promise<NetworkAssistantStatusRaw> {
  return fetchJson<NetworkAssistantStatusRaw>("/network-assistant/mode", {
    method: "POST",
    body: JSON.stringify({ mode }),
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
