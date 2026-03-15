import type {
  ControllerUpgradeResponse,
  ControllerVersionResponse,
  DashboardStatusResponse,
  LogContentResponse,
  LoginResponse,
  NonceResponse,
  UpgradeProgress,
} from "../types";

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
  const response = await fetch(`${baseURL}/api/admin/status`, {
    headers: { Authorization: `Bearer ${token}` },
  });
  if (!response.ok) {
    const errBody = await response.text();
    throw new Error(`admin check failed: HTTP ${response.status} ${errBody}`);
  }
  return (await response.json()) as { status: string; uptime: number; server_time: string };
}

export async function fetchControllerVersion(baseURL: string, token: string): Promise<ControllerVersionResponse> {
  const response = await fetch(`${baseURL}/api/admin/version`, {
    headers: { Authorization: `Bearer ${token}` },
  });
  if (!response.ok) {
    const errBody = await response.text();
    throw new Error(`controller version failed: HTTP ${response.status} ${errBody}`);
  }
  return (await response.json()) as ControllerVersionResponse;
}

export async function triggerControllerUpgrade(baseURL: string, token: string): Promise<ControllerUpgradeResponse> {
  const response = await fetch(`${baseURL}/api/admin/upgrade`, {
    method: "POST",
    headers: { Authorization: `Bearer ${token}` },
  });
  if (!response.ok) {
    const errBody = await response.text();
    throw new Error(`upgrade failed: HTTP ${response.status} ${errBody}`);
  }
  return (await response.json()) as ControllerUpgradeResponse;
}

export async function fetchControllerUpgradeProgress(baseURL: string, token: string): Promise<UpgradeProgress> {
  const response = await fetch(`${baseURL}/api/admin/upgrade/progress`, {
    headers: { Authorization: `Bearer ${token}` },
  });
  if (!response.ok) {
    const errBody = await response.text();
    throw new Error(`upgrade progress failed: HTTP ${response.status} ${errBody}`);
  }
  return (await response.json()) as UpgradeProgress;
}

export async function fetchServerLogs(baseURL: string, token: string, lines: number, sinceMinutes: number): Promise<LogContentResponse> {
  const safeLines = Number.isFinite(lines) ? Math.max(1, Math.min(2000, Math.trunc(lines))) : 200;
  const safeSince = Number.isFinite(sinceMinutes) ? Math.max(0, Math.min(2000, Math.trunc(sinceMinutes))) : 0;
  const qs = safeSince > 0 ? `?lines=${safeLines}&since_minutes=${safeSince}` : `?lines=${safeLines}`;
  const response = await fetch(`${baseURL}/api/admin/logs${qs}`, {
    headers: { Authorization: `Bearer ${token}` },
  });
  if (!response.ok) {
    const errBody = await response.text();
    throw new Error(`server logs failed: HTTP ${response.status} ${errBody}`);
  }
  return (await response.json()) as LogContentResponse;
}

export async function upsertProbeSecret(baseURL: string, token: string, nodeID: number, secret: string): Promise<void> {
  const response = await fetch(`${baseURL}/api/admin/probe/secret`, {
    method: "POST",
    headers: {
      Authorization: `Bearer ${token}`,
      "Content-Type": "application/json",
    },
    body: JSON.stringify({
      node_id: String(nodeID),
      secret,
    }),
  });
  if (!response.ok) {
    const errBody = await response.text();
    throw new Error(`sync probe secret failed: HTTP ${response.status} ${errBody}`);
  }
}

export type ProbeNodeSyncItem = {
  node_no: number;
  node_name: string;
  node_secret: string;
  target_system: "linux" | "windows";
  direct_connect: boolean;
  created_at: string;
  updated_at: string;
};

export async function fetchProbeNodes(baseURL: string, token: string): Promise<ProbeNodeSyncItem[]> {
  const response = await fetch(`${baseURL}/api/admin/probe/nodes`, {
    headers: { Authorization: `Bearer ${token}` },
  });
  if (!response.ok) {
    const errBody = await response.text();
    throw new Error(`fetch probe nodes failed: HTTP ${response.status} ${errBody}`);
  }

  const payload = (await response.json()) as { nodes?: ProbeNodeSyncItem[] };
  return Array.isArray(payload.nodes) ? payload.nodes : [];
}

export async function syncProbeNodes(baseURL: string, token: string, nodes: ProbeNodeSyncItem[]): Promise<ProbeNodeSyncItem[]> {
  const response = await fetch(`${baseURL}/api/admin/probe/nodes/sync`, {
    method: "POST",
    headers: {
      Authorization: `Bearer ${token}`,
      "Content-Type": "application/json",
    },
    body: JSON.stringify({ nodes }),
  });
  if (!response.ok) {
    const errBody = await response.text();
    throw new Error(`sync probe nodes failed: HTTP ${response.status} ${errBody}`);
  }

  const payload = (await response.json()) as { nodes?: ProbeNodeSyncItem[] };
  return Array.isArray(payload.nodes) ? payload.nodes : [];
}

export async function upgradeProbeNode(baseURL: string, token: string, nodeID: number): Promise<void> {
  const response = await fetch(`${baseURL}/api/admin/probe/upgrade`, {
    method: "POST",
    headers: {
      Authorization: `Bearer ${token}`,
      "Content-Type": "application/json",
    },
    body: JSON.stringify({ node_id: String(nodeID) }),
  });
  if (!response.ok) {
    const errBody = await response.text();
    throw new Error(`upgrade probe node failed: HTTP ${response.status} ${errBody}`);
  }
}

export async function upgradeAllProbeNodes(baseURL: string, token: string): Promise<{ success: number; total: number; failures: string[] }> {
  const response = await fetch(`${baseURL}/api/admin/probe/upgrade/all`, {
    method: "POST",
    headers: { Authorization: `Bearer ${token}` },
  });
  if (!response.ok) {
    const errBody = await response.text();
    throw new Error(`upgrade all probe nodes failed: HTTP ${response.status} ${errBody}`);
  }
  const payload = (await response.json()) as { success?: number; total?: number; failures?: string[] };
  return {
    success: typeof payload.success === "number" ? payload.success : 0,
    total: typeof payload.total === "number" ? payload.total : 0,
    failures: Array.isArray(payload.failures) ? payload.failures : [],
  };
}
