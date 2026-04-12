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
import { apiGetNetworkAssistantStatus, apiSwitchNetworkAssistantMode } from "../manager-api";
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

  /** @w4-pending GET /api/network-assistant/logs */
  const refreshLogs = useCallback(async () => {
    setIsLoadingLogs(true);
    setLogStatus("正在刷新网络助手日志...");
    try {
      // TODO(W4): 后端代理 /api/network-assistant/logs 端点未实现
      // 当 probe_manager 提供日志代理时，替换为 apiGetNetworkAssistantLogs()
      throw notImplementedError("网络助手日志");
    } catch (error) {
      const msg = error instanceof Error ? error.message : "unknown";
      setLogStatus(`网络助手日志：${msg}`);
    } finally {
      setIsLoadingLogs(false);
    }
  }, []);

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

  /** @w4-pending GET /api/network-assistant/rules */
  const refreshRuleConfig = useCallback(async () => {
    ++ruleConfigRequestSeqRef.current;
    setIsLoadingRuleConfig(true);
    setRuleConfigStatus("正在加载规则策略...");
    try {
      throw notImplementedError("规则策略");
    } catch (error) {
      setRuleConfigStatus(`规则策略：${error instanceof Error ? error.message : "unknown"}`);
    } finally {
      setIsLoadingRuleConfig(false);
    }
  }, []);

  /** @w4-pending POST /api/network-assistant/rules/policy */
  const setRulePolicy = useCallback(
    async (_group: string, _action: NetworkAssistantRuleAction, _tunnelNodeID = "") => {
      setIsOperating(true);
      try {
        throw notImplementedError("规则策略更新");
      } catch (error) {
        const msg = error instanceof Error ? error.message : "unknown";
        setRuleConfigStatus(`规则策略更新：${msg}`);
        setOperateStatus(`规则策略更新：${msg}`);
        throw error;
      } finally {
        setIsOperating(false);
      }
    },
    []
  );

  /** @w4-pending POST /api/network-assistant/tun/install */
  const installTUN = useCallback(async () => {
    setIsOperating(true);
    try {
      throw notImplementedError("TUN 安装");
    } catch (error) {
      setOperateStatus(`TUN 安装：${error instanceof Error ? error.message : "unknown"}`);
      throw error;
    } finally {
      setIsOperating(false);
    }
  }, []);

  /** @w4-pending POST /api/network-assistant/tun/enable */
  const enableTUN = useCallback(async () => {
    setIsOperating(true);
    try {
      throw notImplementedError("TUN 启用");
    } catch (error) {
      setOperateStatus(`TUN 启用：${error instanceof Error ? error.message : "unknown"}`);
      throw error;
    } finally {
      setIsOperating(false);
    }
  }, []);

  /** @w4-pending POST /api/network-assistant/direct/restore */
  const closeTUN = useCallback(async () => {
    setIsOperating(true);
    try {
      throw notImplementedError("关闭 TUN");
    } catch (error) {
      setOperateStatus(`关闭 TUN：${error instanceof Error ? error.message : "unknown"}`);
      throw error;
    } finally {
      setIsOperating(false);
    }
  }, []);

  /** @w4-pending rule_routes 上传 */
  const uploadRuleRoutes = useCallback(async (_controllerBaseURL: string, _token: string) => {
    setIsSyncingRuleRoutes(true);
    try {
      throw notImplementedError("规则文件上传");
    } catch (error) {
      setRuleRoutesSyncStatus(`上传失败：${error instanceof Error ? error.message : "unknown"}`);
      throw error;
    } finally {
      setIsSyncingRuleRoutes(false);
    }
  }, []);

  /** @w4-pending rule_routes 下载 */
  const downloadRuleRoutes = useCallback(async (_controllerBaseURL: string, _token: string) => {
    setIsSyncingRuleRoutes(true);
    try {
      throw notImplementedError("规则文件下载");
    } catch (error) {
      setRuleRoutesSyncStatus(`下载失败：${error instanceof Error ? error.message : "unknown"}`);
      throw error;
    } finally {
      setIsSyncingRuleRoutes(false);
    }
  }, []);

  /** @w4-pending GET /api/network-assistant/dns/cache */
  const queryDNSCache = useCallback(async (_query: string) => {
    setIsDNSCacheLoading(true);
    setDnsCacheStatus("");
    try {
      throw notImplementedError("DNS 缓存查询");
    } catch (error) {
      setDnsCacheStatus(`查询失败：${error instanceof Error ? error.message : "unknown"}`);
      setDnsCacheEntries([]);
    } finally {
      setIsDNSCacheLoading(false);
    }
  }, []);

  /** @w4-pending GET /api/network-assistant/processes */
  const refreshProcessList = useCallback(async () => {
    setIsLoadingProcesses(true);
    try {
      throw notImplementedError("进程列表");
    } catch (error) {
      setProcessListStatus(`获取失败：${error instanceof Error ? error.message : "unknown"}`);
    } finally {
      setIsLoadingProcesses(false);
    }
  }, []);

  const startProcessMonitor = useCallback(async () => {
    setProcessEventsStatus("[W4-PENDING] 进程监视功能暂未在 manager_service 实现");
  }, []);

  const stopProcessMonitor = useCallback(async () => {
    setIsMonitoring(false);
  }, []);

  const clearProcessEvents = useCallback(async () => {
    setProcessEvents([]);
    setProcessEventsStatus("监视记录已清空");
  }, []);

  const appendDebugLog = useCallback(async (_category: string, _message: string) => {
    // noop
  }, []);

  // ── 定时刷新状态 ────────────────────────────────────────────────────────────
  useEffect(() => {
    if (isOperating) return;
    const timer = setInterval(() => void refreshStatus(), 3000);
    return () => clearInterval(timer);
  }, [isOperating, refreshStatus]);

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
