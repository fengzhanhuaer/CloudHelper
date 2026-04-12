/**
 * useNetworkAssistant.ts — 网络助手状态管理
 *
 * 重构说明 (PKG-FE-R03 / RQ-003 / C-FE-01 / C-FE-04):
 * - 已实现后端接口: GET /api/network-assistant/status, POST /api/network-assistant/mode
 * - 未实现接口 (W4+): tun/install, tun/enable, rules, dns/cache, process monitor
 *   → 这些函数保留语义接口，调用时返回明确的"功能暂不可用"状态，不得静默忽略
 * - 禁止直连主控 WS-RPC (RQ-003)
 */
import { useCallback, useEffect, useMemo, useRef, useState } from "react";
import {
  apiGetNetworkAssistantStatus,
  apiSwitchNetworkAssistantMode,
  apiGetNetworkAssistantLogs,
  apiGetNetworkAssistantDNSCache,
  apiGetNetworkAssistantProcesses,
  apiStartNetworkMonitor,
  apiStopNetworkMonitor,
  apiClearNetworkMonitorEvents,
  apiGetNetworkMonitorEvents,
  apiInstallTUN,
  apiEnableTUN,
  apiRestoreDirect,
  apiGetNetworkRuleConfig,
  apiSetNetworkRulePolicy,
} from "../manager-api";
import type {
  NetworkAssistantDNSCacheEntry,
  NetworkAssistantLogEntry,
  NetworkAssistantLogFilterSource,
  NetworkAssistantLogSource,
  NetworkAssistantLogResponse,
  NetworkAssistantRuleAction,
  NetworkAssistantRuleConfig,
  NetworkAssistantMode,
  NetworkAssistantStatus,
  NetworkProcessInfo,
  NetworkProcessEvent,
} from "../types";

// ─── 常量与工具函数（保留功能语义，PKG-FE-R03 / C-FE-01）─────────────────────

const defaultStatus: NetworkAssistantStatus = {
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
};

function normalizeLogSource(raw: string): NetworkAssistantLogSource {
  const value = raw.trim().toLowerCase();
  return value === "controller" ? "controller" : "manager";
}

function normalizeLogCategory(raw: string): string {
  return raw.trim().toLowerCase() || "general";
}

function buildLogLine(entry: NetworkAssistantLogEntry): string {
  const ts = entry.time || new Date().toLocaleString();
  return `${ts} [${entry.source}/${entry.category}] ${entry.message}`;
}

function normalizeLogEntry(raw: Partial<NetworkAssistantLogEntry>): NetworkAssistantLogEntry {
  const source = normalizeLogSource(String(raw.source ?? "manager"));
  const category = normalizeLogCategory(String(raw.category ?? "general"));
  const message = String(raw.message ?? "").trim();
  const time = String(raw.time ?? "").trim();
  const normalized: NetworkAssistantLogEntry = { time, source, category, message, line: "" };
  normalized.line = String(raw.line ?? "").trim() || buildLogLine(normalized);
  return normalized;
}

function parseLegacyLogEntries(content: string): NetworkAssistantLogEntry[] {
  const normalized = content.replace(/\r\n/g, "\n").trim();
  if (!normalized) return [];
  return normalized.split("\n").map((line) => {
    const trimmed = line.trim();
    return normalizeLogEntry({ time: "", source: "manager", category: "general", message: trimmed, line: trimmed });
  });
}

/** 未实现的后端端点统一错误消息 */
function notImplementedError(feature: string): Error {
  return new Error(`[W4-PENDING] ${feature} 功能暂未在 manager_service 实现，请等待 W4 后端代理端点就绪`);
}

// ─── Hook ────────────────────────────────────────────────────────────────────

export function useNetworkAssistant() {
  const [status, setStatus] = useState<NetworkAssistantStatus>(defaultStatus);
  const [operateStatus, setOperateStatus] = useState("未操作");
  const [isOperating, setIsOperating] = useState(false);
  const [selectedNode, setSelectedNode] = useState(defaultStatus.node_id);
  const [logLines, setLogLinesState] = useState(200);
  const [isLoadingLogs, setIsLoadingLogs] = useState(false);
  const [logStatus, setLogStatus] = useState("未加载网络助手日志");
  const [logEntries, setLogEntries] = useState<NetworkAssistantLogEntry[]>([]);
  const [logSourceFilter, setLogSourceFilter] = useState<NetworkAssistantLogFilterSource>("all");
  const [logCategoryFilter, setLogCategoryFilter] = useState("all");
  const [logCopyStatus, setLogCopyStatus] = useState("");
  const [ruleConfig, setRuleConfig] = useState<NetworkAssistantRuleConfig | null>(null);
  const [isLoadingRuleConfig, setIsLoadingRuleConfig] = useState(false);
  const [ruleConfigStatus, setRuleConfigStatus] = useState("规则策略未加载");
  const [isSyncingRuleRoutes, setIsSyncingRuleRoutes] = useState(false);
  const [ruleRoutesSyncStatus, setRuleRoutesSyncStatus] = useState("规则文件主控备份：未执行");

  // DNS Cache
  const [dnsCacheEntries, setDnsCacheEntries] = useState<NetworkAssistantDNSCacheEntry[]>([]);
  const [dnsCacheQuery, setDnsCacheQuery] = useState("");
  const [isDNSCacheLoading, setIsDNSCacheLoading] = useState(false);
  const [dnsCacheStatus, setDnsCacheStatus] = useState("");

  // 进程监视
  const [processList, setProcessList] = useState<NetworkProcessInfo[]>([]);
  const [isLoadingProcesses, setIsLoadingProcesses] = useState(false);
  const [processListStatus, setProcessListStatus] = useState("");
  const [monitorProcessName, setMonitorProcessName] = useState("");
  const [isMonitoring, setIsMonitoring] = useState(false);
  const [processEvents, setProcessEvents] = useState<NetworkProcessEvent[]>([]);
  const [processEventsStatus, setProcessEventsStatus] = useState("");

  const ruleConfigRequestSeqRef = useRef(0);

  const logCategories = useMemo(() => {
    const set = new Set<string>();
    for (const entry of logEntries) set.add(entry.category || "general");
    return Array.from(set).sort((a, b) => a.localeCompare(b));
  }, [logEntries]);

  const visibleLogEntries = useMemo(() => logEntries, [logEntries]);
  const visibleLogContent = useMemo(
    () => visibleLogEntries.map((e) => e.line || buildLogLine(e)).join("\n"),
    [visibleLogEntries]
  );

  // ── 已实现 ──────────────────────────────────────────────────────────────────

  /** GET /api/network-assistant/status — 已由 manager_service 实现 */
  const refreshStatus = useCallback(async (_controllerBaseURL?: string, _token?: string) => {
    try {
      const raw = await apiGetNetworkAssistantStatus();
      // probe_manager 返回的状态结构与 NetworkAssistantStatus 字段对齐，做类型转换
      const data = raw as unknown as NetworkAssistantStatus;
      setStatus((prev) => ({ ...prev, ...data }));
      if (data.node_id) setSelectedNode(data.node_id);
      if (data.mode === "tun") void refreshRuleConfig();
    } catch (error) {
      const msg = error instanceof Error ? error.message : "unknown";
      setOperateStatus(`状态刷新失败：${msg}`);
    }
  // eslint-disable-next-line react-hooks/exhaustive-deps
  }, []);

  /** POST /api/network-assistant/mode — 已由 manager_service 实现 */
  const switchMode = useCallback(
    async (_controllerBaseURL: string, _token: string, mode: NetworkAssistantMode, nodeIdInput?: string) => {
      setIsOperating(true);
      const nodeID = (nodeIdInput ?? selectedNode).trim() || "direct";
      try {
        const raw = await apiSwitchNetworkAssistantMode(mode);
        const data = raw as unknown as NetworkAssistantStatus;
        setStatus((prev) => ({ ...prev, ...data }));
        setSelectedNode(data.node_id || nodeID);
        if (mode === "direct") {
          setOperateStatus("已切换为直连模式，并恢复系统 DNS/系统代理");
        } else if (mode === "tun") {
          setOperateStatus("已切换为 TUN 模式（按规则分流）");
          void refreshRuleConfig();
        } else {
          setOperateStatus(`模式已切换：${mode}`);
        }
        void refreshLogs();
      } catch (error) {
        const msg = error instanceof Error ? error.message : "unknown";
        setOperateStatus(`模式切换失败：${msg}`);
        void refreshLogs();
        throw error;
      } finally {
        setIsOperating(false);
      }
    },
    // eslint-disable-next-line react-hooks/exhaustive-deps
    [selectedNode]
  );

  // ── 待实现 (W4+) — 保留功能语义，显式报告不可用 ─────────────────────────────

  /** GET /api/network-assistant/logs — 已由 netassist 代理实现 */
  const refreshLogs = useCallback(async () => {
    setIsLoadingLogs(true);
    setLogStatus("正在刷新网络助手日志...");
    try {
      const raw = await apiGetNetworkAssistantLogs(logLines);
      const data = raw as { entries?: Partial<NetworkAssistantLogEntry>[]; content?: string };
      const entries = Array.isArray(data?.entries) && (data.entries as []).length > 0
        ? (data.entries as Partial<NetworkAssistantLogEntry>[]).map(normalizeLogEntry)
        : parseLegacyLogEntries(String(data?.content ?? ""));
      setLogEntries(entries);
      setLogCopyStatus("");
      setLogStatus(`已加载网络助手日志（${entries.length} 条）`);
    } catch (error) {
      const msg = error instanceof Error ? error.message : "unknown";
      setLogStatus(`网络助手日志加载失败：${msg}`);
    } finally {
      setIsLoadingLogs(false);
    }
  }, [logLines]);

  const copyLogs = useCallback(async () => {
    const text = visibleLogContent.trim();
    if (!text) { setLogCopyStatus("暂无日志可复制"); return; }
    try {
      if (navigator?.clipboard?.writeText) {
        await navigator.clipboard.writeText(visibleLogContent);
      } else {
        const ta = document.createElement("textarea");
        ta.value = visibleLogContent;
        ta.style.cssText = "position:fixed;opacity:0";
        document.body.appendChild(ta);
        ta.select();
        document.execCommand("copy");
        ta.remove();
      }
      setLogCopyStatus("已复制网络助手日志");
    } catch (error) {
      setLogCopyStatus(`复制失败：${error instanceof Error ? error.message : "unknown"}`);
    }
  }, [visibleLogContent]);

  function clearLogs() {
    setLogStatus("未加载网络助手日志");
    setLogEntries([]);
    setLogSourceFilter("all");
    setLogCategoryFilter("all");
    setLogCopyStatus("");
  }

  function updateLogLines(value: number) {
    const n = Math.trunc(value);
    setLogLinesState(!Number.isFinite(n) || n <= 0 ? 200 : n > 2000 ? 2000 : n);
  }

  /** GET /api/network-assistant/rules — 代理实现 */
  const refreshRuleConfig = useCallback(async () => {
    ++ruleConfigRequestSeqRef.current;
    setIsLoadingRuleConfig(true);
    setRuleConfigStatus("正在加载规则策略...");
    try {
      const data = await apiGetNetworkRuleConfig() as NetworkAssistantRuleConfig;
      setRuleConfig(data);
      setRuleConfigStatus("规则策略已加载");
    } catch (error) {
      const msg = error instanceof Error ? error.message : "unknown";
      setRuleConfigStatus(`规则策略加载失败：${msg}`);
    } finally {
      setIsLoadingRuleConfig(false);
    }
  }, []);

  /** POST /api/network-assistant/rules/policy — 代理实现 */
  const setRulePolicy = useCallback(
    async (group: string, action: NetworkAssistantRuleAction, tunnelNodeID = "") => {
      setIsOperating(true);
      setRuleConfigStatus("正在更新规则策略...");
      try {
        const data = await apiSetNetworkRulePolicy(group, action, tunnelNodeID) as NetworkAssistantRuleConfig;
        setRuleConfig(data);
        setRuleConfigStatus("规则策略已更新");
        setOperateStatus("规则策略已应用");
        void refreshLogs();
      } catch (error) {
        const msg = error instanceof Error ? error.message : "unknown";
        setRuleConfigStatus(`规则策略更新失败：${msg}`);
        setOperateStatus(`规则策略更新失败：${msg}`);
        throw error;
      } finally {
        setIsOperating(false);
      }
    },
    [refreshLogs]
  );

  /** POST /api/network-assistant/tun/install — 代理实现 */
  const installTUN = useCallback(async () => {
    setIsOperating(true);
    try {
      const data = await apiInstallTUN() as unknown as NetworkAssistantStatus;
      setStatus((prev) => ({ ...prev, ...data }));
      setOperateStatus("TUN 安装完成");
      void refreshLogs();
    } catch (error) {
      const msg = error instanceof Error ? error.message : "unknown";
      setOperateStatus(`TUN 安装失败：${msg}`);
      void refreshLogs();
      throw error;
    } finally {
      setIsOperating(false);
    }
  }, [refreshLogs]);

  /** POST /api/network-assistant/tun/enable — 代理实现 */
  const enableTUN = useCallback(async () => {
    setIsOperating(true);
    try {
      const data = await apiEnableTUN() as unknown as NetworkAssistantStatus;
      setStatus((prev) => ({ ...prev, ...data }));
      setOperateStatus("TUN 模式已启用");
      void refreshLogs();
    } catch (error) {
      const msg = error instanceof Error ? error.message : "unknown";
      if (msg.includes("relaunch as admin")) {
        setOperateStatus("正在请求管理员权限，请在 UAC 弹窗中确认后重新启动应用");
      } else {
        setOperateStatus(`启用 TUN 失败：${msg}`);
      }
      void refreshLogs();
      throw error;
    } finally {
      setIsOperating(false);
    }
  }, [refreshLogs]);

  /** POST /api/network-assistant/direct/restore — 代理实现 */
  const closeTUN = useCallback(async () => {
    setIsOperating(true);
    try {
      const data = await apiRestoreDirect() as unknown as NetworkAssistantStatus;
      setStatus((prev) => ({ ...prev, ...data }));
      setOperateStatus("已关闭 TUN，并切回直连模式");
      void refreshLogs();
    } catch (error) {
      const msg = error instanceof Error ? error.message : "unknown";
      setOperateStatus(`关闭 TUN 失败：${msg}`);
      void refreshLogs();
      throw error;
    } finally {
      setIsOperating(false);
    }
  }, [refreshLogs]);

  /** @deprecated [W4-pending] rule_routes 备份上传 (需主控代理) */
  const uploadRuleRoutes = useCallback(async (_controllerBaseURL: string, _token: string) => {
    setIsSyncingRuleRoutes(true);
    try {
      setRuleRoutesSyncStatus("[W4-PENDING] 规则文件上传：需主控备份代理端点，请等待 W4 实现");
    } finally {
      setIsSyncingRuleRoutes(false);
    }
  }, []);

  /** @deprecated [W4-pending] rule_routes 备份下载 (需主控代理) */
  const downloadRuleRoutes = useCallback(async (_controllerBaseURL: string, _token: string) => {
    setIsSyncingRuleRoutes(true);
    try {
      setRuleRoutesSyncStatus("[W4-PENDING] 规则文件下载：需主控备份代理端点，请等待 W4 实现");
    } finally {
      setIsSyncingRuleRoutes(false);
    }
  }, []);

  /** GET /api/network-assistant/dns/cache — 代理实现 */
  const queryDNSCache = useCallback(async (query: string) => {
    setIsDNSCacheLoading(true);
    setDnsCacheStatus("");
    try {
      const entries = await apiGetNetworkAssistantDNSCache(query);
      setDnsCacheEntries(Array.isArray(entries) ? entries as NetworkAssistantDNSCacheEntry[] : []);
    } catch (error) {
      setDnsCacheStatus(`查询失败：${error instanceof Error ? error.message : "unknown"}`);
      setDnsCacheEntries([]);
    } finally {
      setIsDNSCacheLoading(false);
    }
  }, []);

  /** GET /api/network-assistant/processes — 代理实现 */
  const refreshProcessList = useCallback(async () => {
    setIsLoadingProcesses(true);
    try {
      const list = await apiGetNetworkAssistantProcesses();
      setProcessList(Array.isArray(list) ? list as NetworkProcessInfo[] : []);
    } catch (error) {
      setProcessListStatus(`获取失败：${error instanceof Error ? error.message : "unknown"}`);
    } finally {
      setIsLoadingProcesses(false);
    }
  }, []);

  /** POST /api/network-assistant/monitor/start — 代理实现 */
  const startProcessMonitor = useCallback(async () => {
    try {
      await apiStartNetworkMonitor();
      await refreshProcessList();
      setMonitorProcessName("");
      setIsMonitoring(true);
      setProcessEvents([]);
      if (status.mode !== "tun") {
        setProcessEventsStatus("监视已启动：当前为直连模式，通常不会产生监视事件；请切换到 TUN 模式后再观察。");
      } else if (!status.tun_enabled) {
        setProcessEventsStatus("监视已启动：当前 TUN 尚未启用，暂无事件；请先启用 TUN 并产生网络流量。");
      } else {
        setProcessEventsStatus("监视已启动：请产生网络流量，事件会每 2 秒刷新。");
      }
    } catch (error) {
      const msg = error instanceof Error ? error.message : "unknown";
      setProcessEventsStatus(`启动监视失败：${msg}`);
    }
  }, [refreshProcessList, status.mode, status.tun_enabled]);

  /** POST /api/network-assistant/monitor/stop — 代理实现 */
  const stopProcessMonitor = useCallback(async () => {
    try {
      await apiStopNetworkMonitor();
    } catch { /* ignore */ }
    setIsMonitoring(false);
  }, []);

  /** POST /api/network-assistant/monitor/clear — 代理实现 */
  const clearProcessEvents = useCallback(async () => {
    try {
      await apiClearNetworkMonitorEvents();
    } catch { /* ignore */ }
    setProcessEvents([]);
    setProcessEventsStatus("监视记录已清空");
  }, []);

  // ── 进程事件轮询 ────────────────────────────────────────────────────────────
  const pollProcessEvents = useCallback(async () => {
    if (!isMonitoring) return;
    try {
      const raw = await apiGetNetworkMonitorEvents(0);
      if (Array.isArray(raw)) {
        const events = raw as NetworkProcessEvent[];
        setProcessEvents(events);
        if (events.length > 0) {
          setProcessEventsStatus("");
        } else if (status.mode !== "tun") {
          setProcessEventsStatus("当前为直连模式，通常不会产生监视事件；请切换到 TUN 模式后再观察。");
        } else {
          setProcessEventsStatus("暂无事件：请产生网络流量后观察。");
        }
      }
    } catch (error) {
      setProcessEventsStatus(`监视轮询失败：${error instanceof Error ? error.message : "unknown"}`);
    }
  }, [isMonitoring, status.mode]);

  useEffect(() => {
    if (!isMonitoring) return;
    const timer = setInterval(() => void pollProcessEvents(), 2000);
    return () => clearInterval(timer);
  }, [isMonitoring, pollProcessEvents]);

  // ── 定时刷新状态 ────────────────────────────────────────────────────────────
  useEffect(() => {
    if (isOperating) return;
    const timer = setInterval(() => void refreshStatus(), 3000);
    return () => clearInterval(timer);
  }, [isOperating, refreshStatus]);

  const appendDebugLog = useCallback(async (_category: string, _message: string) => {
    // noop — debug logging not supported in web mode
  }, []);

  return {
    status,
    selectedNode,
    setSelectedNode,
    isOperating,
    operateStatus,
    refreshStatus,
    switchMode,
    installTUN,
    enableTUN,
    closeTUN,
    logLines,
    setLogLines: updateLogLines,
    isLoadingLogs,
    logStatus,
    logContent: visibleLogContent,
    logSourceFilter,
    setLogSourceFilter,
    logCategoryFilter,
    setLogCategoryFilter,
    logCategories,
    logVisibleCount: visibleLogEntries.length,
    logTotalCount: logEntries.length,
    logCopyStatus,
    ruleConfig,
    isLoadingRuleConfig,
    ruleConfigStatus,
    isSyncingRuleRoutes,
    ruleRoutesSyncStatus,
    appendDebugLog,
    refreshRuleConfig,
    setRulePolicy,
    uploadRuleRoutes,
    downloadRuleRoutes,
    refreshLogs,
    copyLogs,
    clearLogs,
    dnsCacheEntries,
    dnsCacheQuery,
    setDnsCacheQuery,
    isDNSCacheLoading,
    dnsCacheStatus,
    queryDNSCache,
    processList,
    isLoadingProcesses,
    processListStatus,
    refreshProcessList,
    monitorProcessName,
    setMonitorProcessName,
    isMonitoring,
    startProcessMonitor,
    stopProcessMonitor,
    clearProcessEvents,
    processEvents,
    processEventsStatus,
  };
}
