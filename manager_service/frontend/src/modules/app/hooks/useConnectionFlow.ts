/**
 * useConnectionFlow.ts — manager_service 健康连接状态管理
 *
 * 只轮询 manager_service 自身的健康检查，不直连主控 WebSocket。
 * C-FE-04 / PKG-FE-R02 / RQ-003
 */
import { useCallback, useEffect, useState } from "react";
import { fetchJson } from "../api";

export function useConnectionFlow(_baseUrl: string, sessionToken: string) {
  const [serverStatus, setServerStatus] = useState("");
  const [wsStatus] = useState(""); // WS 直连主控已移除 (RQ-003 / C-FE-04)
  const [adminStatus, setAdminStatus] = useState("");

  // 登录后自动检查 manager_service 健康状态
  useEffect(() => {
    if (!sessionToken) {
      setServerStatus("");
      setAdminStatus("");
      return;
    }
    checkHealth();
    // eslint-disable-next-line react-hooks/exhaustive-deps
  }, [sessionToken]);

  const checkHealth = useCallback(async () => {
    try {
      const data = await fetchJson<{ version?: string }>("/system/version");
      setServerStatus(`manager_service 在线，版本：${data?.version ?? "unknown"}`);
    } catch (e) {
      const msg = e instanceof Error ? e.message : "unknown";
      setServerStatus(`manager_service 状态异常：${msg}`);
    }
  }, []);

  async function pingServer(_baseUrlInput: string, _token: string) {
    await checkHealth();
  }

  async function checkAdminStatus(_baseUrlInput: string, _token: string) {
    // 主控直连已禁用。manager_service healthz 替代。
    try {
      await fetchJson<unknown>("/healthz");
      setAdminStatus("manager_service 健康检查正常");
    } catch {
      setAdminStatus("manager_service 健康检查失败");
    }
  }

  function clearStatusMessages() {
    setServerStatus("");
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
