import type {
  ControllerUpgradeResponse,
  ControllerVersionResponse,
  DashboardStatusResponse,
  LogContentResponse,
  LoginResponse,
  NonceResponse,
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
  node_secret: string;
  target_system: "linux" | "windows";
  direct_connect: boolean;
  created_at: string;
  updated_at: string;
  runtime?: {
    node_id?: string;
    online?: boolean;
    last_seen?: string;
    system?: {
      cpu_percent?: number;
      memory_used_percent?: number;
      swap_used_percent?: number;
      disk_used_percent?: number;
    };
  };
};

export async function fetchProbeNodes(baseURL: string, token: string): Promise<ProbeNodeSyncItem[]> {
  const payload = await callAdminWSRpc<{ nodes?: ProbeNodeSyncItem[] }>(baseURL, token, "admin.probe.nodes.get");
  return Array.isArray(payload.nodes) ? payload.nodes : [];
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
