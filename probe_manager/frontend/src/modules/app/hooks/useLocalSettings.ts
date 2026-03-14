import { useEffect, useState } from "react";
import {
  DEFAULT_UPGRADE_PROJECT,
  STORAGE_CONTROLLER_URL,
  STORAGE_UPGRADE_PROJECT,
} from "../constants";

export function useLocalSettings() {
  const [baseUrl, setBaseUrl] = useState("http://127.0.0.1:15030");
  const [upgradeProject, setUpgradeProject] = useState(DEFAULT_UPGRADE_PROJECT);

  useEffect(() => {
    try {
      const savedBaseURL = window.localStorage.getItem(STORAGE_CONTROLLER_URL);
      if (savedBaseURL?.trim()) {
        setBaseUrl(savedBaseURL.trim());
      }

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
      window.localStorage.setItem(STORAGE_CONTROLLER_URL, baseUrl);
    } catch {
      // ignore localStorage errors
    }
  }, [baseUrl]);

  useEffect(() => {
    try {
      window.localStorage.setItem(STORAGE_UPGRADE_PROJECT, upgradeProject);
    } catch {
      // ignore localStorage errors
    }
  }, [upgradeProject]);

  return {
    baseUrl,
    setBaseUrl,
    upgradeProject,
    setUpgradeProject,
  };
}
