import { useEffect, useRef, useState } from "react";
import { fetchAdminStatus } from "../services/controller-api";
import { buildAdminStatusWSURL, normalizeBaseUrl } from "../utils/url";
import type { StatusWSMessage } from "../types";

type AdminStatusResponse = { status: string; uptime: number; server_time: string };

function isUnauthorizedError(error: unknown): boolean {
  if (!(error instanceof Error)) {
    return false;
  }
  const message = error.message.toLowerCase();
  return message.includes("401") || message.includes("invalid or expired session token");
}

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

    const wsURL = buildAdminStatusWSURL(baseUrl);
    if (!wsURL) {
      setWsStatus("WebSocket URL 无效（仅支持 HTTPS/WSS）");
      return;
    }

    setWsStatus("WebSocket 连接中...");
    const ws = new WebSocket(wsURL);
    wsConnRef.current = ws;

    ws.onopen = () => {
      setWsStatus("WebSocket 已连接，认证中...");
      ws.send(JSON.stringify({ id: "status-auth", action: "auth.session", payload: { token: sessionToken } }));
    };
    ws.onerror = () => setWsStatus("WebSocket 连接异常");
    ws.onclose = () => setWsStatus("WebSocket 已断开");

    ws.onmessage = (event) => {
      try {
        const data = JSON.parse(event.data as string) as StatusWSMessage;
        if ((data as { id?: string }).id === "status-auth") {
          if ((data as { ok?: boolean }).ok) {
            setWsStatus("WebSocket 已连接并认证");
          } else {
            setWsStatus("WebSocket 认证失败");
            ws.close();
          }
          return;
        }
        if ((data.type ?? "") !== "status") {
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

  async function pingServer(baseUrlInput: string, token: string) {
    const base = normalizeBaseUrl(baseUrlInput);
    if (!base) {
      setServerStatus("Controller URL is required");
      return;
    }
    if (!token) {
      setServerStatus("未登录，无法访问管理接口");
      return;
    }

    try {
      const data = await fetchAdminStatus(base, token);
      const uptime = typeof data.uptime === "number" ? `，运行 ${data.uptime}s` : "";
      const serverTime = data.server_time ? `，时间 ${data.server_time}` : "";
      setServerStatus(`主控在线：status=${data.status}${uptime}${serverTime}`);
    } catch (error) {
      const msg = error instanceof Error ? error.message : "unknown error";
      setServerStatus(`主控状态获取失败：${msg}`);
    }
  }

  async function checkAdminStatus(baseUrlInput: string, token: string, reauthenticate?: () => Promise<string>) {
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
      let activeToken = token;
      let reloginUsed = false;
      let data: AdminStatusResponse;
      try {
        data = await fetchAdminStatus(base, activeToken);
      } catch (error) {
        if (!reauthenticate || !isUnauthorizedError(error)) {
          throw error;
        }

        setAdminStatus("会话已过期，正在自动重新登录...");
        activeToken = await reauthenticate();
        reloginUsed = true;
        data = await fetchAdminStatus(base, activeToken);
      }

      setAdminStatus(`${reloginUsed ? "已自动重新登录，" : ""}管理接口正常：status=${data.status}, uptime=${data.uptime}s`);
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
