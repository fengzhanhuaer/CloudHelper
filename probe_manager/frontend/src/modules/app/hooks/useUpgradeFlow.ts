import { useState } from "react";
import {
  GetLatestGitHubRelease,
  GetLatestGitHubReleaseViaProxy,
  GetManagerVersion,
  UpgradeManagerDirect,
  UpgradeManagerViaProxy,
} from "../../../../wailsjs/go/main/App";
import { fetchControllerVersion, triggerControllerUpgrade } from "../services/controller-api";
import { normalizeBaseUrl } from "../utils/url";
import type { ControllerUpgradeResponse, ControllerVersionResponse, ManagerUpgradeResult, ReleaseInfo } from "../types";

function isUnauthorizedError(error: unknown): boolean {
  if (!(error instanceof Error)) {
    return false;
  }
  const message = error.message.toLowerCase();
  return message.includes("401") || message.includes("invalid or expired session token");
}

export function useUpgradeFlow() {
  const [managerVersion, setManagerVersion] = useState("unknown");
  const [controllerVersion, setControllerVersion] = useState("unknown");
  const [controllerLatestVersion, setControllerLatestVersion] = useState("");
  const [versionStatus, setVersionStatus] = useState("");

  const [upgradeStatus, setUpgradeStatus] = useState("");
  const [isUpgradingController, setIsUpgradingController] = useState(false);

  const [directRelease, setDirectRelease] = useState<ReleaseInfo | null>(null);
  const [proxyRelease, setProxyRelease] = useState<ReleaseInfo | null>(null);
  const [managerUpgradeStatus, setManagerUpgradeStatus] = useState("");
  const [isCheckingDirect, setIsCheckingDirect] = useState(false);
  const [isCheckingProxy, setIsCheckingProxy] = useState(false);
  const [isUpgradingManager, setIsUpgradingManager] = useState(false);

  async function refreshSystemVersions(baseUrlInput: string, token: string, reauthenticate?: () => Promise<string>) {
    const base = normalizeBaseUrl(baseUrlInput);
    if (!base) {
      setVersionStatus("Controller URL is required");
      return;
    }
    if (!token) {
      setVersionStatus("未登录，无法读取版本信息");
      return;
    }

    setVersionStatus("正在读取版本信息...");
    try {
      const localManagerVersion = await GetManagerVersion();
      setManagerVersion(localManagerVersion || "dev");

      let activeToken = token;
      let reloginUsed = false;
      let data: ControllerVersionResponse;
      try {
        data = await fetchControllerVersion(base, activeToken);
      } catch (error) {
        if (!reauthenticate || !isUnauthorizedError(error)) {
          throw error;
        }

        setVersionStatus("会话已过期，正在自动重新登录...");
        activeToken = await reauthenticate();
        reloginUsed = true;
        data = await fetchControllerVersion(base, activeToken);
      }

      setControllerVersion(data.current_version || "unknown");
      setControllerLatestVersion(data.latest_version || "");

      if (data.upgrade_available) {
        setVersionStatus(`${reloginUsed ? "已自动重新登录，" : ""}主控可升级：${data.current_version} -> ${data.latest_version}`);
      } else {
        setVersionStatus(reloginUsed ? `已自动重新登录，${data.message ?? "版本信息已更新"}` : (data.message ?? "版本信息已更新"));
      }
    } catch (error) {
      const msg = error instanceof Error ? error.message : "unknown error";
      setVersionStatus(`读取版本失败：${msg}`);
    }
  }

  async function upgradeController(baseUrlInput: string, token: string, reauthenticate?: () => Promise<string>) {
    const base = normalizeBaseUrl(baseUrlInput);
    if (!base) {
      setUpgradeStatus("Controller URL is required");
      return;
    }
    if (!token) {
      setUpgradeStatus("未登录，无法执行主控升级");
      return;
    }

    setIsUpgradingController(true);
    setUpgradeStatus("已发送升级命令，正在检查 GitHub Release...");
    try {
      let activeToken = token;
      let data: ControllerUpgradeResponse;
      try {
        data = (await triggerControllerUpgrade(base, activeToken)) as ControllerUpgradeResponse;
      } catch (error) {
        if (!reauthenticate || !isUnauthorizedError(error)) {
          throw error;
        }

        setUpgradeStatus("会话已过期，正在自动重新登录并重试升级...");
        activeToken = await reauthenticate();
        data = (await triggerControllerUpgrade(base, activeToken)) as ControllerUpgradeResponse;
      }

      setControllerVersion(data.current_version || controllerVersion);
      setControllerLatestVersion(data.latest_version || "");
      setUpgradeStatus(data.message || "升级命令执行完成");
      if (data.updated) {
        setVersionStatus("主控二进制已替换，服务正在重启，请稍后刷新版本");
      }
    } catch (error) {
      const msg = error instanceof Error ? error.message : "unknown error";
      setUpgradeStatus(`主控升级失败：${msg}`);
    } finally {
      setIsUpgradingController(false);
    }
  }

  function ensureUpgradeProject(projectInput: string): string | null {
    const project = projectInput.trim();
    if (!project) {
      setManagerUpgradeStatus("请先填写 GitHub 项目地址（owner/repo 或 github URL）");
      return null;
    }
    return project;
  }

  async function checkManagerReleaseDirect(projectInput: string) {
    const project = ensureUpgradeProject(projectInput);
    if (!project) {
      return;
    }

    setIsCheckingDirect(true);
    setManagerUpgradeStatus("直连检查中：正在请求 GitHub 最新 Release...");
    try {
      const release = (await GetLatestGitHubRelease(project)) as ReleaseInfo;
      setDirectRelease(release);
      setManagerUpgradeStatus(`直连检查完成：latest=${release.tag_name}, assets=${release.assets.length}`);
    } catch (error) {
      const msg = error instanceof Error ? error.message : "unknown error";
      setManagerUpgradeStatus(`直连检查失败：${msg}`);
    } finally {
      setIsCheckingDirect(false);
    }
  }

  async function checkManagerReleaseProxy(baseUrlInput: string, token: string, projectInput: string, reauthenticate?: () => Promise<string>) {
    const project = ensureUpgradeProject(projectInput);
    if (!project) {
      return;
    }
    if (!token) {
      setManagerUpgradeStatus("未登录，无法执行代理检查");
      return;
    }

    setIsCheckingProxy(true);
    setManagerUpgradeStatus("代理检查中：正在通过主控请求 GitHub 最新 Release...");
    try {
      let activeToken = token;
      let reloginUsed = false;
      let release: ReleaseInfo;
      try {
        release = (await GetLatestGitHubReleaseViaProxy(baseUrlInput, activeToken, project)) as ReleaseInfo;
      } catch (error) {
        if (!reauthenticate || !isUnauthorizedError(error)) {
          throw error;
        }

        setManagerUpgradeStatus("会话已过期，正在自动重新登录并重试代理检查...");
        activeToken = await reauthenticate();
        reloginUsed = true;
        release = (await GetLatestGitHubReleaseViaProxy(baseUrlInput, activeToken, project)) as ReleaseInfo;
      }

      setProxyRelease(release);
      setManagerUpgradeStatus(`${reloginUsed ? "已自动重新登录，" : ""}代理检查完成：latest=${release.tag_name}, assets=${release.assets.length}`);
    } catch (error) {
      const msg = error instanceof Error ? error.message : "unknown error";
      setManagerUpgradeStatus(`代理检查失败：${msg}`);
    } finally {
      setIsCheckingProxy(false);
    }
  }

  async function upgradeManagerDirect(projectInput: string) {
    const project = ensureUpgradeProject(projectInput);
    if (!project) {
      return;
    }

    setIsUpgradingManager(true);
    setManagerUpgradeStatus("直连升级中：下载并应用管理端更新...");
    try {
      const result = (await UpgradeManagerDirect(project)) as ManagerUpgradeResult;
      if (result.latest_version) {
        setManagerVersion(result.latest_version);
      }
      setManagerUpgradeStatus(`直连升级结果：${result.message}`);
    } catch (error) {
      const msg = error instanceof Error ? error.message : "unknown error";
      setManagerUpgradeStatus(`直连升级失败：${msg}`);
    } finally {
      setIsUpgradingManager(false);
    }
  }

  async function upgradeManagerProxy(baseUrlInput: string, token: string, projectInput: string, reauthenticate?: () => Promise<string>) {
    const project = ensureUpgradeProject(projectInput);
    if (!project) {
      return;
    }
    if (!token) {
      setManagerUpgradeStatus("未登录，无法执行代理升级");
      return;
    }

    setIsUpgradingManager(true);
    setManagerUpgradeStatus("代理升级中：通过主控下载并转发升级包...");
    try {
      let activeToken = token;
      let reloginUsed = false;
      let result: ManagerUpgradeResult;
      try {
        result = (await UpgradeManagerViaProxy(baseUrlInput, activeToken, project)) as ManagerUpgradeResult;
      } catch (error) {
        if (!reauthenticate || !isUnauthorizedError(error)) {
          throw error;
        }

        setManagerUpgradeStatus("会话已过期，正在自动重新登录并重试代理升级...");
        activeToken = await reauthenticate();
        reloginUsed = true;
        result = (await UpgradeManagerViaProxy(baseUrlInput, activeToken, project)) as ManagerUpgradeResult;
      }

      if (result.latest_version) {
        setManagerVersion(result.latest_version);
      }
      setManagerUpgradeStatus(`${reloginUsed ? "已自动重新登录，" : ""}代理升级结果：${result.message}`);
    } catch (error) {
      const msg = error instanceof Error ? error.message : "unknown error";
      setManagerUpgradeStatus(`代理升级失败：${msg}`);
    } finally {
      setIsUpgradingManager(false);
    }
  }

  function clearUpgradeMessages() {
    setVersionStatus("");
    setUpgradeStatus("");
    setManagerUpgradeStatus("");
  }

  return {
    managerVersion,
    controllerVersion,
    controllerLatestVersion,
    versionStatus,
    upgradeStatus,
    isUpgradingController,
    directRelease,
    proxyRelease,
    managerUpgradeStatus,
    isCheckingDirect,
    isCheckingProxy,
    isUpgradingManager,
    refreshSystemVersions,
    upgradeController,
    checkManagerReleaseDirect,
    checkManagerReleaseProxy,
    upgradeManagerDirect,
    upgradeManagerProxy,
    clearUpgradeMessages,
  };
}
