import type {
  ControllerUpgradeResponse,
  ControllerVersionResponse,
  CloudflareAPIKey,
  CloudflareDDNSApplyResult,
  CloudflareDDNSRecord,
  DashboardStatusResponse,
  LogContentResponse,
  LoginResponse,
  NonceResponse,
  TGAssistantAccount,
  TGAssistantAPIKey,
  TGAssistantBotAPIKey,
  TGAssistantBotTestSendResult,
  TGAssistantPendingTask,
  TGAssistantSchedule,
  TGAssistantScheduleSendNowResult,
  TGAssistantTaskHistoryRecord,
  TGAssistantTarget,
  UpgradeProgress,
} from "../types";
import { callAdminWSRpc } from "./admin-ws-rpc";

export async function fetchDashboardStatus(baseURL: string): Promise<DashboardStatusResponse> {
  const response = await fetch(`${baseURL}/dashboard/status`);
  if (!response.ok) {
    throw new Error(`HTTP ${response.status}`);
  }
  return (await response.json()) as DashboardStatusResponse;
}

export async function requestAuthNonce(baseURL: string): Promise<NonceResponse> {
  const response = await fetch(`${baseURL}/api/auth/nonce`);
  if (!response.ok) {
    const errBody = await response.text();
    throw new Error(`nonce failed: HTTP ${response.status} ${errBody}`);
  }
  return (await response.json()) as NonceResponse;
}

export async function submitAuthLogin(baseURL: string, nonce: string, signature: string): Promise<LoginResponse> {
  const response = await fetch(`${baseURL}/api/auth/login`, {
    method: "POST",
    headers: { "Content-Type": "application/json" },
    body: JSON.stringify({ nonce, signature }),
  });
  if (!response.ok) {
    const errBody = await response.text();
    throw new Error(`login failed: HTTP ${response.status} ${errBody}`);
  }
  return (await response.json()) as LoginResponse;
}

export async function fetchAdminStatus(baseURL: string, token: string): Promise<{ status: string; uptime: number; server_time: string }> {
  return await callAdminWSRpc<{ status: string; uptime: number; server_time: string }>(baseURL, token, "admin.status");
}

export async function fetchControllerVersion(baseURL: string, token: string): Promise<ControllerVersionResponse> {
  return await callAdminWSRpc<ControllerVersionResponse>(baseURL, token, "admin.version");
}

export async function triggerControllerUpgrade(baseURL: string, token: string): Promise<ControllerUpgradeResponse> {
	return await callAdminWSRpc<ControllerUpgradeResponse>(
		baseURL,
		token,
		"admin.upgrade",
		undefined,
		{ timeoutMs: 30000 },
	);
}

export async function fetchControllerUpgradeProgress(baseURL: string, token: string): Promise<UpgradeProgress> {
  return await callAdminWSRpc<UpgradeProgress>(baseURL, token, "admin.upgrade.progress");
}

export type ControllerBackupSettings = {
  enabled: boolean;
  rclone_remote: string;
};

export async function fetchControllerBackupSettings(baseURL: string, token: string): Promise<ControllerBackupSettings> {
  return await callAdminWSRpc<ControllerBackupSettings>(baseURL, token, "admin.backup.settings.get");
}

export async function setControllerBackupSettings(baseURL: string, token: string, enabled: boolean, rcloneRemote: string): Promise<ControllerBackupSettings> {
  return await callAdminWSRpc<ControllerBackupSettings>(baseURL, token, "admin.backup.settings.set", {
    enabled,
    rclone_remote: rcloneRemote,
  });
}

export async function testControllerBackupSettings(baseURL: string, token: string, rcloneRemote: string): Promise<{ ok: boolean; rclone_remote: string; message: string }> {
  return await callAdminWSRpc<{ ok: boolean; rclone_remote: string; message: string }>(baseURL, token, "admin.backup.settings.test", {
    rclone_remote: rcloneRemote,
  });
}

export async function fetchCloudflareAPIKey(baseURL: string, token: string): Promise<CloudflareAPIKey> {
  const payload = await callAdminWSRpc<CloudflareAPIKey>(baseURL, token, "admin.cloudflare.api.get");
  return {
    api_key: typeof payload.api_key === "string" ? payload.api_key : "",
    zone_name: typeof payload.zone_name === "string" ? payload.zone_name : "",
    configured: payload.configured === true,
  };
}

export async function setCloudflareAPIKey(baseURL: string, token: string, apiKey: string): Promise<CloudflareAPIKey> {
  const payload = await callAdminWSRpc<CloudflareAPIKey>(baseURL, token, "admin.cloudflare.api.set", {
    api_key: apiKey,
  });
  return {
    api_key: typeof payload.api_key === "string" ? payload.api_key : "",
    zone_name: typeof payload.zone_name === "string" ? payload.zone_name : "",
    configured: payload.configured === true,
  };
}

export async function fetchCloudflareZone(baseURL: string, token: string): Promise<string> {
  const payload = await callAdminWSRpc<{ zone_name?: string }>(baseURL, token, "admin.cloudflare.zone.get");
  return typeof payload.zone_name === "string" ? payload.zone_name : "";
}

export async function setCloudflareZone(baseURL: string, token: string, zoneName: string): Promise<string> {
  const payload = await callAdminWSRpc<{ zone_name?: string }>(baseURL, token, "admin.cloudflare.zone.set", {
    zone_name: zoneName,
  });
  return typeof payload.zone_name === "string" ? payload.zone_name : "";
}

export async function fetchCloudflareDDNSRecords(baseURL: string, token: string): Promise<CloudflareDDNSRecord[]> {
  const payload = await callAdminWSRpc<{ records?: CloudflareDDNSRecord[] }>(baseURL, token, "admin.cloudflare.ddns.records");
  return Array.isArray(payload.records) ? payload.records : [];
}

export async function applyCloudflareDDNS(baseURL: string, token: string, zoneName: string): Promise<CloudflareDDNSApplyResult> {
  const payload = await callAdminWSRpc<CloudflareDDNSApplyResult>(
    baseURL,
    token,
    "admin.cloudflare.ddns.apply",
    {
      zone_name: zoneName,
    },
    { timeoutMs: 120000 },
  );
  return {
    zone_name: typeof payload.zone_name === "string" ? payload.zone_name : zoneName,
    applied: typeof payload.applied === "number" ? payload.applied : 0,
    skipped: typeof payload.skipped === "number" ? payload.skipped : 0,
    items: Array.isArray(payload.items) ? payload.items : [],
    records: Array.isArray(payload.records) ? payload.records : [],
  };
}

export async function fetchServerLogs(baseURL: string, token: string, lines: number, sinceMinutes: number): Promise<LogContentResponse> {
  const safeLines = Number.isFinite(lines) ? Math.max(1, Math.min(2000, Math.trunc(lines))) : 200;
  const safeSince = Number.isFinite(sinceMinutes) ? Math.max(0, Math.min(2000, Math.trunc(sinceMinutes))) : 0;
  return await callAdminWSRpc<LogContentResponse>(baseURL, token, "admin.logs", {
    lines: safeLines,
    since_minutes: safeSince,
  });
}

export async function upsertProbeSecret(baseURL: string, token: string, nodeID: number, secret: string): Promise<void> {
  await callAdminWSRpc(baseURL, token, "admin.probe.secret.upsert", {
    node_id: String(nodeID),
    secret,
  });
}

export type ProbeNodeSyncItem = {
  node_no: number;
  node_name: string;
  remark?: string;
  ddns?: string;
  node_secret: string;
  target_system: "linux" | "windows";
  direct_connect: boolean;
  service_scheme?: "http" | "https";
  service_host?: string;
  service_port?: number;
  public_scheme?: "http" | "https";
  public_host?: string;
  public_port?: number;
  payment_cycle?: string;
  cost?: string;
  expire_at?: string;
  vendor_name?: string;
  vendor_url?: string;
  created_at: string;
  updated_at: string;
};

export type ProbeNodeStatusItem = {
  node_no: number;
  node_name: string;
  runtime: {
    node_id?: string;
    online?: boolean;
    last_seen?: string;
    version?: string;
    ipv4?: string[];
    ipv6?: string[];
    ip_locations?: Record<string, string>;
    system?: {
      cpu_percent?: number;
      memory_total_bytes?: number;
      memory_used_bytes?: number;
      memory_used_percent?: number;
      swap_total_bytes?: number;
      swap_used_bytes?: number;
      swap_used_percent?: number;
      disk_total_bytes?: number;
      disk_used_bytes?: number;
      disk_used_percent?: number;
    };
  };
};

export type ProbeReportIntervalSettings = {
  default_sec: number;
  current_sec: number;
  override_sec?: number;
  override_expires_at?: string;
  active_admin_connections?: number;
};

export type ProbeNodeLogsResponse = {
  node_id: string;
  node_name?: string;
  source?: string;
  file_path?: string;
  lines?: number;
  since_minutes?: number;
  content?: string;
  fetched?: string;
  timestamp?: string;
};

export type ProbeUpgradeDispatchResult = {
  ok: boolean;
  node_id: string;
  node_no: number;
  node_name?: string;
  direct_connect?: boolean;
  mode?: "direct" | "proxy" | string;
  repo?: string;
  message?: string;
  timestamp?: string;
};

export type ProbeUpgradeAllResponse = {
  success: number;
  total: number;
  failures: string[];
  items: ProbeUpgradeDispatchResult[];
  message: string;
};

export type ProbeLinkTestControlResponse = {
  ok: boolean;
  node_id: string;
  action: "start" | "stop";
  protocol?: "http" | "https" | "http3";
  listen_host?: string;
  internal_port?: number;
  message?: string;
  timestamp?: string;
};

export type ProbeShellSessionControlResponse = {
  ok: boolean;
  node_id: string;
  action: "start" | "exec" | "stop";
  session_id?: string;
  command?: string;
  stdout?: string;
  stderr?: string;
  error?: string;
  message?: string;
  started_at?: string;
  finished_at?: string;
  duration_ms?: number;
  timestamp?: string;
};

export type ProbeShellShortcutItem = {
  name: string;
  command: string;
  updated_at?: string;
};

export type ProbeLinkChainItem = {
	chain_id: string;
	name: string;
	user_id: string;
  user_public_key: string;
  secret: string;
  entry_node_id: string;
  exit_node_id: string;
  cascade_node_ids: string[];
  listen_host: string;
  listen_port: number;
  link_layer?: "http" | "http2" | "http3";
	hop_configs?: Array<{
		node_no: number;
		listen_host?: string;
		listen_port?: number;
		external_port?: number;
		link_layer: "http" | "http2" | "http3" | "";
		dial_mode?: "forward" | "reverse" | "";
	}>;
	port_forwards?: Array<{
		id?: string;
		name?: string;
		listen_host?: string;
		listen_port: number;
		target_host: string;
		target_port: number;
		network?: "tcp" | "udp" | "both" | "";
		enabled: boolean;
	}>;
	egress_host: string;
	egress_port: number;
	created_at?: string;
	updated_at?: string;
};

export type ProbeLinkChainUpsertPayload = {
  chain_id?: string;
  name: string;
  user_id: string;
  user_public_key: string;
  secret?: string;
  entry_node_id?: string;
  exit_node_id: string;
  cascade_node_ids: string[];
  listen_host?: string;
  listen_port: number;
  link_layer?: "http" | "http2" | "http3";
	hop_configs?: Array<{
		node_no: number;
		listen_host?: string;
		listen_port?: number;
		external_port?: number;
		link_layer?: "http" | "http2" | "http3";
		dial_mode?: "forward" | "reverse";
	}>;
	port_forwards?: Array<{
		id?: string;
		name?: string;
		listen_host?: string;
		listen_port: number;
		target_host: string;
		target_port: number;
		network?: "tcp" | "udp" | "both";
		enabled: boolean;
	}>;
	egress_host: string;
	egress_port: number;
};

export type ProbeLinkUserItem = {
  username: string;
  user_role?: string;
  cert_type?: string;
};

export type ProbeLinkUserPublicKeyResponse = {
  username: string;
  user_role?: string;
  cert_type?: string;
  public_key: string;
};

export async function fetchProbeNodes(baseURL: string, token: string): Promise<ProbeNodeSyncItem[]> {
  const payload = await callAdminWSRpc<{ nodes?: ProbeNodeSyncItem[] }>(baseURL, token, "admin.probe.nodes.get");
  return Array.isArray(payload.nodes) ? payload.nodes : [];
}

export async function fetchProbeLinkChains(baseURL: string, token: string): Promise<ProbeLinkChainItem[]> {
  const payload = await callAdminWSRpc<{ items?: ProbeLinkChainItem[] }>(baseURL, token, "admin.probe.link.chains.get");
  return Array.isArray(payload.items) ? payload.items : [];
}

export async function fetchProbeLinkUsers(baseURL: string, token: string): Promise<ProbeLinkUserItem[]> {
  const payload = await callAdminWSRpc<{ users?: ProbeLinkUserItem[] }>(baseURL, token, "admin.probe.link.users.get");
  if (!Array.isArray(payload.users)) {
    return [];
  }
  return payload.users
    .map((item) => ({
      username: typeof item?.username === "string" ? item.username : "",
      user_role: typeof item?.user_role === "string" ? item.user_role : "",
      cert_type: typeof item?.cert_type === "string" ? item.cert_type : "",
    }))
    .filter((item) => item.username.trim() !== "");
}

export async function fetchProbeLinkUserPublicKey(
  baseURL: string,
  token: string,
  username: string,
): Promise<ProbeLinkUserPublicKeyResponse> {
  const payload = await callAdminWSRpc<ProbeLinkUserPublicKeyResponse>(
    baseURL,
    token,
    "admin.probe.link.user.public_key.get",
    {
      username: String(username),
    },
  );
  return {
    username: typeof payload.username === "string" ? payload.username : String(username),
    user_role: typeof payload.user_role === "string" ? payload.user_role : "",
    cert_type: typeof payload.cert_type === "string" ? payload.cert_type : "",
    public_key: typeof payload.public_key === "string" ? payload.public_key : "",
  };
}

export async function upsertProbeLinkChain(
  baseURL: string,
  token: string,
  input: ProbeLinkChainUpsertPayload,
): Promise<{ item?: ProbeLinkChainItem; items: ProbeLinkChainItem[]; apply_ok?: boolean; apply_error?: string }> {
  const payload = await callAdminWSRpc<{
    item?: ProbeLinkChainItem;
    items?: ProbeLinkChainItem[];
    apply_ok?: boolean;
    apply_error?: string;
  }>(baseURL, token, "admin.probe.link.chain.upsert", input, { timeoutMs: 120000 });
  return {
    item: payload.item,
    items: Array.isArray(payload.items) ? payload.items : [],
    apply_ok: payload.apply_ok,
    apply_error: typeof payload.apply_error === "string" ? payload.apply_error : "",
  };
}

export async function deleteProbeLinkChain(
  baseURL: string,
  token: string,
  chainID: string,
): Promise<{ items: ProbeLinkChainItem[]; apply_ok?: boolean; apply_error?: string }> {
  const payload = await callAdminWSRpc<{
    items?: ProbeLinkChainItem[];
    apply_ok?: boolean;
    apply_error?: string;
  }>(baseURL, token, "admin.probe.link.chain.delete", {
    chain_id: String(chainID),
  }, { timeoutMs: 120000 });
  return {
    items: Array.isArray(payload.items) ? payload.items : [],
    apply_ok: payload.apply_ok,
    apply_error: typeof payload.apply_error === "string" ? payload.apply_error : "",
  };
}

export async function createProbeNodeOnController(baseURL: string, token: string, nodeName: string): Promise<ProbeNodeSyncItem> {
  const payload = await callAdminWSRpc<{ node?: ProbeNodeSyncItem }>(baseURL, token, "admin.probe.node.create", {
    node_name: String(nodeName),
  });
  if (!payload.node) {
    throw new Error("controller returned empty node");
  }
  return payload.node;
}

export async function updateProbeNodeOnController(
  baseURL: string,
  token: string,
  payload: {
    node_no: number;
    node_name: string;
    remark: string;
    ddns: string;
    target_system: "linux" | "windows";
    direct_connect: boolean;
    payment_cycle: string;
    cost: string;
    expire_at: string;
    vendor_name: string;
    vendor_url: string;
  },
): Promise<ProbeNodeSyncItem> {
  const result = await callAdminWSRpc<{ node?: ProbeNodeSyncItem }>(baseURL, token, "admin.probe.node.update", payload);
  if (!result.node) {
    throw new Error("controller returned empty node");
  }
  return result.node;
}

export async function updateProbeNodeLinkOnController(
  baseURL: string,
  token: string,
  payload: {
    node_no: number;
    service_scheme: "http" | "https";
    service_host: string;
    service_port: number;
    public_scheme: "http" | "https";
    public_host: string;
    public_port: number;
  },
): Promise<ProbeNodeSyncItem> {
  const result = await callAdminWSRpc<{ node?: ProbeNodeSyncItem }>(baseURL, token, "admin.probe.link.update", payload);
  if (!result.node) {
    throw new Error("controller returned empty node");
  }
  return result.node;
}

export async function startProbeLinkTestOnController(
  baseURL: string,
  token: string,
  payload: {
    node_id: string;
    protocol: "http" | "https" | "http3";
    internal_port: number;
  },
): Promise<ProbeLinkTestControlResponse> {
  return await callAdminWSRpc<ProbeLinkTestControlResponse>(
    baseURL,
    token,
    "admin.probe.link.test.start",
    payload,
    { timeoutMs: 130000 },
  );
}

export async function stopProbeLinkTestOnController(
  baseURL: string,
  token: string,
  nodeID: string,
): Promise<ProbeLinkTestControlResponse> {
  return await callAdminWSRpc<ProbeLinkTestControlResponse>(
    baseURL,
    token,
    "admin.probe.link.test.stop",
    {
      node_id: String(nodeID),
    },
    { timeoutMs: 60000 },
  );
}

export async function startProbeShellSessionOnController(
  baseURL: string,
  token: string,
  payload: {
    node_id: string;
  },
): Promise<ProbeShellSessionControlResponse> {
  return await callAdminWSRpc<ProbeShellSessionControlResponse>(
    baseURL,
    token,
    "admin.probe.shell.session.start",
    payload,
    { timeoutMs: 30000 },
  );
}

export async function execProbeShellSessionOnController(
  baseURL: string,
  token: string,
  payload: {
    node_id: string;
    session_id: string;
    command: string;
    timeout_sec: number;
  },
): Promise<ProbeShellSessionControlResponse> {
  const safeTimeoutSec = Number.isFinite(payload.timeout_sec) ? Math.max(5, Math.min(300, Math.trunc(payload.timeout_sec))) : 60;
  return await callAdminWSRpc<ProbeShellSessionControlResponse>(
    baseURL,
    token,
    "admin.probe.shell.session.exec",
    {
      node_id: String(payload.node_id),
      session_id: String(payload.session_id),
      command: String(payload.command),
      timeout_sec: safeTimeoutSec,
    },
    { timeoutMs: (safeTimeoutSec + 15) * 1000 },
  );
}

export async function stopProbeShellSessionOnController(
  baseURL: string,
  token: string,
  payload: {
    node_id: string;
    session_id: string;
  },
): Promise<ProbeShellSessionControlResponse> {
  return await callAdminWSRpc<ProbeShellSessionControlResponse>(
    baseURL,
    token,
    "admin.probe.shell.session.stop",
    payload,
    { timeoutMs: 45000 },
  );
}

export async function fetchProbeShellShortcuts(
  baseURL: string,
  token: string,
): Promise<ProbeShellShortcutItem[]> {
  const payload = await callAdminWSRpc<{ items?: ProbeShellShortcutItem[] }>(baseURL, token, "admin.probe.shell.shortcuts.get");
  return Array.isArray(payload.items) ? payload.items : [];
}

export async function upsertProbeShellShortcut(
  baseURL: string,
  token: string,
  payload: { name: string; command: string },
): Promise<ProbeShellShortcutItem[]> {
  const result = await callAdminWSRpc<{ items?: ProbeShellShortcutItem[] }>(
    baseURL,
    token,
    "admin.probe.shell.shortcuts.upsert",
    payload,
  );
  return Array.isArray(result.items) ? result.items : [];
}

export async function deleteProbeShellShortcut(
  baseURL: string,
  token: string,
  name: string,
): Promise<ProbeShellShortcutItem[]> {
  const result = await callAdminWSRpc<{ items?: ProbeShellShortcutItem[] }>(
    baseURL,
    token,
    "admin.probe.shell.shortcuts.delete",
    { name },
  );
  return Array.isArray(result.items) ? result.items : [];
}

export async function fetchProbeNodeStatus(baseURL: string, token: string, nodeID?: number | string): Promise<ProbeNodeStatusItem[]> {
  const payload = await callAdminWSRpc<{ items?: ProbeNodeStatusItem[] }>(
    baseURL,
    token,
    "admin.probe.status.get",
    nodeID === undefined || nodeID === null || String(nodeID).trim() === "" ? undefined : { node_id: String(nodeID) },
  );
  return Array.isArray(payload.items) ? payload.items : [];
}

export async function fetchProbeNodeLogs(baseURL: string, token: string, nodeID: number | string, lines: number, sinceMinutes: number): Promise<ProbeNodeLogsResponse> {
  const safeLines = Number.isFinite(lines) ? Math.max(1, Math.min(2000, Math.trunc(lines))) : 200;
  const safeSince = Number.isFinite(sinceMinutes) ? Math.max(0, Math.min(2000, Math.trunc(sinceMinutes))) : 0;
  return await callAdminWSRpc<ProbeNodeLogsResponse>(baseURL, token, "admin.probe.logs.get", {
    node_id: String(nodeID),
    lines: safeLines,
    since_minutes: safeSince,
  });
}

export async function fetchProbeReportIntervalSettings(baseURL: string, token: string): Promise<ProbeReportIntervalSettings> {
  return await callAdminWSRpc<ProbeReportIntervalSettings>(baseURL, token, "admin.probe.report_interval.get");
}

export async function setProbeReportInterval(baseURL: string, token: string, intervalSec: number): Promise<ProbeReportIntervalSettings> {
  return await callAdminWSRpc<ProbeReportIntervalSettings>(baseURL, token, "admin.probe.report_interval.set", { interval_sec: intervalSec });
}

export async function syncProbeNodes(baseURL: string, token: string, nodes: ProbeNodeSyncItem[]): Promise<ProbeNodeSyncItem[]> {
  const payload = await callAdminWSRpc<{ nodes?: ProbeNodeSyncItem[] }>(baseURL, token, "admin.probe.nodes.sync", { nodes });
  return Array.isArray(payload.nodes) ? payload.nodes : [];
}

export async function upgradeProbeNode(baseURL: string, token: string, nodeID: number): Promise<ProbeUpgradeDispatchResult> {
  const payload = await callAdminWSRpc<ProbeUpgradeDispatchResult>(baseURL, token, "admin.probe.upgrade", { node_id: String(nodeID) });
  return {
    ok: payload.ok !== false,
    node_id: String(payload.node_id || nodeID),
    node_no: typeof payload.node_no === "number" ? payload.node_no : nodeID,
    node_name: typeof payload.node_name === "string" ? payload.node_name : "",
    direct_connect: payload.direct_connect === true,
    mode: typeof payload.mode === "string" ? payload.mode : "",
    repo: typeof payload.repo === "string" ? payload.repo : "",
    message: typeof payload.message === "string" ? payload.message : "",
    timestamp: typeof payload.timestamp === "string" ? payload.timestamp : "",
  };
}

export async function upgradeAllProbeNodes(baseURL: string, token: string): Promise<ProbeUpgradeAllResponse> {
  const payload = await callAdminWSRpc<{
    success?: number;
    total?: number;
    failures?: string[];
    items?: ProbeUpgradeDispatchResult[];
    message?: string;
  }>(baseURL, token, "admin.probe.upgrade.all");
  return {
    success: typeof payload.success === "number" ? payload.success : 0,
    total: typeof payload.total === "number" ? payload.total : 0,
    failures: Array.isArray(payload.failures) ? payload.failures : [],
    items: Array.isArray(payload.items) ? payload.items.map((item) => ({
      ok: item.ok !== false,
      node_id: String(item.node_id || ""),
      node_no: typeof item.node_no === "number" ? item.node_no : 0,
      node_name: typeof item.node_name === "string" ? item.node_name : "",
      direct_connect: item.direct_connect === true,
      mode: typeof item.mode === "string" ? item.mode : "",
      repo: typeof item.repo === "string" ? item.repo : "",
      message: typeof item.message === "string" ? item.message : "",
      timestamp: typeof item.timestamp === "string" ? item.timestamp : "",
    })) : [],
    message: typeof payload.message === "string" ? payload.message : "",
  };
}

export async function fetchTGAssistantAccounts(baseURL: string, token: string): Promise<TGAssistantAccount[]> {
  const payload = await callAdminWSRpc<{ accounts?: TGAssistantAccount[] }>(baseURL, token, "admin.tg.accounts.list");
  return Array.isArray(payload.accounts) ? payload.accounts : [];
}

export async function fetchTGAssistantAPIKey(baseURL: string, token: string): Promise<TGAssistantAPIKey> {
  const payload = await callAdminWSRpc<TGAssistantAPIKey>(baseURL, token, "admin.tg.api.get");
  return {
    api_id: typeof payload.api_id === "number" ? payload.api_id : 0,
    api_hash: typeof payload.api_hash === "string" ? payload.api_hash : "",
    configured: payload.configured === true,
  };
}

export async function setTGAssistantAPIKey(
  baseURL: string,
  token: string,
  input: { api_id: number; api_hash: string },
): Promise<TGAssistantAPIKey> {
  const payload = await callAdminWSRpc<TGAssistantAPIKey>(baseURL, token, "admin.tg.api.set", input);
  return {
    api_id: typeof payload.api_id === "number" ? payload.api_id : 0,
    api_hash: typeof payload.api_hash === "string" ? payload.api_hash : "",
    configured: payload.configured === true,
  };
}

export async function refreshTGAssistantAccounts(baseURL: string, token: string): Promise<TGAssistantAccount[]> {
  const payload = await callAdminWSRpc<{ accounts?: TGAssistantAccount[] }>(baseURL, token, "admin.tg.accounts.refresh");
  return Array.isArray(payload.accounts) ? payload.accounts : [];
}

export async function addTGAssistantAccount(
  baseURL: string,
  token: string,
  input: { label: string; phone: string },
): Promise<TGAssistantAccount> {
  const payload = await callAdminWSRpc<{ account?: TGAssistantAccount }>(baseURL, token, "admin.tg.account.add", input);
  if (!payload.account) {
    throw new Error("controller returned empty account");
  }
  return payload.account;
}

export async function removeTGAssistantAccount(baseURL: string, token: string, accountID: string): Promise<TGAssistantAccount[]> {
  const payload = await callAdminWSRpc<{ accounts?: TGAssistantAccount[] }>(baseURL, token, "admin.tg.account.remove", {
    account_id: accountID,
  });
  return Array.isArray(payload.accounts) ? payload.accounts : [];
}

export async function sendTGAssistantLoginCode(baseURL: string, token: string, accountID: string): Promise<TGAssistantAccount> {
  const payload = await callAdminWSRpc<{ account?: TGAssistantAccount }>(baseURL, token, "admin.tg.account.send_code", {
    account_id: accountID,
  });
  if (!payload.account) {
    throw new Error("controller returned empty account");
  }
  return payload.account;
}

export async function completeTGAssistantLogin(
  baseURL: string,
  token: string,
  input: { account_id: string; code: string; password: string },
): Promise<TGAssistantAccount> {
  const payload = await callAdminWSRpc<{ account?: TGAssistantAccount }>(baseURL, token, "admin.tg.account.sign_in", input);
  if (!payload.account) {
    throw new Error("controller returned empty account");
  }
  return payload.account;
}

export async function logoutTGAssistantAccount(baseURL: string, token: string, accountID: string): Promise<TGAssistantAccount> {
  const payload = await callAdminWSRpc<{ account?: TGAssistantAccount }>(baseURL, token, "admin.tg.account.logout", {
    account_id: accountID,
  });
  if (!payload.account) {
    throw new Error("controller returned empty account");
  }
  return payload.account;
}

export async function fetchTGAssistantBotAPIKey(baseURL: string, token: string, accountID: string): Promise<TGAssistantBotAPIKey> {
  const payload = await callAdminWSRpc<TGAssistantBotAPIKey>(baseURL, token, "admin.tg.bot.get", {
    account_id: accountID,
  });
  return {
    account_id: typeof payload.account_id === "string" ? payload.account_id : accountID,
    api_key: typeof payload.api_key === "string" ? payload.api_key : "",
    configured: payload.configured === true,
    mode: payload.mode === "webhook" ? "webhook" : "polling",
    webhook_path: typeof payload.webhook_path === "string" ? payload.webhook_path : "",
    webhook_enabled: payload.webhook_enabled === true,
  };
}

export async function setTGAssistantBotAPIKey(
  baseURL: string,
  token: string,
  input: { account_id: string; api_key: string; mode?: "polling" | "webhook" },
): Promise<TGAssistantBotAPIKey> {
  const payload = await callAdminWSRpc<TGAssistantBotAPIKey>(baseURL, token, "admin.tg.bot.set", input);
  return {
    account_id: typeof payload.account_id === "string" ? payload.account_id : input.account_id,
    api_key: typeof payload.api_key === "string" ? payload.api_key : "",
    configured: payload.configured === true,
    mode: payload.mode === "webhook" ? "webhook" : "polling",
    webhook_path: typeof payload.webhook_path === "string" ? payload.webhook_path : "",
    webhook_enabled: payload.webhook_enabled === true,
  };
}

export async function testSendTGAssistantBotMessage(
  baseURL: string,
  token: string,
  input: { account_id: string; message: string },
): Promise<TGAssistantBotTestSendResult> {
  const payload = await callAdminWSRpc<{ result?: TGAssistantBotTestSendResult }>(
    baseURL,
    token,
    "admin.tg.bot.test_send",
    input,
    { timeoutMs: 30000 },
  );
  if (!payload.result) {
    throw new Error("controller returned empty bot test-send result");
  }
  return payload.result;
}

export async function fetchTGAssistantSchedules(baseURL: string, token: string, accountID: string): Promise<TGAssistantSchedule[]> {
  const payload = await callAdminWSRpc<{ schedules?: TGAssistantSchedule[] }>(baseURL, token, "admin.tg.schedule.list", {
    account_id: accountID,
  });
  return Array.isArray(payload.schedules) ? payload.schedules : [];
}

export async function addTGAssistantSchedule(
  baseURL: string,
  token: string,
  input: {
    account_id: string;
    task_type: string;
    enabled: boolean;
    target: string;
    send_at: string;
    message: string;
    delay_min_sec: number;
    delay_max_sec: number;
  },
): Promise<TGAssistantSchedule[]> {
  const payload = await callAdminWSRpc<{ schedules?: TGAssistantSchedule[] }>(baseURL, token, "admin.tg.schedule.add", input);
  return Array.isArray(payload.schedules) ? payload.schedules : [];
}

export async function updateTGAssistantSchedule(
  baseURL: string,
  token: string,
  input: {
    account_id: string;
    task_id: string;
    task_type: string;
    enabled: boolean;
    target: string;
    send_at: string;
    message: string;
    delay_min_sec: number;
    delay_max_sec: number;
  },
): Promise<TGAssistantSchedule[]> {
  const payload = await callAdminWSRpc<{ schedules?: TGAssistantSchedule[] }>(baseURL, token, "admin.tg.schedule.update", input);
  return Array.isArray(payload.schedules) ? payload.schedules : [];
}

export async function removeTGAssistantSchedule(
  baseURL: string,
  token: string,
  input: { account_id: string; task_id: string },
): Promise<TGAssistantSchedule[]> {
  const payload = await callAdminWSRpc<{ schedules?: TGAssistantSchedule[] }>(baseURL, token, "admin.tg.schedule.remove", input);
  return Array.isArray(payload.schedules) ? payload.schedules : [];
}

export async function setTGAssistantScheduleEnabled(
  baseURL: string,
  token: string,
  input: { account_id: string; task_id: string; enabled: boolean },
): Promise<TGAssistantSchedule[]> {
  const payload = await callAdminWSRpc<{ schedules?: TGAssistantSchedule[] }>(baseURL, token, "admin.tg.schedule.set_enabled", input);
  return Array.isArray(payload.schedules) ? payload.schedules : [];
}

export async function sendNowTGAssistantSchedule(
  baseURL: string,
  token: string,
  input: { account_id: string; task_id: string },
  options?: { timeoutMs?: number },
): Promise<TGAssistantScheduleSendNowResult> {
  const timeoutMs = Number.isFinite(options?.timeoutMs)
    ? Math.max(15000, Math.trunc(options?.timeoutMs ?? 90000))
    : 90000;
  const payload = await callAdminWSRpc<{ result?: TGAssistantScheduleSendNowResult }>(
    baseURL,
    token,
    "admin.tg.schedule.send_now",
    input,
    { timeoutMs },
  );
  if (!payload.result) {
    throw new Error("controller returned empty send-now result");
  }
  return payload.result;
}

export async function fetchTGAssistantScheduleTaskHistory(
  baseURL: string,
  token: string,
  input: { account_id: string; task_id: string; limit?: number },
): Promise<TGAssistantTaskHistoryRecord[]> {
  const limit = Number.isFinite(input.limit) ? Math.max(1, Math.min(360, Math.trunc(input.limit ?? 360))) : 360;
  const payload = await callAdminWSRpc<{ history?: TGAssistantTaskHistoryRecord[] }>(
    baseURL,
    token,
    "admin.tg.schedule.history",
    {
      account_id: input.account_id,
      task_id: input.task_id,
      limit,
    },
  );
  return Array.isArray(payload.history) ? payload.history : [];
}

export async function fetchTGAssistantPendingTasks(
  baseURL: string,
  token: string,
  accountID: string,
): Promise<TGAssistantPendingTask[]> {
  const payload = await callAdminWSRpc<{ pending?: TGAssistantPendingTask[] }>(
    baseURL,
    token,
    "admin.tg.schedule.pending",
    {
      account_id: accountID,
    },
  );
  return Array.isArray(payload.pending) ? payload.pending : [];
}

export async function fetchTGAssistantTargets(baseURL: string, token: string, accountID: string): Promise<TGAssistantTarget[]> {
  const payload = await callAdminWSRpc<{ targets?: TGAssistantTarget[] }>(baseURL, token, "admin.tg.targets.list", {
    account_id: accountID,
  });
  return Array.isArray(payload.targets) ? payload.targets : [];
}

export async function refreshTGAssistantTargets(baseURL: string, token: string, accountID: string): Promise<TGAssistantTarget[]> {
  const payload = await callAdminWSRpc<{ targets?: TGAssistantTarget[] }>(baseURL, token, "admin.tg.targets.refresh", {
    account_id: accountID,
  });
  return Array.isArray(payload.targets) ? payload.targets : [];
}
