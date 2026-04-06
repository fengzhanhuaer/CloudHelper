import { useEffect, useState } from "react";
import {
  DEFAULT_UPGRADE_PROJECT,
  STORAGE_UPGRADE_PROJECT,
} from "../constants";
import {
  GetAIDebugListenEnabled,
  GetGlobalControllerIP,
  GetGlobalControllerURL,
  SetAIDebugListenEnabled,
  SetGlobalControllerIP,
  SetGlobalControllerURL,
} from "../../../../wailsjs/go/main/App";

export function useLocalSettings() {
  const [baseUrl, setBaseUrl] = useState("http://127.0.0.1:15030");
  const [controllerIP, setControllerIPState] = useState("");
  const [upgradeProject, setUpgradeProject] = useState(DEFAULT_UPGRADE_PROJECT);
  const [controllerLoaded, setControllerLoaded] = useState(false);
  const [isLoadingControllerIP, setIsLoadingControllerIP] = useState(false);
  const [isSavingControllerIP, setIsSavingControllerIP] = useState(false);
  const [controllerIPStatus, setControllerIPStatus] = useState("controller_ip：未读取");
  const [aiDebugListenEnabled, setAIDebugListenEnabledState] = useState(false);
  const [isLoadingAIDebugListenEnabled, setIsLoadingAIDebugListenEnabled] = useState(false);
  const [isSavingAIDebugListenEnabled, setIsSavingAIDebugListenEnabled] = useState(false);
  const [aiDebugListenStatus, setAIDebugListenStatus] = useState("AI 调试入口：未读取");

  useEffect(() => {
    let cancelled = false;

    void (async () => {
      try {
        const savedBaseURL = await GetGlobalControllerURL();
        if (!cancelled && savedBaseURL?.trim()) {
          setBaseUrl(savedBaseURL.trim());
        }
      } catch {
        // ignore backend errors and keep default value
      }

      try {
        const savedControllerIP = await GetGlobalControllerIP();
        if (!cancelled) {
          const next = String(savedControllerIP || "").trim();
          setControllerIPState(next);
          setControllerIPStatus(
            next
              ? `controller_ip：${next}（已配置，连接主控时将直接按 IP 拨号并跳过 DNS）`
              : "controller_ip：空（未配置）",
          );
        }
      } catch (error) {
        if (!cancelled) {
          const message = error instanceof Error ? error.message : String(error);
          setControllerIPStatus(`controller_ip 读取失败：${message}`);
        }
      } finally {
        if (!cancelled) {
          setControllerLoaded(true);
        }
      }
    })();

    return () => {
      cancelled = true;
    };
  }, []);

  useEffect(() => {
    if (!controllerLoaded) {
      return;
    }

    void SetGlobalControllerURL(baseUrl).catch(() => {
      // ignore backend errors
    });
  }, [baseUrl, controllerLoaded]);

  useEffect(() => {
    try {
      const savedProject = window.localStorage.getItem(STORAGE_UPGRADE_PROJECT);
      if (savedProject?.trim()) {
        setUpgradeProject(savedProject.trim());
      }
    } catch {
      // ignore localStorage errors in restricted environments
    }
  }, []);

  useEffect(() => {
    try {
      window.localStorage.setItem(STORAGE_UPGRADE_PROJECT, upgradeProject);
    } catch {
      // ignore localStorage errors
    }
  }, [upgradeProject]);

  async function refreshControllerIP() {
    setIsLoadingControllerIP(true);
    setControllerIPStatus("controller_ip 读取中...");
    try {
      const saved = await GetGlobalControllerIP();
      const next = String(saved || "").trim();
      setControllerIPState(next);
      setControllerIPStatus(
        next
          ? `controller_ip：${next}（已配置，连接主控时将直接按 IP 拨号并跳过 DNS）`
          : "controller_ip：空（未配置）",
      );
      return next;
    } catch (error) {
      const message = error instanceof Error ? error.message : String(error);
      setControllerIPStatus(`controller_ip 读取失败：${message}`);
      throw error;
    } finally {
      setIsLoadingControllerIP(false);
    }
  }

  async function saveControllerIP(value: string) {
    const next = value.trim();
    setIsSavingControllerIP(true);
    setControllerIPStatus(next ? `controller_ip 保存中：${next}` : "controller_ip 清空中...");
    try {
      const saved = await SetGlobalControllerIP(next);
      const normalized = String(saved || "").trim();
      setControllerIPState(normalized);
      setControllerIPStatus(
        normalized
          ? `controller_ip 已保存：${normalized}（连接主控时将直接按 IP 拨号并跳过 DNS）`
          : "controller_ip 已清空（未配置）",
      );
      return normalized;
    } catch (error) {
      const message = error instanceof Error ? error.message : String(error);
      setControllerIPStatus(`controller_ip 保存失败：${message}`);
      throw error;
    } finally {
      setIsSavingControllerIP(false);
    }
  }

  async function refreshAIDebugListenEnabled() {
    setIsLoadingAIDebugListenEnabled(true);
    setAIDebugListenStatus("AI 调试入口状态读取中...");
    try {
      const enabled = await GetAIDebugListenEnabled();
      setAIDebugListenEnabledState(Boolean(enabled));
      setAIDebugListenStatus(`AI 调试入口已${enabled ? "启用" : "关闭"}（0.0.0.0:16031）`);
    } catch (error) {
      const message = error instanceof Error ? error.message : String(error);
      setAIDebugListenStatus(`AI 调试入口状态读取失败：${message}`);
    } finally {
      setIsLoadingAIDebugListenEnabled(false);
    }
  }

  async function setAIDebugListenEnabled(enabled: boolean) {
    setIsSavingAIDebugListenEnabled(true);
    setAIDebugListenStatus(`AI 调试入口正在${enabled ? "启用" : "关闭"}...`);
    try {
      const saved = await SetAIDebugListenEnabled(enabled);
      setAIDebugListenEnabledState(Boolean(saved));
      setAIDebugListenStatus(`AI 调试入口已${saved ? "启用" : "关闭"}（0.0.0.0:16031）`);
    } catch (error) {
      const message = error instanceof Error ? error.message : String(error);
      setAIDebugListenStatus(`AI 调试入口保存失败：${message}`);
    } finally {
      setIsSavingAIDebugListenEnabled(false);
    }
  }

  useEffect(() => {
    void refreshAIDebugListenEnabled();
  }, []);

  return {
    baseUrl,
    setBaseUrl,
    controllerIP,
    controllerIPStatus,
    isLoadingControllerIP,
    isSavingControllerIP,
    refreshControllerIP,
    saveControllerIP,
    upgradeProject,
    setUpgradeProject,
    aiDebugListenEnabled,
    aiDebugListenStatus,
    isLoadingAIDebugListenEnabled,
    isSavingAIDebugListenEnabled,
    refreshAIDebugListenEnabled,
    setAIDebugListenEnabled,
  };
}
