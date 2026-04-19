import { fetchJson } from "./fetch-json";

async function apiLogin(username, password) {
  return fetchJson("/auth/login", {
    method: "POST",
    body: JSON.stringify({ username, password }),
  });
}

async function apiLogout() {
  return fetchJson("/auth/logout", { method: "POST" }).catch(() => undefined);
}

async function apiGetVersion() {
  return fetchJson("/system/version");
}

async function apiHealthz() {
  return fetchJson("/healthz");
}

export { apiLogin, apiLogout, apiGetVersion, apiHealthz };
