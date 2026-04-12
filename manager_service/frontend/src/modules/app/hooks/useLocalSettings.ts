import { useEffect, useState } from "react";
import {
  DEFAULT_UPGRADE_PROJECT,
  STORAGE_UPGRADE_PROJECT,
} from "../constants";

export function useLocalSettings() {
  const [baseUrl, setBaseUrl] = useState(() => localStorage.getItem("controller_base_url") || "http://127.0.0.1:15030");
  const [controllerIP, setControllerIPState] = useState(() => localStorage.getItem("controller_ip") || "");
  const [upgradeProject, setUpgradeProject] = useState(() => localStorage.getItem(STORAGE_UPGRADE_PROJECT) || DEFAULT_UPGRADE_PROJECT);
  const [controllerLoaded, setControllerLoaded] = useState(true);
  const [isLoadingControllerIP, setIsLoadingControllerIP] = useState(false);
  const [isSavingControllerIP, setIsSavingControllerIP] = useState(false);
  const [controllerIPStatus, setControllerIPStatus] = useState("");
  const [aiDebugListenEnabled, setAIDebugListenEnabledState] = useState(false);
  const [isLoadingAIDebugListenEnabled, setIsLoadingAIDebugListenEnabled] = useState(false);
  const [isSavingAIDebugListenEnabled, setIsSavingAIDebugListenEnabled] = useState(false);
  const [aiDebugListenStatus, setAIDebugListenStatus] = useState("AI Debug not supported in web mode");

  useEffect(() => {
    localStorage.setItem("controller_base_url", baseUrl);
  }, [baseUrl]);

  useEffect(() => {
    try {
      window.localStorage.setItem(STORAGE_UPGRADE_PROJECT, upgradeProject);
    } catch {
      // ignore localStorage errors
    }
  }, [upgradeProject]);

  async function refreshControllerIP() {
    setIsLoadingControllerIP(true);
    const next = localStorage.getItem("controller_ip") || "";
    setControllerIPState(next);
    setControllerIPStatus(next ? `Using controller IP: ${next}` : "Controller IP not configured");
    setIsLoadingControllerIP(false);
    return next;
  }

  async function saveControllerIP(value: string) {
    setIsSavingControllerIP(true);
    const next = value.trim();
    localStorage.setItem("controller_ip", next);
    setControllerIPState(next);
    setControllerIPStatus(next ? `Controller IP saved: ${next}` : "Controller IP cleared");
    setIsSavingControllerIP(false);
    return next;
  }

  async function refreshAIDebugListenEnabled() {
    setAIDebugListenStatus("AI Debug unsupported");
  }

  async function setAIDebugListenEnabled(enabled: boolean) {
    setAIDebugListenStatus("AI Debug unsupported");
  }

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
