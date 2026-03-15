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
