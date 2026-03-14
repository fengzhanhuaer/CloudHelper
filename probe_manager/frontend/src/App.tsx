import { useEffect, useState } from "react";
import logo from "./assets/images/logo-universal.png";
import "./App.css";

type NonceResponse = {
  nonce: string;
  expires_at: string;
};

type LoginResponse = {
  session_token: string;
  ttl: number;
};

const STORAGE_SECRET_KEY = "cloudhelper.manager.secret_key";
const STORAGE_REMEMBER_SECRET = "cloudhelper.manager.remember_secret";

function bytesToHex(bytes: Uint8Array): string {
  return Array.from(bytes)
    .map((v) => v.toString(16).padStart(2, "0"))
    .join("");
}

async function hmacSha256Hex(message: string, secret: string): Promise<string> {
  const encoder = new TextEncoder();
  const key = await crypto.subtle.importKey(
    "raw",
    encoder.encode(secret),
    { name: "HMAC", hash: "SHA-256" },
    false,
    ["sign"]
  );
  const signature = await crypto.subtle.sign("HMAC", key, encoder.encode(message));
  return bytesToHex(new Uint8Array(signature));
}

function App() {
  const [baseUrl, setBaseUrl] = useState("http://127.0.0.1:15030");
  const [secretKey, setSecretKey] = useState("");
  const [rememberSecret, setRememberSecret] = useState(false);
  const [sessionToken, setSessionToken] = useState("");
  const [loginStatus, setLoginStatus] = useState("Not logged in");
  const [serverStatus, setServerStatus] = useState("Probe Controller: Unknown");
  const [adminStatus, setAdminStatus] = useState("Admin API: Not checked");

  useEffect(() => {
    const remember = window.localStorage.getItem(STORAGE_REMEMBER_SECRET) === "1";
    setRememberSecret(remember);
    if (remember) {
      setSecretKey(window.localStorage.getItem(STORAGE_SECRET_KEY) ?? "");
    }
  }, []);

  useEffect(() => {
    if (rememberSecret) {
      window.localStorage.setItem(STORAGE_REMEMBER_SECRET, "1");
      window.localStorage.setItem(STORAGE_SECRET_KEY, secretKey);
      return;
    }

    window.localStorage.setItem(STORAGE_REMEMBER_SECRET, "0");
    window.localStorage.removeItem(STORAGE_SECRET_KEY);
  }, [rememberSecret, secretKey]);

  async function pingServer() {
    try {
      const response = await fetch(`${baseUrl}/api/ping`);
      if (!response.ok) {
        throw new Error(`HTTP ${response.status}`);
      }
      const data = (await response.json()) as { message: string; service: string };
      setServerStatus(`Probe Controller: ${data.message} from ${data.service}`);
    } catch (error) {
      const msg = error instanceof Error ? error.message : "unknown error";
      setServerStatus(`Probe Controller Error: ${msg}`);
    }
  }

  async function login() {
    if (!secretKey.trim()) {
      setLoginStatus("Secret Key is required");
      return;
    }

    try {
      setLoginStatus("Requesting nonce...");
      const nonceResp = await fetch(`${baseUrl}/api/auth/nonce`);
      if (!nonceResp.ok) {
        const errBody = await nonceResp.text();
        throw new Error(`nonce failed: HTTP ${nonceResp.status} ${errBody}`);
      }
      const nonceData = (await nonceResp.json()) as NonceResponse;

      setLoginStatus("Signing challenge...");
      const signature = await hmacSha256Hex(nonceData.nonce, secretKey);

      setLoginStatus("Submitting login...");
      const loginResp = await fetch(`${baseUrl}/api/auth/login`, {
        method: "POST",
        headers: { "Content-Type": "application/json" },
        body: JSON.stringify({
          nonce: nonceData.nonce,
          hmac: signature,
        }),
      });
      if (!loginResp.ok) {
        const errBody = await loginResp.text();
        throw new Error(`login failed: HTTP ${loginResp.status} ${errBody}`);
      }

      const loginData = (await loginResp.json()) as LoginResponse;
      setSessionToken(loginData.session_token);
      setLoginStatus(`Logged in. Token TTL: ${loginData.ttl}s`);
    } catch (error) {
      const msg = error instanceof Error ? error.message : "unknown error";
      setSessionToken("");
      setLoginStatus(msg);
    }
  }

  async function checkAdminStatus() {
    if (!sessionToken) {
      setAdminStatus("Missing session token, login first");
      return;
    }

    try {
      const resp = await fetch(`${baseUrl}/api/admin/status`, {
        headers: { Authorization: `Bearer ${sessionToken}` },
      });
      if (!resp.ok) {
        const errBody = await resp.text();
        throw new Error(`admin check failed: HTTP ${resp.status} ${errBody}`);
      }
      const data = (await resp.json()) as {
        status: string;
        uptime: number;
        server_time: string;
      };
      setAdminStatus(`Admin OK: status=${data.status}, uptime=${data.uptime}s`);
    } catch (error) {
      const msg = error instanceof Error ? error.message : "unknown error";
      setAdminStatus(msg);
    }
  }

  function logout() {
    setSessionToken("");
    setLoginStatus("Logged out");
    setAdminStatus("Admin API: Not checked");
  }

  return (
    <div id="App">
      <img src={logo} id="logo" alt="logo" />

      <div className="panel">
        <div className="row">
          <label htmlFor="base-url">Controller URL</label>
          <input
            id="base-url"
            className="input"
            value={baseUrl}
            onChange={(e) => setBaseUrl(e.target.value)}
          />
        </div>

        <div className="row">
          <label htmlFor="secret-key">Secret Key</label>
          <input
            id="secret-key"
            className="input"
            type="password"
            value={secretKey}
            onChange={(e) => setSecretKey(e.target.value)}
            placeholder="Paste the admin secret key"
          />
        </div>

        <div className="remember-row">
          <label htmlFor="remember-secret">
            <input
              id="remember-secret"
              type="checkbox"
              checked={rememberSecret}
              onChange={(e) => setRememberSecret(e.target.checked)}
            />
            Remember secret key locally
          </label>
        </div>

        <div className="btn-row">
          <button className="btn" onClick={pingServer}>
            Ping
          </button>
          <button className="btn" onClick={login}>
            Login
          </button>
          <button className="btn" onClick={checkAdminStatus}>
            Check Admin
          </button>
          <button className="btn" onClick={logout}>
            Logout
          </button>
        </div>

        <div className="status">{serverStatus}</div>
        <div className="status">{loginStatus}</div>
        <div className="status">{adminStatus}</div>
        <div className="status token">
          Session Token: {sessionToken ? `${sessionToken.slice(0, 24)}...` : "None"}
        </div>
      </div>
    </div>
  );
}

export default App;
