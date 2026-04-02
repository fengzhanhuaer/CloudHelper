import { useEffect, useState } from "react";
import {
  DEFAULT_UPGRADE_PROJECT,
  STORAGE_UPGRADE_PROJECT,
} from "../constants";
import {
  GetAIDebugListenEnabled,
  GetGlobalControllerURL,
  SetAIDebugListenEnabled,
  SetGlobalControllerURL,
} from "../../../../wailsjs/go/main/App";

export function useLocalSettings() {
  const [baseUrl, setBaseUrl] = useState("http://127.0.0.1:15030");
  const [upgradeProject, setUpgradeProject] = useState(DEFAULT_UPGRADE_PROJECT);
  const [controllerLoaded, setControllerLoaded] = useState(false);
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
