import { useEffect, useRef, useState } from "react";
import { fetchAdminStatus, fetchDashboardStatus } from "../services/controller-api";
import { buildAdminStatusWSURL, normalizeBaseUrl } from "../utils/url";
import type { StatusWSMessage } from "../types";

export function useConnectionFlow(baseUrl: string, sessionToken: string) {
  const [serverStatus, setServerStatus] = useState("");
  const [wsStatus, setWsStatus] = useState("");
  const [adminStatus, setAdminStatus] = useState("");
  const wsConnRef = useRef<WebSocket | null>(null);

  useEffect(() => {
    if (!sessionToken) {
      if (wsConnRef.current) {
        wsConnRef.current.close();
        wsConnRef.current = null;
      }
      setWsStatus("");
      return;
    }

    const wsURL = buildAdminStatusWSURL(baseUrl, sessionToken);
    if (!wsURL) {
      setWsStatus("WebSocket URL 无效");
      return;
    }

    setWsStatus("WebSocket 连接中...");
    const ws = new WebSocket(wsURL);
    wsConnRef.current = ws;

    ws.onopen = () => setWsStatus("WebSocket 已连接");
    ws.onerror = () => setWsStatus("WebSocket 连接异常");
    ws.onclose = () => setWsStatus("WebSocket 已断开");

    ws.onmessage = (event) => {
      try {
        const data = JSON.parse(event.data as string) as StatusWSMessage;
        if ((data.type ?? "status") !== "status") {
          return;
        }
        const uptime = typeof data.uptime === "number" ? `，运行 ${data.uptime}s` : "";
        const serverTime = data.server_time ? `，时间 ${data.server_time}` : "";
        setServerStatus(`主控在线：${data.message ?? "pong"} / ${data.service ?? "CloudHelper"}${uptime}${serverTime}`);
      } catch {
        // ignore malformed ws message
      }
    };

    return () => {
      ws.close();
      if (wsConnRef.current === ws) {
        wsConnRef.current = null;
      }
    };
  }, [baseUrl, sessionToken]);

  async function pingServer(baseUrlInput: string) {
    const base = normalizeBaseUrl(baseUrlInput);
    if (!base) {
      setServerStatus("Controller URL is required");
      return;
    }

    try {
      const data = await fetchDashboardStatus(base);
      const uptime = typeof data.uptime === "number" ? `，运行 ${data.uptime}s` : "";
      setServerStatus(`主控在线：${data.message} / ${data.service}${uptime}`);
    } catch (error) {
      const msg = error instanceof Error ? error.message : "unknown error";
      setServerStatus(`主控状态获取失败：${msg}`);
    }
  }

  async function checkAdminStatus(baseUrlInput: string, token: string) {
    const base = normalizeBaseUrl(baseUrlInput);
    if (!base) {
      setAdminStatus("Controller URL is required");
      return;
    }
    if (!token) {
      setAdminStatus("未登录，无法访问管理接口");
      return;
    }

    try {
      const data = await fetchAdminStatus(base, token);
      setAdminStatus(`管理接口正常：status=${data.status}, uptime=${data.uptime}s`);
    } catch (error) {
      const msg = error instanceof Error ? error.message : "unknown error";
      setAdminStatus(`管理接口异常：${msg}`);
    }
  }

  function clearStatusMessages() {
    setServerStatus("");
    setWsStatus("");
    setAdminStatus("");
  }

  return {
    serverStatus,
    wsStatus,
    adminStatus,
    pingServer,
    checkAdminStatus,
    clearStatusMessages,
  };
}
