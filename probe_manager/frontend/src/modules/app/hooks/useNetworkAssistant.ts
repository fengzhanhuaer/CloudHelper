import { useCallback, useEffect, useMemo, useRef, useState } from "react";
import {
  downloadNetworkRuleRoutes,
} from "../services/controller-api";
import {
	EnableNetworkAssistantTUN,
	GetNetworkAssistantLogs,
	GetNetworkAssistantRuleConfig,
	GetNetworkAssistantStatus,
	InstallNetworkAssistantTUN,
	RestoreNetworkAssistantDirect,
	SetNetworkAssistantMode,
	SetNetworkAssistantRulePolicy,
	SyncNetworkAssistant,
	QueryNetworkAssistantDNSCache,
	ListNetworkAssistantProcesses,
	StartNetworkAssistantProcessMonitor,
	StopNetworkAssistantProcessMonitor,
	QueryNetworkAssistantProcessEvents,
	ClearNetworkAssistantProcessEvents,
	AppendNetworkAssistantDebugLog,
} from "../../../../wailsjs/go/main/App";
import * as AppBindings from "../../../../wailsjs/go/main/App";
import type {
  NetworkAssistantDNSCacheEntry,
  NetworkAssistantLogEntry,
  NetworkAssistantLogFilterSource,
  NetworkAssistantLogResponse,
  NetworkAssistantLogSource,
  NetworkAssistantRuleAction,
  NetworkAssistantRuleConfig,
  NetworkAssistantMode,
  NetworkAssistantStatus,
  NetworkProcessInfo,
  NetworkProcessEvent,
} from "../types";

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
  const value = raw.trim().toLowerCase();
  return value || "general";
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
  const line = String(raw.line ?? "").trim();
  const normalized: NetworkAssistantLogEntry = {
    time,
    source,
    category,
    message,
    line: "",
  };
  normalized.line = line || buildLogLine(normalized);
  return normalized;
}

function parseLegacyLogEntries(content: string): NetworkAssistantLogEntry[] {
  const normalized = content.replace(/\r\n/g, "\n").trim();
  if (!normalized) {
    return [];
  }
  return normalized.split("\n").map((line) => {
    const trimmed = line.trim();
    return normalizeLogEntry({
      time: "",
      source: "manager",
      category: "general",
      message: trimmed,
      line: trimmed,
    });
  });
}


export function useNetworkAssistant() {
  const [status, setStatus] = useState<NetworkAssistantStatus>(defaultStatus);
  const [operateStatus, setOperateStatus] = useState("未操作");
  const [isOperating, setIsOperating] = useState(false);
  const [selectedNode, setSelectedNode] = useState(defaultStatus.node_id);
  const [logLines, setLogLines] = useState(200);
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

  const ruleConfigRequestSeqRef = useRef(0);

  const logCategories = useMemo(() => {
    const set = new Set<string>();
    for (const entry of logEntries) {
      set.add(entry.category || "general");
    }
    return Array.from(set).sort((left, right) => left.localeCompare(right));
  }, [logEntries]);

  const visibleLogEntries = useMemo(() => logEntries, [logEntries]);

  const visibleLogContent = useMemo(() => {
    return visibleLogEntries.map((entry) => entry.line || buildLogLine(entry)).join("\n");
  }, [visibleLogEntries]);

  const refreshLogs = useCallback(async () => {
    setIsLoadingLogs(true);
    setLogStatus("正在刷新网络助手日志...");
    try {
      const data = (await GetNetworkAssistantLogs(logLines)) as NetworkAssistantLogResponse;
      const entries = Array.isArray(data.entries) && data.entries.length > 0
        ? data.entries.map((entry) => normalizeLogEntry(entry))
        : parseLegacyLogEntries(data.content || "");
      setLogEntries(entries);
      setLogCopyStatus("");
      setLogStatus(`已加载网络助手日志（${entries.length} 条）`);
    } catch (error) {
      const msg = error instanceof Error ? error.message : "unknown error";
      setLogStatus(`网络助手日志加载失败：${msg}`);
    } finally {
      setIsLoadingLogs(false);
    }
  }, [logLines]);

  const copyLogs = useCallback(async () => {
    const text = visibleLogContent.trim();
    if (!text) {
      setLogCopyStatus("暂无日志可复制");
      return;
    }

    try {
      if (typeof navigator !== "undefined" && navigator.clipboard?.writeText) {
        await navigator.clipboard.writeText(visibleLogContent);
      } else if (typeof document !== "undefined") {
        const textarea = document.createElement("textarea");
        textarea.value = visibleLogContent;
        textarea.style.position = "fixed";
        textarea.style.opacity = "0";
        document.body.appendChild(textarea);
        textarea.focus();
        textarea.select();
        document.execCommand("copy");
        document.body.removeChild(textarea);
      } else {
        throw new Error("clipboard api unavailable");
      }
      setLogCopyStatus("已复制网络助手日志");
    } catch (error) {
      const msg = error instanceof Error ? error.message : "unknown error";
      setLogCopyStatus(`复制失败：${msg}`);
    }
  }, [visibleLogContent]);

  function updateLogLines(value: number) {
    if (!Number.isFinite(value)) {
      setLogLines(200);
      return;
    }
    const normalized = Math.trunc(value);
    if (normalized <= 0) {
      setLogLines(200);
      return;
    }
    if (normalized > 2000) {
      setLogLines(2000);
      return;
    }
    setLogLines(normalized);
  }

  function clearLogs() {
    setLogStatus("未加载网络助手日志");
    setLogEntries([]);
    setLogSourceFilter("all");
    setLogCategoryFilter("all");
    setLogCopyStatus("");
  }

  const refreshRuleConfig = useCallback(async () => {
    const requestID = ++ruleConfigRequestSeqRef.current;
    setIsLoadingRuleConfig(true);
    setRuleConfigStatus("正在加载规则策略...");
    try {
      const data = (await GetNetworkAssistantRuleConfig()) as NetworkAssistantRuleConfig;
      setRuleConfig(data);
      setRuleConfigStatus("规则策略已加载");
    } catch (error) {
      const msg = error instanceof Error ? error.message : "unknown error";
      void AppendNetworkAssistantDebugLog(
        "frontend-rule-config",
        `[error] ${JSON.stringify({ requestID, message: msg })}`,
      );
      setRuleConfigStatus(`规则策略加载失败：${msg}`);
    } finally {
      setIsLoadingRuleConfig(false);
    }
  }, []);

  const refreshStatus = useCallback(async (controllerBaseURL?: string, token?: string) => {
    try {
      const data = (controllerBaseURL && token
        ? (await SyncNetworkAssistant(controllerBaseURL, token))
        : (await GetNetworkAssistantStatus())) as NetworkAssistantStatus;
      setStatus(data);
      if (data.node_id) {
        setSelectedNode(data.node_id);
      }
      if (data.mode === "tun") {
        void refreshRuleConfig();
      }
    } catch (error) {
      const msg = error instanceof Error ? error.message : "unknown error";
      setOperateStatus(`状态刷新失败：${msg}`);
    }
  }, [refreshRuleConfig]);

  const setRulePolicy = useCallback(async (group: string, action: NetworkAssistantRuleAction, tunnelNodeID = "") => {
    setIsOperating(true);
    setRuleConfigStatus("正在更新规则策略...");
    try {
      const data = (await SetNetworkAssistantRulePolicy(group, action, tunnelNodeID.trim())) as NetworkAssistantRuleConfig;
      setRuleConfig(data);
      setRuleConfigStatus("规则策略已更新");
      setOperateStatus("规则策略已应用");
      void refreshLogs();
    } catch (error) {
      const msg = error instanceof Error ? error.message : "unknown error";
      setRuleConfigStatus(`规则策略更新失败：${msg}`);
      setOperateStatus(`规则策略更新失败：${msg}`);
      throw error;
    } finally {
      setIsOperating(false);
    }
  }, [refreshLogs]);

  const switchMode = useCallback(async (controllerBaseURL: string, token: string, mode: NetworkAssistantMode, nodeIdInput?: string) => {
    setIsOperating(true);
    const nodeID = (nodeIdInput ?? selectedNode).trim() || "direct";
    try {
      const data = (await SetNetworkAssistantMode(controllerBaseURL, token, mode, nodeID)) as NetworkAssistantStatus;
      setStatus(data);
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
      const msg = error instanceof Error ? error.message : "unknown error";
      setOperateStatus(`模式切换失败：${msg}`);
      void refreshLogs();
      throw error;
    } finally {
      setIsOperating(false);
    }
  }, [refreshLogs, refreshRuleConfig, selectedNode]);

  const installTUN = useCallback(async () => {
    setIsOperating(true);
    try {
      const data = (await InstallNetworkAssistantTUN()) as NetworkAssistantStatus;
      setStatus(data);
      if (data.node_id) {
        setSelectedNode(data.node_id);
      }
      setOperateStatus("TUN 安装完成");
      void refreshLogs();
    } catch (error) {
      const msg = error instanceof Error ? error.message : "unknown error";
      setOperateStatus(`TUN 安装失败：${msg}`);
      void refreshLogs();
      throw error;
    } finally {
      setIsOperating(false);
    }
  }, [refreshLogs]);

  const enableTUN = useCallback(async () => {
    setIsOperating(true);
    try {
      const data = (await EnableNetworkAssistantTUN()) as NetworkAssistantStatus;
      setStatus(data);
      if (data.node_id) {
        setSelectedNode(data.node_id);
      }
      setOperateStatus("TUN 模式已启用");
      void refreshLogs();
    } catch (error) {
      const msg = error instanceof Error ? error.message : "unknown error";
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

  const closeTUN = useCallback(async () => {
    setIsOperating(true);
    try {
      const data = (await RestoreNetworkAssistantDirect()) as NetworkAssistantStatus;
      setStatus(data);
      if (data.node_id) {
        setSelectedNode(data.node_id);
      }
      setOperateStatus("已关闭 TUN，并切回直连模式");
      void refreshLogs();
    } catch (error) {
      const msg = error instanceof Error ? error.message : "unknown error";
      setOperateStatus(`关闭 TUN 失败：${msg}`);
      void refreshLogs();
      throw error;
    } finally {
      setIsOperating(false);
    }
  }, [refreshLogs]);

  const uploadRuleRoutes = useCallback(async (controllerBaseURL: string, token: string) => {
    const uploadFn = (AppBindings as { UploadNetworkAssistantRuleRoutes?: (baseURL: string, sessionToken: string) => Promise<string> }).UploadNetworkAssistantRuleRoutes;
    if (!uploadFn) {
      setRuleRoutesSyncStatus("上传失败：当前版本未包含自动上传能力");
      throw new Error("UploadNetworkAssistantRuleRoutes is not available");
    }
    setIsSyncingRuleRoutes(true);
    setRuleRoutesSyncStatus("正在上传 rule_routes.txt 到主控备份...");
    try {
      const message = await uploadFn(controllerBaseURL, token);
      setRuleRoutesSyncStatus(`上传成功：${message}`);
      setRuleConfigStatus("规则策略已更新，请刷新规则组确认");
      await refreshRuleConfig();
      await refreshLogs();
    } catch (error) {
      const msg = error instanceof Error ? error.message : "unknown error";
      setRuleRoutesSyncStatus(`上传失败：${msg}`);
      throw error;
    } finally {
      setIsSyncingRuleRoutes(false);
    }
  }, [refreshLogs, refreshRuleConfig]);

  const downloadRuleRoutes = useCallback(async (controllerBaseURL: string, token: string) => {
    setIsSyncingRuleRoutes(true);
    setRuleRoutesSyncStatus("正在从主控备份下载 rule_routes.txt...");
    try {
      const { fileName, content } = await downloadNetworkRuleRoutes(controllerBaseURL, token);
      const blob = new Blob([content], { type: "text/plain;charset=utf-8" });
      const url = URL.createObjectURL(blob);
      const anchor = document.createElement("a");
      anchor.href = url;
      anchor.download = fileName || "rule_routes.txt";
      document.body.appendChild(anchor);
      anchor.click();
      anchor.remove();
      URL.revokeObjectURL(url);
      setRuleRoutesSyncStatus(`下载成功：${fileName || "rule_routes.txt"}`);
    } catch (error) {
      const msg = error instanceof Error ? error.message : "unknown error";
      setRuleRoutesSyncStatus(`下载失败：${msg}`);
      throw error;
    } finally {
      setIsSyncingRuleRoutes(false);
    }
  }, []);

  // DNS Cache 查询
  const [dnsCacheEntries, setDnsCacheEntries] = useState<NetworkAssistantDNSCacheEntry[]>([]);
  const [dnsCacheQuery, setDnsCacheQuery] = useState("");
  const [isDNSCacheLoading, setIsDNSCacheLoading] = useState(false);
  const [dnsCacheStatus, setDnsCacheStatus] = useState("");

  const queryDNSCache = useCallback(async (query: string) => {
    setIsDNSCacheLoading(true);
    setDnsCacheStatus("");
    try {
      const entries = await QueryNetworkAssistantDNSCache(query);
      setDnsCacheEntries(Array.isArray(entries) ? entries : []);
      setDnsCacheStatus("");
    } catch (error) {
      const msg = error instanceof Error ? error.message : String(error);
      setDnsCacheStatus(`查询失败：${msg}`);
      setDnsCacheEntries([]);
    } finally {
      setIsDNSCacheLoading(false);
    }
  }, []);

  // 进程监视
  const [processList, setProcessList] = useState<NetworkProcessInfo[]>([]);
  const [isLoadingProcesses, setIsLoadingProcesses] = useState(false);
  const [processListStatus, setProcessListStatus] = useState("");
  const [monitorProcessName, setMonitorProcessName] = useState("");
  const [isMonitoring, setIsMonitoring] = useState(false);
  const [processEvents, setProcessEvents] = useState<NetworkProcessEvent[]>([]);
  const [processEventsStatus, setProcessEventsStatus] = useState("");

  const refreshProcessList = useCallback(async () => {
    setIsLoadingProcesses(true);
    setProcessListStatus("");
    try {
      const list = await ListNetworkAssistantProcesses();
      setProcessList(Array.isArray(list) ? list : []);
    } catch (error) {
      const msg = error instanceof Error ? error.message : String(error);
      setProcessListStatus(`获取进程列表失败：${msg}`);
    } finally {
      setIsLoadingProcesses(false);
    }
  }, []);

  const startProcessMonitor = useCallback(async () => {
    try {
      await StartNetworkAssistantProcessMonitor();
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
      const msg = error instanceof Error ? error.message : String(error);
      setProcessEventsStatus(`启动监视失败：${msg}`);
    }
  }, [refreshProcessList, status.mode, status.tun_enabled]);

  const clearProcessEvents = useCallback(async () => {
    try {
      await ClearNetworkAssistantProcessEvents();
      setProcessEvents([]);
      setProcessEventsStatus("监视记录已清空");
    } catch (error) {
      const msg = error instanceof Error ? error.message : String(error);
      setProcessEventsStatus(`清空监视记录失败：${msg}`);
    }
  }, []);

  const stopProcessMonitor = useCallback(async () => {
    try {
      await StopNetworkAssistantProcessMonitor();
      setIsMonitoring(false);
    } catch (error) {
      const msg = error instanceof Error ? error.message : String(error);
      setProcessEventsStatus(`停止监视失败：${msg}`);
    }
  }, []);

  const pollProcessEvents = useCallback(async () => {
    if (!isMonitoring) return;
    try {
      const raw = await QueryNetworkAssistantProcessEvents(0);
      if (Array.isArray(raw)) {
        const events: NetworkProcessEvent[] = raw.map((e) => {
          const source = e as unknown as { process_name?: string; count?: number };
          return {
            kind: e.kind as NetworkProcessEvent["kind"],
            timestamp: e.timestamp,
            process_name: source.process_name,
            domain: e.domain,
            target_ip: e.target_ip,
            target_port: e.target_port,
            direct: e.direct,
            node_id: e.node_id,
            group: e.group,
            resolved_ips: e.resolved_ips,
            count: source.count,
          };
        });
        setProcessEvents(events);
        if (events.length > 0) {
          setProcessEventsStatus("");
        } else if (status.mode !== "tun") {
          setProcessEventsStatus("当前为直连模式，通常不会产生监视事件；请切换到 TUN 模式后再观察。");
        } else if (!status.tun_enabled) {
          setProcessEventsStatus("当前 TUN 尚未启用，暂无事件；请先启用 TUN 并产生网络流量。");
        } else {
          setProcessEventsStatus("暂无事件：请产生网络流量后观察。DNS/TCP/UDP 命中后会在此处展示。");
        }
      }
    } catch (error) {
      const msg = error instanceof Error ? error.message : String(error);
      setProcessEventsStatus(`监视轮询失败：${msg}`);
    }
  }, [isMonitoring, status.mode, status.tun_enabled]);

  // 监视轮询：每 2 秒刷新一次事件
  useEffect(() => {
    if (!isMonitoring) return;
    const timer = setInterval(pollProcessEvents, 2000);
    return () => clearInterval(timer);
  }, [isMonitoring, pollProcessEvents]);

  // 网络助手页定时刷新状态（包含分组保活），避免保活展示滞后。
  useEffect(() => {
    if (isOperating) {
      return;
    }
    const timer = setInterval(() => {
      void refreshStatus();
    }, 3000);
    return () => clearInterval(timer);
  }, [isOperating, refreshStatus]);

  const appendDebugLog = useCallback(async (category: string, message: string) => {
    try {
      await AppendNetworkAssistantDebugLog(category.trim(), message.trim());
    } catch (error) {
      const msg = error instanceof Error ? error.message : "unknown error";
      console.warn("[network-assistant][debug-log] append failed", { category, message, error: msg });
    }
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
