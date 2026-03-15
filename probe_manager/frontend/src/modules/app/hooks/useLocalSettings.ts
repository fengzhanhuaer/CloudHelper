import { useEffect, useState } from "react";
import {
  DEFAULT_UPGRADE_PROJECT,
  STORAGE_UPGRADE_PROJECT,
} from "../constants";
import { GetGlobalControllerURL, SetGlobalControllerURL } from "../../../../wailsjs/go/main/App";

export function useLocalSettings() {
  const [baseUrl, setBaseUrl] = useState("http://127.0.0.1:15030");
  const [upgradeProject, setUpgradeProject] = useState(DEFAULT_UPGRADE_PROJECT);
  const [controllerLoaded, setControllerLoaded] = useState(false);

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

  return {
    baseUrl,
    setBaseUrl,
    upgradeProject,
    setUpgradeProject,
  };
}
