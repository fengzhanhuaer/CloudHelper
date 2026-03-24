import { useCallback, useEffect, useMemo, useState } from "react";
import {
  EnableNetworkAssistantTUN,
  GetNetworkAssistantLogs,
  GetNetworkAssistantRuleConfig,
  GetNetworkAssistantStatus,
  InstallNetworkAssistantTUN,
  SetNetworkAssistantMode,
  SetNetworkAssistantRulePolicy,
  SyncNetworkAssistant,
} from "../../../../wailsjs/go/main/App";
import type {
  NetworkAssistantLogEntry,
  NetworkAssistantLogFilterSource,
  NetworkAssistantLogResponse,
  NetworkAssistantLogSource,
  NetworkAssistantRuleAction,
  NetworkAssistantRuleConfig,
  NetworkAssistantMode,
  NetworkAssistantStatus,
} from "../types";

const defaultStatus: NetworkAssistantStatus = {
  enabled: false,
  mode: "direct",
  node_id: "cloudserver",
  available_nodes: ["cloudserver"],
  socks5_listen: "127.0.0.1:10808",
  tunnel_route: "/api/ws/tunnel/cloudserver",
  tunnel_status: "未启用",
  system_proxy_status: "未设置",
  last_error: "",
  mux_connected: false,
  mux_active_streams: 0,
  mux_reconnects: 0,
  mux_last_recv: "",
  mux_last_pong: "",
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

  const logCategories = useMemo(() => {
    const set = new Set<string>();
    for (const entry of logEntries) {
      if (logSourceFilter !== "all" && entry.source !== logSourceFilter) {
        continue;
      }
      set.add(entry.category || "general");
    }
    return Array.from(set).sort((left, right) => left.localeCompare(right));
  }, [logEntries, logSourceFilter]);

  useEffect(() => {
    if (logCategoryFilter === "all") {
      return;
    }
    if (!logCategories.includes(logCategoryFilter)) {
      setLogCategoryFilter("all");
    }
  }, [logCategories, logCategoryFilter]);

  const visibleLogEntries = useMemo(() => {
    return logEntries.filter((entry) => {
      if (logSourceFilter !== "all" && entry.source !== logSourceFilter) {
        return false;
      }
      if (logCategoryFilter !== "all" && entry.category !== logCategoryFilter) {
        return false;
      }
      return true;
    });
  }, [logCategoryFilter, logEntries, logSourceFilter]);

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
    setIsLoadingRuleConfig(true);
    setRuleConfigStatus("正在加载规则策略...");
    try {
      const data = (await GetNetworkAssistantRuleConfig()) as NetworkAssistantRuleConfig;
      setRuleConfig(data);
      const groupCount = (data.groups?.length || 0) + 1;
      setRuleConfigStatus(`规则策略已加载（${groupCount} 个组）`);
    } catch (error) {
      const msg = error instanceof Error ? error.message : "unknown error";
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
      if (data.mode === "rule") {
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
    const nodeID = (nodeIdInput ?? selectedNode).trim() || "cloudserver";
    try {
      const data = (await SetNetworkAssistantMode(controllerBaseURL, token, mode, nodeID)) as NetworkAssistantStatus;
      setStatus(data);
      setSelectedNode(data.node_id || nodeID);
      if (mode === "direct") {
        setOperateStatus("已切换为直连模式，并清除系统代理");
      } else if (mode === "rule") {
        setOperateStatus("已切换为规则模式（命中规则走链路）");
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
      setOperateStatus(`启用 TUN 失败：${msg}`);
      void refreshLogs();
      throw error;
    } finally {
      setIsOperating(false);
    }
  }, [refreshLogs]);

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
    refreshRuleConfig,
    setRulePolicy,
    refreshLogs,
    copyLogs,
    clearLogs,
  };
}
