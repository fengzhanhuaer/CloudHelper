const DEFAULT_BASE_URL = "http://127.0.0.1:15030";

function readStorage(key, fallback = "") {
  try {
    const value = window.localStorage.getItem(key);
    return value == null ? fallback : value;
  } catch {
    return fallback;
  }
}

function createInitialState() {
  return {
    ui: {
      activeTab: "overview",
      statusMessage: "",
      errorMessage: "",
    },
    auth: {
      sessionToken: readStorage("manager_session_token", ""),
      username: "admin",
      userRole: "viewer",
      certType: "viewer",
      isAuthenticating: false,
      loginTone: "info",
      loginStatus: "Please login",
    },
    settings: {
      baseUrl: readStorage("controller_base_url", DEFAULT_BASE_URL) || DEFAULT_BASE_URL,
      baseUrlStatus: "",
      isLoadingBaseUrl: false,
      isSavingBaseUrl: false,
      controllerIP: readStorage("controller_ip", ""),
      controllerIPStatus: "",
      isLoadingControllerIP: false,
      isSavingControllerIP: false,
      upgradeProject: readStorage("cloudhelper.manager.upgrade_project", "fengzhanhuaer/CloudHelper"),
      aiDebugListenEnabled: false,
      aiDebugListenStatus: "AI Debug not supported in web mode",
      isLoadingAIDebugListenEnabled: false,
      isSavingAIDebugListenEnabled: false,
    },
    connection: {
      wsStatus: "",
      serverStatus: "",
      adminStatus: "",
    },
    probeManage: {
      isLoading: false,
      status: "未加载探针节点",
      nodes: [],
      deletedNodes: [],
      selectedNodeNo: 0,
      selectedNodeStatus: null,
      nodeLogs: "",
      nodeLogsStatus: "",
    },
    upgrade: {
      managerVersion: "...",
      controllerVersion: "—",
      controllerLatestVersion: "—",
      versionStatus: "未检查版本",
      mergedUpgradeStatus: "未升级",
      mergedUpgradeMessages: [],
      controllerUpgradeProgress: { active: false, phase: "idle", percent: 0, message: "" },
      managerUpgradeProgress: { active: false, phase: "idle", percent: 0, message: "" },
      isUpgradingController: false,
      isUpgradingManager: false,
      isCheckingDirect: false,
      isCheckingProxy: false,
      directRelease: null,
      proxyRelease: null,
      backupEnabled: false,
      backupRcloneRemote: "",
      backupSettingsStatus: "未加载",
      isLoadingBackupSettings: false,
      isSavingBackupSettings: false,
      isTestingBackupSettings: false,
    },
    network: {
      status: {
        enabled: false,
        mode: "direct",
        node_id: "direct",
        available_nodes: ["direct"],
        socks5_listen: "127.0.0.1:10808",
        tunnel_route: "/api/ws/tunnel/direct",
        tunnel_status: "未启用",
        system_proxy_status: "未设置",
        last_error: "",
        mux_connected: false,
        mux_active_streams: 0,
        mux_reconnects: 0,
        mux_last_recv: "",
        mux_last_pong: "",
        group_keepalive: [],
        tun_supported: false,
        tun_installed: false,
        tun_enabled: false,
        tun_library_path: "",
        tun_status: "未安装",
      },
      isOperating: false,
      operateStatus: "未操作",
      selectedNode: "direct",
      ruleConfig: null,
      isLoadingRuleConfig: false,
      ruleConfigStatus: "规则策略未加载",
      isSyncingRuleRoutes: false,
      ruleRoutesSyncStatus: "规则文件主控备份：未执行",
      dnsCacheEntries: [],
      dnsCacheQuery: "",
      isDNSCacheLoading: false,
      dnsCacheStatus: "",
      processList: [],
      isLoadingProcesses: false,
      processListStatus: "",
      monitorProcessName: "",
      isMonitoring: false,
      processEvents: [],
      processEventsStatus: "",
      logLines: 200,
      isLoadingLogs: false,
      logStatus: "未加载网络助手日志",
      logCopyStatus: "",
      logContent: "",
      logSourceFilter: "all",
      logCategoryFilter: "all",
      logCategories: [],
      logVisibleCount: 0,
      logTotalCount: 0,
      logAutoScroll: true,
    },
    logViewer: {
      source: "local",
      lines: 200,
      sinceMinutes: 0,
      minLevel: "normal",
      autoScroll: true,
      isLoading: false,
      status: "未加载日志",
      copyStatus: "",
      logFilePath: "",
      content: "",
    },
    cloudflare: {
      apiKey: "",
      zoneName: "",
      records: [],
      status: "未加载 Cloudflare 配置",
      isLoading: false,
    },
    tg: {
      apiId: "",
      apiHash: "",
      accounts: [],
      schedules: [],
      status: "未加载 TG 配置",
      isLoading: false,
    },
  };
}

export function createStore() {
  let state = createInitialState();
  const listeners = new Set();

  function getState() {
    return state;
  }

  function setState(patch) {
    state = {
      ...state,
      ...patch,
    };
    for (const listener of listeners) {
      try {
        listener(state);
      } catch (error) {
        console.error("[store] listener failed", error);
      }
    }
  }

  function update(path, value) {
    const current = { ...state[path] };
    state = {
      ...state,
      [path]: {
        ...current,
        ...value,
      },
    };
    for (const listener of listeners) {
      try {
        listener(state);
      } catch (error) {
        console.error("[store] listener failed", error);
      }
    }
  }

  function subscribe(listener) {
    listeners.add(listener);
    return () => listeners.delete(listener);
  }

  function reset() {
    state = createInitialState();
    for (const listener of listeners) {
      try {
        listener(state);
      } catch (error) {
        console.error("[store] listener failed", error);
      }
    }
  }

  return {
    getState,
    setState,
    update,
    subscribe,
    reset,
  };
}
