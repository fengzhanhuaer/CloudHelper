const API_BASE = "/api";

function getSessionToken() {
  try {
    return window.localStorage.getItem("manager_session_token") || "";
  } catch {
    return "";
  }
}

function clearSessionToken() {
  try {
    window.localStorage.removeItem("manager_session_token");
  } catch {
    // ignore
  }
}

async function fetchJson(url, options = {}) {
  const token = getSessionToken();
  const headers = new Headers(options.headers || {});
  if (token) {
    headers.set("X-Session-Token", token);
    headers.set("Authorization", `Bearer ${token}`);
  }
  if (!headers.has("Content-Type") && options.body !== undefined) {
    headers.set("Content-Type", "application/json");
  }

  const response = await fetch(url.startsWith("http") ? url : `${API_BASE}${url}`, {
    ...options,
    headers,
  });

  if (!response.ok) {
    if (response.status === 401) {
      clearSessionToken();
      window.dispatchEvent(new Event("unauthorized"));
    }
    const body = await response.text();
    let message = body;
    try {
      const json = JSON.parse(body);
      message = json.message || body;
    } catch {
      // keep raw
    }
    throw new Error(message || `HTTP error ${response.status}`);
  }

  const json = await response.json();
  if (json && typeof json.code === "number") {
    if (json.code !== 0) {
      throw new Error(json.message || "Unknown error");
    }
    return json.data;
  }
  return json;
}

export { fetchJson, getSessionToken, clearSessionToken };
