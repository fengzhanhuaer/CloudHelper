import type {
  ControllerUpgradeResponse,
  ControllerVersionResponse,
  DashboardStatusResponse,
  LoginResponse,
  NonceResponse,
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
