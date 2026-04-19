import "../style.css";
import "../App.css";
import "./theme.css";

import { APP_EVENTS, createEventBus } from "./core/events";
import { createStore } from "./state/store";
import { normalizeUsernameClaim, resolveTabs } from "./authz";
import { fetchJson } from "./services/api";
import {
  apiLogin,
  apiLogout,
  apiGetVersion,
  apiGetControllerVersion,
  apiGetLogs,
  apiGetControllerLogs,
  apiGetNetworkAssistantStatus,
  apiSwitchNetworkAssistantMode,
  apiGetNetworkAssistantLogs,
  apiListNodes,
  apiGetNodesStatus,
  apiGetNodeLogs,
  apiGetCloudflareAPIKey,
  apiSetCloudflareAPIKey,
  apiGetCloudflareZone,
  apiSetCloudflareZone,
  apiGetCloudflareDDNSRecords,
  apiListTGAccounts,
  apiListTGSchedules,
} from "./services/manager-api";

const DEFAULT_BASE_URL = "http://127.0.0.1:15030";

function escapeHtml(value) {
  const node = document.createElement("div");
  node.innerText = String(value ?? "");
  return node.innerHTML;
}

function readStorage(key, fallback = "") {
  try {
    const value = window.localStorage.getItem(key);
    return value == null ? fallback : value;
  } catch {
    return fallback;
  }
}

function createAppShell(root) {
  const store = createStore();
  const bus = createEventBus();
  let mounted = false;
  let unsub = null;

  function ensureActiveTab() {
    const state = store.getState();
    const tabs = resolveTabs(state.auth.userRole, state.auth.certType);
    if (!tabs.some((tab) => tab.key === state.ui.activeTab)) {
      store.update("ui", { activeTab: tabs[0].key });
    }
  }

  function setAuthStatus(message, tone = "info") {
    store.update("auth", { loginStatus: message, loginTone: tone });
  }

  function setUiStatus(message) {
    store.update("ui", { statusMessage: message, errorMessage: "" });
    bus.emit(APP_EVENTS.STATUS_MESSAGE, { message });
  }

  function setUiError(message) {
    store.update("ui", { errorMessage: message });
    bus.emit(APP_EVENTS.ERROR_MESSAGE, { message });
  }

  async function handleLoginSubmit(event) {
    event.preventDefault();
    const form = event.currentTarget;
    const formData = new FormData(form);
    const username = String(formData.get("username") || "").trim();
    const password = String(formData.get("password") || "");

    if (!username || !password) {
      setAuthStatus("Login failed: username and password required", "error");
      return;
    }

    store.update("auth", { isAuthenticating: true });
    setAuthStatus("Authenticating...", "info");

    try {
      const data = await apiLogin(username, password);
      const token = String(data?.token || "");
      const normalized = normalizeUsernameClaim(data?.username, "admin");
      if (!token) throw new Error("empty session token");

      window.localStorage.setItem("manager_session_token", token);
      store.update("auth", {
        sessionToken: token,
        username: normalized,
        userRole: "admin",
        certType: "admin",
        loginTone: "success",
        loginStatus: `Login successful: username=${normalized}`,
      });

      ensureActiveTab();
      bus.emit(APP_EVENTS.AUTH_CHANGED, { loggedIn: true, token });
      await Promise.allSettled([refreshOverviewStatus(), refreshSystemVersions(), refreshNetworkStatus()]);
      setUiStatus("登录成功");
    } catch (error) {
      const message = error instanceof Error ? error.message : "unknown error";
      window.localStorage.removeItem("manager_session_token");
      store.update("auth", {
        sessionToken: "",
        loginTone: "error",
        loginStatus: `Login failed: ${message}`,
      });
      setUiError(`登录失败: ${message}`);
    } finally {
      store.update("auth", { isAuthenticating: false });
      render();
    }
  }

  async function handleLogout() {
    await apiLogout();
    window.localStorage.removeItem("manager_session_token");
    store.update("auth", {
      sessionToken: "",
      username: "admin",
      userRole: "viewer",
      certType: "viewer",
      loginTone: "info",
      loginStatus: "Logged out",
      isAuthenticating: false,
    });
    store.update("connection", { serverStatus: "", adminStatus: "", wsStatus: "" });
    bus.emit(APP_EVENTS.AUTH_CHANGED, { loggedIn: false });
    setUiStatus("已退出登录");
    render();
  }

  async function refreshOverviewStatus() {
    const state = store.getState();
    if (!state.auth.sessionToken) return;

    try {
      const version = await apiGetVersion();
      store.update("connection", {
        serverStatus: `manager_service 在线，版本：${version?.version || "unknown"}`,
      });
    } catch (error) {
      const message = error instanceof Error ? error.message : "unknown";
      store.update("connection", { serverStatus: `manager_service 状态异常：${message}` });
    }

    try {
      await fetchJson("/healthz");
      store.update("connection", { adminStatus: "manager_service 健康检查正常" });
    } catch {
      store.update("connection", { adminStatus: "manager_service 健康检查失败" });
    }
  }

  async function refreshSystemVersions() {
    store.update("upgrade", { versionStatus: "正在检查版本..." });
    try {
      const [managerData, controllerData] = await Promise.allSettled([
        apiGetVersion(),
        apiGetControllerVersion(),
      ]);
      const managerVersion =
        managerData.status === "fulfilled"
          ? managerData.value?.version || "unknown"
          : "error";
      const controllerVersion =
        controllerData.status === "fulfilled"
          ? controllerData.value?.current_version || "—"
          : "—";
      const controllerLatest =
        controllerData.status === "fulfilled"
          ? controllerData.value?.latest_version || "—"
          : "—";

      store.update("upgrade", {
        managerVersion,
        controllerVersion,
        controllerLatestVersion: controllerLatest,
        versionStatus:
          controllerData.status === "fulfilled"
            ? `manager ${managerVersion} | controller ${controllerVersion}`
            : `manager ${managerVersion} | 主控版本查询失败`,
      });
    } catch (error) {
      const message = error instanceof Error ? error.message : "unknown";
      store.update("upgrade", { versionStatus: `版本检查失败：${message}` });
    }
  }

  function refreshLocalSettings() {
    const baseUrl = readStorage("controller_base_url", DEFAULT_BASE_URL) || DEFAULT_BASE_URL;
    const controllerIP = readStorage("controller_ip", "");
    store.update("settings", {
      baseUrl,
      controllerIP,
      baseUrlStatus: `Controller URL loaded: ${baseUrl}`,
      controllerIPStatus: controllerIP
        ? `Using controller IP: ${controllerIP}`
        : "Controller IP not configured",
    });
  }

  function saveBaseUrl(value) {
    const next = String(value || "").trim() || DEFAULT_BASE_URL;
    window.localStorage.setItem("controller_base_url", next);
    store.update("settings", {
      baseUrl: next,
      baseUrlStatus: `Controller URL saved: ${next}`,
    });
    setUiStatus("主控地址已保存");
  }

  function saveControllerIP(value) {
    const next = String(value || "").trim();
    window.localStorage.setItem("controller_ip", next);
    store.update("settings", {
      controllerIP: next,
      controllerIPStatus: next
        ? `Controller IP saved: ${next}`
        : "Controller IP cleared",
    });
    setUiStatus("主控 IP 已保存");
  }

  async function refreshLogs() {
    const state = store.getState();
    const source = state.logViewer.source;
    const lines = state.logViewer.lines;
    const sinceMinutes = state.logViewer.sinceMinutes;
    const minLevel = state.logViewer.minLevel;

    store.update("logViewer", {
      isLoading: true,
      status: `正在刷新${source === "local" ? "本地" : "服务器"}日志...`,
    });

    try {
      if (source === "local") {
        const data = await apiGetLogs({ lines, sinceMinutes, minLevel });
        const content = String(data?.content || "");
        store.update("logViewer", {
          isLoading: false,
          status: `已加载本地日志 (${lines} 行)` ,
          content,
          logFilePath: String(data?.file_path || ""),
          copyStatus: "",
        });
      } else {
        const data = await apiGetControllerLogs({ lines, sinceMinutes, minLevel });
        store.update("logViewer", {
          isLoading: false,
          status: `已加载主控日志 (${lines} 行)`,
          content: String(data?.content || ""),
          logFilePath: String(data?.file_path || ""),
          copyStatus: "",
        });
      }
      bus.emit(APP_EVENTS.LOG_VIEWER_REFRESHED, { source });
    } catch (error) {
      const message = error instanceof Error ? error.message : "unknown";
      store.update("logViewer", {
        isLoading: false,
        status: `日志加载失败：${message}`,
      });
    }
  }

  async function copyLogs() {
    const text = String(store.getState().logViewer.content || "").trim();
    if (!text) {
      store.update("logViewer", { copyStatus: "暂无日志可复制" });
      return;
    }

    try {
      if (navigator?.clipboard?.writeText) {
        await navigator.clipboard.writeText(text);
      } else {
        const textarea = document.createElement("textarea");
        textarea.value = text;
        textarea.style.cssText = "position:fixed;opacity:0";
        document.body.appendChild(textarea);
        textarea.select();
        document.execCommand("copy");
        textarea.remove();
      }
      store.update("logViewer", { copyStatus: "已复制日志内容" });
    } catch (error) {
      const message = error instanceof Error ? error.message : "unknown";
      store.update("logViewer", { copyStatus: `复制失败：${message}` });
    }
  }

  async function refreshNetworkStatus() {
    try {
      const data = await apiGetNetworkAssistantStatus();
      store.update("network", {
        status: { ...store.getState().network.status, ...data },
        operateStatus: "状态已刷新",
      });
      bus.emit(APP_EVENTS.NETWORK_STATUS_REFRESHED, { mode: data?.mode || "direct" });
    } catch (error) {
      const message = error instanceof Error ? error.message : "unknown";
      store.update("network", { operateStatus: `状态刷新失败：${message}` });
    }
  }

  async function switchNetworkMode(mode) {
    store.update("network", { isOperating: true, operateStatus: "正在切换模式..." });
    try {
      const data = await apiSwitchNetworkAssistantMode(mode);
      store.update("network", {
        isOperating: false,
        status: { ...store.getState().network.status, ...data },
        operateStatus: mode === "tun" ? "已切换为 TUN 模式" : "已切换为直连模式",
      });
      await refreshNetworkLogs();
    } catch (error) {
      const message = error instanceof Error ? error.message : "unknown";
      store.update("network", {
        isOperating: false,
        operateStatus: `模式切换失败：${message}`,
      });
    }
  }

  async function refreshNetworkLogs() {
    const lines = store.getState().network.logLines;
    store.update("network", { isLoadingLogs: true, logStatus: "正在刷新网络助手日志..." });

    try {
      const data = await apiGetNetworkAssistantLogs(lines);
      const content = String(data?.content || "");
      store.update("network", {
        isLoadingLogs: false,
        logStatus: "网络助手日志已加载",
        logContent: content,
        logTotalCount: content ? content.split(/\r?\n/).filter(Boolean).length : 0,
        logVisibleCount: content ? content.split(/\r?\n/).filter(Boolean).length : 0,
      });
    } catch (error) {
      const message = error instanceof Error ? error.message : "unknown";
      store.update("network", {
        isLoadingLogs: false,
        logStatus: `网络助手日志加载失败：${message}`,
      });
    }
  }

  async function refreshProbeManage() {
    store.update("probeManage", { isLoading: true, status: "正在加载探针节点..." });
    try {
      const [nodes, statuses] = await Promise.all([
        apiListNodes(),
        apiGetNodesStatus(),
      ]);
      const statusMap = new Map();
      (Array.isArray(statuses) ? statuses : []).forEach((item) => {
        statusMap.set(item.node_no, item);
      });
      const rows = (Array.isArray(nodes) ? nodes : []).map((node) => {
        const s = statusMap.get(node.node_no) || {};
        return {
          ...node,
          online: !!s.online,
          last_seen: s.last_seen || "",
          version: s.version || "",
        };
      });

      const selected = rows.length > 0 ? rows[0].node_no : 0;
      store.update("probeManage", {
        isLoading: false,
        status: `已加载探针节点: ${rows.length}`,
        nodes: rows,
        selectedNodeNo: selected,
      });

      if (selected) {
        await refreshProbeNodeLogs(selected);
      }
    } catch (error) {
      const message = error instanceof Error ? error.message : "unknown";
      store.update("probeManage", {
        isLoading: false,
        status: `探针节点加载失败: ${message}`,
      });
    }
  }

  async function refreshProbeNodeLogs(nodeNo) {
    try {
      const data = await apiGetNodeLogs(nodeNo, { lines: 120 });
      store.update("probeManage", {
        nodeLogs: String(data?.content || ""),
        nodeLogsStatus: `节点 ${nodeNo} 日志已加载`,
      });
    } catch (error) {
      const message = error instanceof Error ? error.message : "unknown";
      store.update("probeManage", { nodeLogsStatus: `节点日志加载失败: ${message}` });
    }
  }

  async function refreshCloudflareConfig() {
    store.update("cloudflare", { isLoading: true, status: "正在加载 Cloudflare 配置..." });
    try {
      const [keyInfo, zoneInfo, recordsInfo] = await Promise.all([
        apiGetCloudflareAPIKey(),
        apiGetCloudflareZone(),
        apiGetCloudflareDDNSRecords(),
      ]);
      store.update("cloudflare", {
        isLoading: false,
        apiKey: String(keyInfo?.api_key || ""),
        zoneName: String(zoneInfo?.zone_name || ""),
        records: Array.isArray(recordsInfo?.records) ? recordsInfo.records : [],
        status: "Cloudflare 配置已加载",
      });
    } catch (error) {
      const message = error instanceof Error ? error.message : "unknown";
      store.update("cloudflare", {
        isLoading: false,
        status: `Cloudflare 配置加载失败: ${message}`,
      });
    }
  }

  async function saveCloudflareKey(value) {
    try {
      await apiSetCloudflareAPIKey(value);
      setUiStatus("Cloudflare API Key 已保存");
      await refreshCloudflareConfig();
    } catch (error) {
      const message = error instanceof Error ? error.message : "unknown";
      setUiError(`保存 Cloudflare API Key 失败: ${message}`);
    }
  }

  async function saveCloudflareZone(value) {
    try {
      await apiSetCloudflareZone(value);
      setUiStatus("Cloudflare Zone 已保存");
      await refreshCloudflareConfig();
    } catch (error) {
      const message = error instanceof Error ? error.message : "unknown";
      setUiError(`保存 Cloudflare Zone 失败: ${message}`);
    }
  }

  async function refreshTGData() {
    store.update("tg", { isLoading: true, status: "正在加载 TG 配置..." });
    try {
      const [accountsInfo, schedulesInfo] = await Promise.all([
        apiListTGAccounts(),
        apiListTGSchedules(),
      ]);
      const accounts = Array.isArray(accountsInfo?.accounts) ? accountsInfo.accounts : [];
      const schedules = Array.isArray(schedulesInfo?.schedules) ? schedulesInfo.schedules : [];
      store.update("tg", {
        isLoading: false,
        accounts,
        schedules,
        status: `TG 数据已加载: 账号 ${accounts.length} / 任务 ${schedules.length}`,
      });
    } catch (error) {
      const message = error instanceof Error ? error.message : "unknown";
      store.update("tg", {
        isLoading: false,
        status: `TG 数据加载失败: ${message}`,
      });
    }
  }

  function renderLogin(state) {
    return `
      <div id="App">
        <img src="./src/assets/images/site-icon.png" id="logo" alt="logo" />
        <form id="login-form" class="panel login-panel">
          <div class="row">
            <label for="username">Username</label>
            <input id="username" name="username" class="input" value="admin" ${state.auth.isAuthenticating ? "disabled" : ""} />
          </div>
          <div class="row">
            <label for="password">Password</label>
            <input id="password" name="password" class="input" type="password" ${state.auth.isAuthenticating ? "disabled" : ""} />
          </div>
          <div class="btn-row">
            <button class="btn" type="submit" ${state.auth.isAuthenticating ? "disabled" : ""}>
              ${state.auth.isAuthenticating ? "Logging in..." : "Login"}
            </button>
          </div>
          <div class="status auth-status ${escapeHtml(state.auth.loginTone)}">${escapeHtml(state.auth.loginStatus)}</div>
        </form>
      </div>
    `;
  }

  function renderOverview(state) {
    return `
      <section class="content-block">
        <h2>概要状态</h2>
        <div class="identity-card">
          <div>用户: ${escapeHtml(state.auth.username)}</div>
          <div>角色: ${escapeHtml(state.auth.userRole)}</div>
          <div>证书: ${escapeHtml(state.auth.certType)}</div>
          <div>服务状态: ${escapeHtml(state.connection.serverStatus || "未检查")}</div>
          <div>健康检查: ${escapeHtml(state.connection.adminStatus || "未检查")}</div>
        </div>
        <div class="content-actions">
          <button class="btn" id="btn-refresh-overview">刷新状态</button>
        </div>
      </section>
    `;
  }

  function renderSystemSettings(state) {
    return `
      <section class="content-block">
        <h2>系统设置</h2>
        <div class="identity-card">
          <div>Manager 版本: ${escapeHtml(state.upgrade.managerVersion)}</div>
          <div>Controller 版本: ${escapeHtml(state.upgrade.controllerVersion)}</div>
          <div>Controller 最新: ${escapeHtml(state.upgrade.controllerLatestVersion)}</div>
          <div>${escapeHtml(state.upgrade.versionStatus)}</div>
        </div>
        <div class="content-actions">
          <button class="btn" id="btn-refresh-versions">刷新版本</button>
        </div>
        <div class="row" style="margin-top:12px;">
          <label for="settings-base-url">Controller URL</label>
          <input id="settings-base-url" class="input" value="${escapeHtml(state.settings.baseUrl)}" />
        </div>
        <div class="row">
          <label for="settings-controller-ip">Controller IP</label>
          <input id="settings-controller-ip" class="input" value="${escapeHtml(state.settings.controllerIP)}" />
        </div>
        <div class="content-actions">
          <button class="btn" id="btn-save-base-url">保存主控地址</button>
          <button class="btn" id="btn-save-controller-ip">保存主控IP</button>
          <button class="btn" id="btn-refresh-settings">重新读取设置</button>
        </div>
        <div class="status">${escapeHtml(state.settings.baseUrlStatus || state.settings.controllerIPStatus || "")}</div>
      </section>
    `;
  }

  function renderLogViewer(state) {
    return `
      <section class="content-block">
        <h2>日志查看</h2>
        <div class="row">
          <label for="log-source">日志来源</label>
          <select id="log-source" class="input">
            <option value="local" ${state.logViewer.source === "local" ? "selected" : ""}>local</option>
            <option value="server" ${state.logViewer.source === "server" ? "selected" : ""}>server</option>
          </select>
        </div>
        <div class="row">
          <label for="log-lines">日志行数</label>
          <input id="log-lines" class="input" type="number" value="${escapeHtml(state.logViewer.lines)}" />
        </div>
        <div class="content-actions">
          <button class="btn" id="btn-refresh-logs" ${state.logViewer.isLoading ? "disabled" : ""}>刷新日志</button>
          <button class="btn" id="btn-copy-logs">复制日志</button>
        </div>
        <div class="status">${escapeHtml(state.logViewer.status)}</div>
        <div class="status">${escapeHtml(state.logViewer.copyStatus)}</div>
        <pre class="log-viewer-output">${escapeHtml(state.logViewer.content)}</pre>
      </section>
    `;
  }

  function renderNetworkAssistant(state) {
    return `
      <section class="content-block">
        <h2>网络助手</h2>
        <div class="identity-card">
          <div>当前模式: ${escapeHtml(state.network.status.mode || "direct")}</div>
          <div>节点: ${escapeHtml(state.network.status.node_id || "direct")}</div>
          <div>TUN 状态: ${escapeHtml(state.network.status.tun_status || "未安装")}</div>
          <div>系统代理: ${escapeHtml(state.network.status.system_proxy_status || "未设置")}</div>
          <div>${escapeHtml(state.network.operateStatus)}</div>
        </div>
        <div class="content-actions">
          <button class="btn" id="btn-network-refresh">刷新状态</button>
          <button class="btn" id="btn-network-direct" ${state.network.isOperating ? "disabled" : ""}>切换直连</button>
          <button class="btn" id="btn-network-tun" ${state.network.isOperating ? "disabled" : ""}>切换TUN</button>
          <button class="btn" id="btn-network-logs">刷新网络日志</button>
        </div>
        <div class="status">${escapeHtml(state.network.logStatus)}</div>
        <pre class="log-viewer-output">${escapeHtml(state.network.logContent || "")}</pre>
      </section>
    `;
  }

  function renderProbeManage(state) {
    const rows = (state.probeManage.nodes || []).map((node) => {
      return `<tr>
        <td>${escapeHtml(node.node_no)}</td>
        <td>${escapeHtml(node.node_name)}</td>
        <td>${node.online ? "在线" : "离线"}</td>
        <td>${escapeHtml(node.version || "")}</td>
        <td>${escapeHtml(node.last_seen || "")}</td>
      </tr>`;
    }).join("");

    return `
      <section class="content-block">
        <h2>探针管理</h2>
        <div class="content-actions">
          <button class="btn" id="btn-probe-refresh">刷新节点</button>
        </div>
        <div class="status">${escapeHtml(state.probeManage.status)}</div>
        <table class="probe-table" style="min-width:unset; margin-top:10px;">
          <thead><tr><th>No</th><th>Name</th><th>在线</th><th>版本</th><th>最后上报</th></tr></thead>
          <tbody>${rows || '<tr><td colspan="5">暂无节点</td></tr>'}</tbody>
        </table>
        <div class="status">${escapeHtml(state.probeManage.nodeLogsStatus || "")}</div>
        <pre class="log-viewer-output">${escapeHtml(state.probeManage.nodeLogs || "")}</pre>
      </section>
    `;
  }

  function renderCloudflareAssistant(state) {
    const records = (state.cloudflare.records || []).map((item) => {
      const host = item?.hostname || item?.name || "";
      const value = item?.value || item?.content || "";
      return `<tr><td>${escapeHtml(host)}</td><td>${escapeHtml(value)}</td></tr>`;
    }).join("");

    return `
      <section class="content-block">
        <h2>Cloudflare 助手</h2>
        <div class="row">
          <label for="cf-api-key">API Key</label>
          <input id="cf-api-key" class="input" value="${escapeHtml(state.cloudflare.apiKey || "")}" />
        </div>
        <div class="row">
          <label for="cf-zone">Zone</label>
          <input id="cf-zone" class="input" value="${escapeHtml(state.cloudflare.zoneName || "")}" />
        </div>
        <div class="content-actions">
          <button class="btn" id="btn-cf-refresh">刷新配置</button>
          <button class="btn" id="btn-cf-save-key">保存 API Key</button>
          <button class="btn" id="btn-cf-save-zone">保存 Zone</button>
        </div>
        <div class="status">${escapeHtml(state.cloudflare.status || "")}</div>
        <table class="probe-table" style="min-width:unset; margin-top:10px;">
          <thead><tr><th>记录</th><th>值</th></tr></thead>
          <tbody>${records || '<tr><td colspan="2">暂无记录</td></tr>'}</tbody>
        </table>
      </section>
    `;
  }

  function renderTGAssistant(state) {
    const accounts = (state.tg.accounts || []).map((item) => {
      return `<tr><td>${escapeHtml(item?.id || "")}</td><td>${escapeHtml(item?.label || "")}</td><td>${item?.authorized ? "是" : "否"}</td></tr>`;
    }).join("");
    const schedules = (state.tg.schedules || []).map((item) => {
      return `<tr><td>${escapeHtml(item?.id || "")}</td><td>${escapeHtml(item?.target || "")}</td><td>${item?.enabled ? "启用" : "停用"}</td></tr>`;
    }).join("");

    return `
      <section class="content-block">
        <h2>TG 助手</h2>
        <div class="content-actions">
          <button class="btn" id="btn-tg-refresh">刷新 TG 数据</button>
        </div>
        <div class="status">${escapeHtml(state.tg.status || "")}</div>
        <h3 style="margin-top:12px;">账号</h3>
        <table class="probe-table" style="min-width:unset;">
          <thead><tr><th>ID</th><th>Label</th><th>已授权</th></tr></thead>
          <tbody>${accounts || '<tr><td colspan="3">暂无账号</td></tr>'}</tbody>
        </table>
        <h3 style="margin-top:12px;">计划任务</h3>
        <table class="probe-table" style="min-width:unset;">
          <thead><tr><th>ID</th><th>Target</th><th>状态</th></tr></thead>
          <tbody>${schedules || '<tr><td colspan="3">暂无任务</td></tr>'}</tbody>
        </table>
      </section>
    `;
  }

  function renderPlaceholder(state) {
    const tab = state.ui.activeTab;
    return `
      <section class="content-block">
        <h2>${escapeHtml(tab)}</h2>
        <p>该模块将在后续迁移批次落地，当前入口与路由已稳定。</p>
      </section>
    `;
  }

  function renderMainContent(state) {
    switch (state.ui.activeTab) {
      case "overview":
        return renderOverview(state);
      case "probe-manage":
        return renderProbeManage(state);
      case "network-assistant":
        return renderNetworkAssistant(state);
      case "cloudflare-assistant":
        return renderCloudflareAssistant(state);
      case "tg-assistant":
        return renderTGAssistant(state);
      case "log-viewer":
        return renderLogViewer(state);
      case "system-settings":
        return renderSystemSettings(state);
      default:
        return renderPlaceholder(state);
    }
  }

  function renderMain(state) {
    const tabs = resolveTabs(state.auth.userRole, state.auth.certType);
    const tabButtons = tabs
      .map((tab) => {
        const active = tab.key === state.ui.activeTab ? "active" : "";
        return `<button class="tab-btn ${active}" data-tab="${escapeHtml(tab.key)}">${escapeHtml(tab.label)}</button>`;
      })
      .join("");

    const content = renderMainContent(state);

    return `
      <div id="App">
        <div class="app-shell">
          <aside class="sidebar">
            <div class="sidebar-title">CloudHelper Manager</div>
            <div class="sidebar-identity">${escapeHtml(state.auth.username)}</div>
            <div class="tab-list">${tabButtons}</div>
            <div class="sidebar-actions">
              <button class="btn" id="btn-logout">退出登录</button>
            </div>
          </aside>
          <main class="content">
            ${content}
            <div class="status" style="margin-top:12px;">${escapeHtml(state.ui.statusMessage)}</div>
            <div class="status" style="margin-top:8px;">${escapeHtml(state.ui.errorMessage)}</div>
          </main>
        </div>
      </div>
    `;
  }

  function bindCommonEvents() {
    root.querySelectorAll("[data-tab]").forEach((button) => {
      button.addEventListener("click", () => {
        const key = button.getAttribute("data-tab") || "overview";
        store.update("ui", { activeTab: key });
        bus.emit(APP_EVENTS.TAB_CHANGED, { activeTab: key });
      });
    });

    const logoutBtn = root.querySelector("#btn-logout");
    if (logoutBtn) logoutBtn.addEventListener("click", () => void handleLogout());
  }

  function bindOverviewEvents() {
    const refreshBtn = root.querySelector("#btn-refresh-overview");
    if (refreshBtn) {
      refreshBtn.addEventListener("click", () => void refreshOverviewStatus());
    }
  }

  function bindSystemSettingsEvents() {
    const refreshVersionBtn = root.querySelector("#btn-refresh-versions");
    if (refreshVersionBtn) {
      refreshVersionBtn.addEventListener("click", () => void refreshSystemVersions());
    }

    const saveBaseUrlBtn = root.querySelector("#btn-save-base-url");
    if (saveBaseUrlBtn) {
      saveBaseUrlBtn.addEventListener("click", () => {
        const input = root.querySelector("#settings-base-url");
        if (!input) return;
        saveBaseUrl(input.value);
      });
    }

    const saveControllerIPBtn = root.querySelector("#btn-save-controller-ip");
    if (saveControllerIPBtn) {
      saveControllerIPBtn.addEventListener("click", () => {
        const input = root.querySelector("#settings-controller-ip");
        if (!input) return;
        saveControllerIP(input.value);
      });
    }

    const refreshSettingsBtn = root.querySelector("#btn-refresh-settings");
    if (refreshSettingsBtn) {
      refreshSettingsBtn.addEventListener("click", refreshLocalSettings);
    }
  }

  function bindLogViewerEvents() {
    const sourceSelect = root.querySelector("#log-source");
    if (sourceSelect) {
      sourceSelect.addEventListener("change", () => {
        store.update("logViewer", { source: sourceSelect.value });
      });
    }

    const lineInput = root.querySelector("#log-lines");
    if (lineInput) {
      lineInput.addEventListener("change", () => {
        const n = Number(lineInput.value);
        const lines = Number.isFinite(n) ? Math.max(1, Math.min(2000, Math.trunc(n))) : 200;
        store.update("logViewer", { lines });
      });
    }

    const refreshBtn = root.querySelector("#btn-refresh-logs");
    if (refreshBtn) {
      refreshBtn.addEventListener("click", () => {
        bus.emit(APP_EVENTS.LOG_VIEWER_REFRESH_REQUESTED, {});
        void refreshLogs();
      });
    }

    const copyBtn = root.querySelector("#btn-copy-logs");
    if (copyBtn) {
      copyBtn.addEventListener("click", () => void copyLogs());
    }
  }

  function bindNetworkEvents() {
    const refreshBtn = root.querySelector("#btn-network-refresh");
    if (refreshBtn) {
      refreshBtn.addEventListener("click", () => {
        bus.emit(APP_EVENTS.NETWORK_STATUS_REFRESH_REQUESTED, {});
        void refreshNetworkStatus();
      });
    }

    const directBtn = root.querySelector("#btn-network-direct");
    if (directBtn) {
      directBtn.addEventListener("click", () => void switchNetworkMode("direct"));
    }

    const tunBtn = root.querySelector("#btn-network-tun");
    if (tunBtn) {
      tunBtn.addEventListener("click", () => void switchNetworkMode("tun"));
    }

    const logsBtn = root.querySelector("#btn-network-logs");
    if (logsBtn) {
      logsBtn.addEventListener("click", () => void refreshNetworkLogs());
    }
  }

  function bindProbeManageEvents() {
    const refreshBtn = root.querySelector("#btn-probe-refresh");
    if (refreshBtn) {
      refreshBtn.addEventListener("click", () => void refreshProbeManage());
    }
  }

  function bindCloudflareEvents() {
    const refreshBtn = root.querySelector("#btn-cf-refresh");
    if (refreshBtn) {
      refreshBtn.addEventListener("click", () => void refreshCloudflareConfig());
    }

    const saveKeyBtn = root.querySelector("#btn-cf-save-key");
    if (saveKeyBtn) {
      saveKeyBtn.addEventListener("click", () => {
        const input = root.querySelector("#cf-api-key");
        if (!input) return;
        void saveCloudflareKey(input.value);
      });
    }

    const saveZoneBtn = root.querySelector("#btn-cf-save-zone");
    if (saveZoneBtn) {
      saveZoneBtn.addEventListener("click", () => {
        const input = root.querySelector("#cf-zone");
        if (!input) return;
        void saveCloudflareZone(input.value);
      });
    }
  }

  function bindTGEvents() {
    const refreshBtn = root.querySelector("#btn-tg-refresh");
    if (refreshBtn) {
      refreshBtn.addEventListener("click", () => void refreshTGData());
    }
  }

  function bindEvents() {
    const state = store.getState();

    if (!state.auth.sessionToken) {
      const loginForm = root.querySelector("#login-form");
      if (loginForm) loginForm.addEventListener("submit", handleLoginSubmit);
      return;
    }

    bindCommonEvents();

    switch (state.ui.activeTab) {
      case "overview":
        bindOverviewEvents();
        break;
      case "probe-manage":
        bindProbeManageEvents();
        break;
      case "network-assistant":
        bindNetworkEvents();
        break;
      case "cloudflare-assistant":
        bindCloudflareEvents();
        break;
      case "tg-assistant":
        bindTGEvents();
        break;
      case "log-viewer":
        bindLogViewerEvents();
        break;
      case "system-settings":
        bindSystemSettingsEvents();
        break;
      default:
        break;
    }
  }

  function render() {
    const state = store.getState();
    root.innerHTML = state.auth.sessionToken ? renderMain(state) : renderLogin(state);
    bindEvents();
  }

  function onUnauthorized() {
    store.update("auth", {
      sessionToken: "",
      loginTone: "error",
      loginStatus: "Session expired, please login again",
    });
    setUiError("会话已过期，请重新登录");
    render();
  }

  function mount() {
    if (mounted) return;
    mounted = true;
    unsub = store.subscribe(() => render());
    window.addEventListener("unauthorized", onUnauthorized);

    refreshLocalSettings();
    render();

    if (store.getState().auth.sessionToken) {
      void Promise.allSettled([
        refreshOverviewStatus(),
        refreshSystemVersions(),
        refreshNetworkStatus(),
        refreshProbeManage(),
        refreshCloudflareConfig(),
        refreshTGData(),
      ]);
    }
  }

  function unmount() {
    if (!mounted) return;
    mounted = false;
    if (unsub) unsub();
    bus.clear();
    window.removeEventListener("unauthorized", onUnauthorized);
    root.innerHTML = "";
  }

  return {
    mount,
    unmount,
    store,
    bus,
  };
}

export { createAppShell };
