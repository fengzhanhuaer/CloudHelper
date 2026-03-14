import { useEffect, useMemo, useState } from "react";
import logo from "./assets/images/logo-universal.png";
import "./App.css";
import { GetLocalPrivateKeyStatus, SignNonceWithLocalKey } from "../wailsjs/go/main/App";

type NonceResponse = {
  nonce: string;
  expires_at: string;
};

type LoginResponse = {
  session_token: string;
  ttl: number;
  username?: string;
  user_role?: string;
  cert_type?: string;
};

type DashboardStatusResponse = {
  message: string;
  service: string;
  uptime?: number;
};

type PrivateKeyStatus = {
  found: boolean;
  path?: string;
  message?: string;
};

type TabKey = "overview" | "probe-status" | "probe-manage" | "link-manage" | "system-settings";

type TabItem = {
  key: TabKey;
  label: string;
};

type StatusTone = "info" | "success" | "error";

const STORAGE_CONTROLLER_URL = "cloudhelper.manager.controller_url";

const ALL_TABS: TabItem[] = [
  { key: "overview", label: "概要状态" },
  { key: "probe-status", label: "探针状态" },
  { key: "probe-manage", label: "探针管理" },
  { key: "link-manage", label: "链路管理" },
  { key: "system-settings", label: "系统设置" },
];

const OPERATOR_TABS: TabItem[] = [
  { key: "overview", label: "概要状态" },
  { key: "probe-status", label: "探针状态" },
  { key: "probe-manage", label: "探针管理" },
  { key: "link-manage", label: "链路管理" },
];

const VIEWER_TABS: TabItem[] = [
  { key: "overview", label: "概要状态" },
  { key: "probe-status", label: "探针状态" },
];

function normalizeClaim(v: string | undefined, fallback: string): string {
  const normalized = (v ?? "").trim().toLowerCase();
  return normalized || fallback;
}

function normalizeUsernameClaim(v: string | undefined, fallback: string): string {
  const normalized = (v ?? "").trim();
  return normalized || fallback;
}

function resolveTabs(userRole: string, certType: string): TabItem[] {
  const role = normalizeClaim(userRole, "viewer");
  const type = normalizeClaim(certType, role);

  if (role === "admin" || type === "admin") {
    return ALL_TABS;
  }
  if (role === "operator" || type === "operator" || type === "ops") {
    return OPERATOR_TABS;
  }
  return VIEWER_TABS;
}

function App() {
  const [baseUrl, setBaseUrl] = useState("http://127.0.0.1:15030");
  const [sessionToken, setSessionToken] = useState("");

  const [loginStatus, setLoginStatus] = useState("请点击 Login 开始验证");
  const [loginTone, setLoginTone] = useState<StatusTone>("info");
  const [isAuthenticating, setIsAuthenticating] = useState(false);

  const [serverStatus, setServerStatus] = useState("");
  const [adminStatus, setAdminStatus] = useState("");
  const [privateKeyStatus, setPrivateKeyStatus] = useState("");
  const [privateKeyPath, setPrivateKeyPath] = useState("");

  const [username, setUsername] = useState("admin");
  const [userRole, setUserRole] = useState("viewer");
  const [certType, setCertType] = useState("viewer");
  const [activeTab, setActiveTab] = useState<TabKey>("overview");

  const tabs = useMemo(() => resolveTabs(userRole, certType), [userRole, certType]);

  useEffect(() => {
    try {
      const saved = window.localStorage.getItem(STORAGE_CONTROLLER_URL);
      if (saved && saved.trim()) {
        setBaseUrl(saved.trim());
      }
    } catch {
      // Ignore localStorage errors in restricted environments.
    }

    void refreshPrivateKeyStatus();
  }, []);

  useEffect(() => {
    try {
      window.localStorage.setItem(STORAGE_CONTROLLER_URL, baseUrl);
    } catch {
      // Ignore localStorage errors in restricted environments.
    }
  }, [baseUrl]);

  useEffect(() => {
    if (!tabs.some((item) => item.key === activeTab)) {
      setActiveTab(tabs[0].key);
    }
  }, [activeTab, tabs]);

  function normalizedBaseUrl(): string {
    return baseUrl.trim().replace(/\/+$/, "");
  }

  async function refreshPrivateKeyStatus() {
    try {
      const status = (await GetLocalPrivateKeyStatus()) as PrivateKeyStatus;
      if (status.found) {
        setPrivateKeyStatus("本地私钥已就绪");
        setPrivateKeyPath(status.path ?? "");
      } else {
        setPrivateKeyStatus(`本地私钥不可用：${status.message ?? "未找到"}`);
        setPrivateKeyPath("");
      }
    } catch (error) {
      const msg = error instanceof Error ? error.message : "unknown error";
      setPrivateKeyStatus(`本地私钥异常：${msg}`);
      setPrivateKeyPath("");
    }
  }

  async function pingServer() {
    const base = normalizedBaseUrl();
    if (!base) {
      setServerStatus("Controller URL is required");
      return;
    }

    try {
      const response = await fetch(`${base}/dashboard/status`);
      if (!response.ok) {
        throw new Error(`HTTP ${response.status}`);
      }
      const data = (await response.json()) as DashboardStatusResponse;
      const uptime = typeof data.uptime === "number" ? `，运行 ${data.uptime}s` : "";
      setServerStatus(`主控在线：${data.message} / ${data.service}${uptime}`);
    } catch (error) {
      const msg = error instanceof Error ? error.message : "unknown error";
      setServerStatus(`主控状态获取失败：${msg}`);
    }
  }

  async function login() {
    const base = normalizedBaseUrl();
    if (!base) {
      setLoginTone("error");
      setLoginStatus("登录失败：Controller URL is required");
      return;
    }

    setIsAuthenticating(true);
    try {
      setLoginTone("info");
      setLoginStatus("登录验证中：检查本地私钥...");
      const keyState = (await GetLocalPrivateKeyStatus()) as PrivateKeyStatus;
      if (!keyState.found) {
        const reason = keyState.message ?? "not found";
        setSessionToken("");
        setPrivateKeyStatus(`本地私钥不可用：${reason}`);
        setPrivateKeyPath("");
        setLoginTone("error");
        setLoginStatus(`登录失败：本地私钥不可用（${reason}）`);
        return;
      }

      setPrivateKeyStatus("本地私钥已就绪");
      setPrivateKeyPath(keyState.path ?? "");

      setLoginStatus("登录验证中：请求 challenge nonce...");
      const nonceResp = await fetch(`${base}/api/auth/nonce`);
      if (!nonceResp.ok) {
        const errBody = await nonceResp.text();
        throw new Error(`nonce failed: HTTP ${nonceResp.status} ${errBody}`);
      }
      const nonceData = (await nonceResp.json()) as NonceResponse;

      setLoginStatus("登录验证中：使用本地私钥签名...");
      const signature = await SignNonceWithLocalKey(nonceData.nonce);

      setLoginStatus("登录验证中：提交签名验证...");
      const loginResp = await fetch(`${base}/api/auth/login`, {
        method: "POST",
        headers: { "Content-Type": "application/json" },
        body: JSON.stringify({
          nonce: nonceData.nonce,
          signature,
        }),
      });
      if (!loginResp.ok) {
        const errBody = await loginResp.text();
        throw new Error(`login failed: HTTP ${loginResp.status} ${errBody}`);
      }

      const loginData = (await loginResp.json()) as LoginResponse;
      const user = normalizeUsernameClaim(loginData.username, "admin");
      const role = normalizeClaim(loginData.user_role, "admin");
      const type = normalizeClaim(loginData.cert_type, role);

      setSessionToken(loginData.session_token);
      setUsername(user);
      setUserRole(role);
      setCertType(type);

      const authorizedTabs = resolveTabs(role, type);
      setActiveTab(authorizedTabs[0].key);

      setLoginTone("success");
      setLoginStatus(`登录成功：username=${user}, role=${role}, certType=${type}, TTL=${loginData.ttl}s`);
      setServerStatus("");
      setAdminStatus("");
    } catch (error) {
      const msg = error instanceof Error ? error.message : "unknown error";
      setSessionToken("");
      setLoginTone("error");
      setLoginStatus(`登录失败：${msg}`);
    } finally {
      setIsAuthenticating(false);
    }
  }

  async function checkAdminStatus() {
    const base = normalizedBaseUrl();
    if (!base) {
      setAdminStatus("Controller URL is required");
      return;
    }

    if (!sessionToken) {
      setAdminStatus("未登录，无法访问管理接口");
      return;
    }

    try {
      const resp = await fetch(`${base}/api/admin/status`, {
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
      setAdminStatus(`管理接口正常：status=${data.status}, uptime=${data.uptime}s`);
    } catch (error) {
      const msg = error instanceof Error ? error.message : "unknown error";
      setAdminStatus(`管理接口异常：${msg}`);
    }
  }

  function logout() {
    setSessionToken("");
    setUsername("admin");
    setUserRole("viewer");
    setCertType("viewer");
    setActiveTab("overview");
    setLoginTone("info");
    setLoginStatus("已退出登录");
    setServerStatus("");
    setAdminStatus("");
  }

  function renderTabContent() {
    switch (activeTab) {
      case "overview":
        return (
          <div className="content-block">
            <h2>概要状态</h2>
            <div className="identity-card">
              <div>用户名：{username}</div>
              <div>当前角色：{userRole}</div>
              <div>证书类型：{certType}</div>
              <div>私钥状态：{privateKeyStatus || "未检查"}</div>
              <div>私钥路径：{privateKeyPath || "未设置"}</div>
            </div>

            <div className="content-actions">
              <button className="btn" onClick={pingServer}>公开状态检测</button>
              <button className="btn" onClick={checkAdminStatus}>管理接口检测</button>
              <button className="btn" onClick={refreshPrivateKeyStatus}>刷新私钥状态</button>
            </div>

            <div className="status">{serverStatus || ""}</div>
            <div className="status">{adminStatus || ""}</div>
          </div>
        );
      case "probe-status":
        return (
          <div className="content-block">
            <h2>探针状态</h2>
            <p>根据当前证书授权，这里将展示可见探针的在线状态与健康信息。</p>
          </div>
        );
      case "probe-manage":
        return (
          <div className="content-block">
            <h2>探针管理</h2>
            <p>根据当前证书授权，这里将展示探针增删改、分组和策略下发能力。</p>
          </div>
        );
      case "link-manage":
        return (
          <div className="content-block">
            <h2>链路管理</h2>
            <p>根据当前证书授权，这里将展示链路拓扑、探测任务和阈值配置。</p>
          </div>
        );
      case "system-settings":
        return (
          <div className="content-block">
            <h2>系统设置</h2>
            <p>根据当前证书授权，这里将展示系统参数、安全策略和维护配置。</p>
          </div>
        );
      default:
        return null;
    }
  }

  if (!sessionToken) {
    return (
      <div id="App">
        <img src={logo} id="logo" alt="logo" />

        <div className="panel login-panel">
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
            <label>Local Private Key</label>
            <div className="status-inline">
              {privateKeyStatus || "未检查"}
              {privateKeyPath ? ` (${privateKeyPath})` : ""}
            </div>
          </div>

          <div className="btn-row">
            <button className="btn" onClick={refreshPrivateKeyStatus} disabled={isAuthenticating}>
              Refresh Key
            </button>
            <button className="btn" onClick={login} disabled={isAuthenticating}>
              {isAuthenticating ? "登录验证中..." : "Login"}
            </button>
          </div>

          <div className={`status auth-status ${loginTone}`}>{loginStatus}</div>
        </div>
      </div>
    );
  }

  return (
    <div id="App">
      <div className="app-shell">
        <aside className="sidebar">
          <div className="sidebar-title">CloudHelper Manager</div>
          <div className="sidebar-identity">user={username}</div>
          <div className="sidebar-identity">role={userRole}</div>
          <div className="sidebar-identity">cert={certType}</div>

          <div className="tab-list">
            {tabs.map((tab) => (
              <button
                key={tab.key}
                className={`tab-btn ${activeTab === tab.key ? "active" : ""}`}
                onClick={() => setActiveTab(tab.key)}
              >
                {tab.label}
              </button>
            ))}
          </div>

          <div className="sidebar-actions">
            <button className="btn" onClick={logout}>退出登录</button>
          </div>
        </aside>

        <main className="content">{renderTabContent()}</main>
      </div>
    </div>
  );
}

export default App;

