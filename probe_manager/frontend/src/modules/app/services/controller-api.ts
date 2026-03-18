import type {
  ControllerUpgradeResponse,
  ControllerVersionResponse,
  DashboardStatusResponse,
  LogContentResponse,
  LoginResponse,
  NonceResponse,
  TGAssistantAccount,
  TGAssistantAPIKey,
  TGAssistantSchedule,
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
  return await callAdminWSRpc<ControllerUpgradeResponse>(baseURL, token, "admin.upgrade");
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
  node_secret: string;
  target_system: "linux" | "windows";
  direct_connect: boolean;
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

export async function fetchProbeNodes(baseURL: string, token: string): Promise<ProbeNodeSyncItem[]> {
  const payload = await callAdminWSRpc<{ nodes?: ProbeNodeSyncItem[] }>(baseURL, token, "admin.probe.nodes.get");
  return Array.isArray(payload.nodes) ? payload.nodes : [];
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

export async function upgradeProbeNode(baseURL: string, token: string, nodeID: number): Promise<void> {
  await callAdminWSRpc(baseURL, token, "admin.probe.upgrade", { node_id: String(nodeID) });
}

export async function upgradeAllProbeNodes(baseURL: string, token: string): Promise<{ success: number; total: number; failures: string[] }> {
  const payload = await callAdminWSRpc<{ success?: number; total?: number; failures?: string[] }>(baseURL, token, "admin.probe.upgrade.all");
  return {
    success: typeof payload.success === "number" ? payload.success : 0,
    total: typeof payload.total === "number" ? payload.total : 0,
    failures: Array.isArray(payload.failures) ? payload.failures : [],
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

export async function removeTGAssistantSchedule(
  baseURL: string,
  token: string,
  input: { account_id: string; task_id: string },
): Promise<TGAssistantSchedule[]> {
  const payload = await callAdminWSRpc<{ schedules?: TGAssistantSchedule[] }>(baseURL, token, "admin.tg.schedule.remove", input);
  return Array.isArray(payload.schedules) ? payload.schedules : [];
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
