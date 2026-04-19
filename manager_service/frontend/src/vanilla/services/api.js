export const API_BASE = "/api";
export async function fetchJson(url, options = {}) {
    const token = localStorage.getItem("manager_session_token");
    const headers = new Headers(options.headers || {});
    if (token) {
        headers.set("X-Session-Token", token);
        headers.set("Authorization", `Bearer ${token}`);
    }
    headers.set("Content-Type", "application/json");
    const response = await fetch(url.startsWith("http") ? url : `${API_BASE}${url}`, {
        ...options,
        headers,
    });
    if (!response.ok) {
        if (response.status === 401) {
            localStorage.removeItem("manager_session_token");
            window.dispatchEvent(new Event("unauthorized"));
        }
        const errorBody = await response.text();
        let message = "";
        try {
            const j = JSON.parse(errorBody);
            message = j.message || errorBody;
        }
        catch {
            message = errorBody;
        }
        throw new Error(message || `HTTP error ${response.status}`);
    }
    const json = await response.json();
    // Support standard API response format (code, message, data, request_id)
    if (json && typeof json.code === "number") {
        if (json.code !== 0) {
            throw new Error(json.message || "Unknown error");
        }
        return json.data;
    }
    return json;
}
