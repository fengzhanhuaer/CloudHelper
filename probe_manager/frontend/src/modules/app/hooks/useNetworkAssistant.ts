import { useCallback, useState } from "react";
import { GetNetworkAssistantLogs, GetNetworkAssistantStatus, RestoreNetworkAssistantDirect, SetNetworkAssistantMode, SyncNetworkAssistant } from "../../../../wailsjs/go/main/App";
import type { NetworkAssistantLogResponse, NetworkAssistantMode, NetworkAssistantStatus } from "../types";

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
};

export function useNetworkAssistant() {
  const [status, setStatus] = useState<NetworkAssistantStatus>(defaultStatus);
  const [operateStatus, setOperateStatus] = useState("未操作");
  const [isOperating, setIsOperating] = useState(false);
  const [selectedNode, setSelectedNode] = useState(defaultStatus.node_id);
  const [logLines, setLogLines] = useState(200);
  const [isLoadingLogs, setIsLoadingLogs] = useState(false);
  const [logStatus, setLogStatus] = useState("未加载网络助手日志");
  const [logContent, setLogContent] = useState("");
  const [logCopyStatus, setLogCopyStatus] = useState("");

  const refreshLogs = useCallback(async () => {
    setIsLoadingLogs(true);
    setLogStatus("正在刷新网络助手日志...");
    try {
      const data = (await GetNetworkAssistantLogs(logLines)) as NetworkAssistantLogResponse;
      setLogContent(data.content || "");
      setLogCopyStatus("");
      setLogStatus(`已加载网络助手日志（${data.lines} 行）`);
    } catch (error) {
      const msg = error instanceof Error ? error.message : "unknown error";
      setLogStatus(`网络助手日志加载失败：${msg}`);
    } finally {
      setIsLoadingLogs(false);
    }
  }, [logLines]);

  const copyLogs = useCallback(async () => {
    const text = logContent.trim();
    if (!text) {
      setLogCopyStatus("暂无日志可复制");
      return;
    }

    try {
      if (typeof navigator !== "undefined" && navigator.clipboard?.writeText) {
        await navigator.clipboard.writeText(logContent);
      } else if (typeof document !== "undefined") {
        const textarea = document.createElement("textarea");
        textarea.value = logContent;
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
  }, [logContent]);

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
    setLogContent("");
    setLogCopyStatus("");
  }

  const refreshStatus = useCallback(async (controllerBaseURL?: string, token?: string) => {
    try {
      const data = (controllerBaseURL && token
        ? (await SyncNetworkAssistant(controllerBaseURL, token))
        : (await GetNetworkAssistantStatus())) as NetworkAssistantStatus;
      setStatus(data);
      if (data.node_id) {
        setSelectedNode(data.node_id);
      }
    } catch (error) {
      const msg = error instanceof Error ? error.message : "unknown error";
      setOperateStatus(`状态刷新失败：${msg}`);
    }
  }, []);

  const switchMode = useCallback(async (controllerBaseURL: string, token: string, mode: NetworkAssistantMode, nodeIdInput?: string) => {
    setIsOperating(true);
    const nodeID = (nodeIdInput ?? selectedNode).trim() || "cloudserver";
    try {
      const data = (await SetNetworkAssistantMode(controllerBaseURL, token, mode, nodeID)) as NetworkAssistantStatus;
      setStatus(data);
      setSelectedNode(data.node_id || nodeID);
      setOperateStatus(`模式已切换：${mode}`);
    } catch (error) {
      const msg = error instanceof Error ? error.message : "unknown error";
      setOperateStatus(`模式切换失败：${msg}`);
      throw error;
    } finally {
      setIsOperating(false);
    }
  }, [selectedNode]);

  const restoreDirect = useCallback(async () => {
    setIsOperating(true);
    try {
      const data = (await RestoreNetworkAssistantDirect()) as NetworkAssistantStatus;
      setStatus(data);
      if (data.node_id) {
        setSelectedNode(data.node_id);
      }
      setOperateStatus("已恢复为直连模式");
    } catch (error) {
      const msg = error instanceof Error ? error.message : "unknown error";
      setOperateStatus(`恢复直连失败：${msg}`);
      throw error;
    } finally {
      setIsOperating(false);
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
    restoreDirect,
    logLines,
    setLogLines: updateLogLines,
    isLoadingLogs,
    logStatus,
    logContent,
    logCopyStatus,
    refreshLogs,
    copyLogs,
    clearLogs,
  };
}
