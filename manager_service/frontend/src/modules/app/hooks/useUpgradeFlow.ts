/**
 * useUpgradeFlow.ts — 升级流程状态管理
 *
 * 重构说明 (PKG-FE-R03 / RQ-003 / C-FE-01 / C-FE-04):
 * - 已实现: GET /api/upgrade/release, POST /api/upgrade/manager, GET /api/system/version
 * - R8-BE 已实现: backup-settings get/set/test via /api/system/backup-settings
 * - W4 已实现: controller-version, controller-upgrade, controller-upgrade-progress
 * - 禁止引用 controller-api 中的 WS-RPC 升级函数
 */
import { useCallback, useState } from "react";
import {
  apiGetRelease,
  apiGetVersion,
  apiUpgradeManager,
  apiGetBackupSettings,
  apiSetBackupSettings,
  apiTestBackupSettings,
  apiGetControllerVersion,
  apiUpgradeController,
  apiGetControllerUpgradeProgress,
} from "../manager-api";
import type { ReleaseInfo, UpgradeProgress } from "../types";

const emptyProgress: UpgradeProgress = {
  active: false,
  phase: "idle",
  percent: 0,
  message: "",
};

export function useUpgradeFlow() {
  const [managerVersion, setManagerVersion] = useState("...");
  const [controllerVersion, setControllerVersion] = useState("—");
  const [controllerLatestVersion, setControllerLatestVersion] = useState("—");
  const [versionStatus, setVersionStatus] = useState("未检查版本");
  const [mergedUpgradeStatus, setMergedUpgradeStatus] = useState("未升级");
  const [mergedUpgradeMessages, setMergedUpgradeMessages] = useState<string[]>([]);
  const [controllerUpgradeProgress, setControllerUpgradeProgress] = useState<UpgradeProgress>(emptyProgress);
  const [managerUpgradeProgress, setManagerUpgradeProgress] = useState<UpgradeProgress>(emptyProgress);
  const [isUpgradingController, setIsUpgradingController] = useState(false);
  const [isUpgradingManager, setIsUpgradingManager] = useState(false);
  const [isCheckingDirect, setIsCheckingDirect] = useState(false);
  const [isCheckingProxy, setIsCheckingProxy] = useState(false);
  const [directRelease, setDirectRelease] = useState<ReleaseInfo | null>(null);
  const [proxyRelease, setProxyRelease] = useState<ReleaseInfo | null>(null);

  // R8-BE: backup settings state (real)
  const [backupEnabled, setBackupEnabled] = useState(false);
  const [backupRcloneRemote, setBackupRcloneRemote] = useState("");
  const [backupSettingsStatus, setBackupSettingsStatus] = useState("未加载");
  const [isLoadingBackupSettings, setIsLoadingBackupSettings] = useState(false);
  const [isSavingBackupSettings, setIsSavingBackupSettings] = useState(false);
  const [isTestingBackupSettings, setIsTestingBackupSettings] = useState(false);

  // ── Manager 版本与升级 ────────────────────────────────────────────────────────

  /** GET /api/system/version + GET /api/system/controller-version (W4) */
  const refreshSystemVersions = useCallback(async () => {
    setVersionStatus("正在检查版本...");
    try {
      const [managerData, controllerData] = await Promise.allSettled([
        apiGetVersion(),
        apiGetControllerVersion(),
      ]);
      const mv = managerData.status === "fulfilled" ? (managerData.value.version || "unknown") : "error";
      setManagerVersion(mv);
      if (controllerData.status === "fulfilled") {
        const cv = controllerData.value;
        setControllerVersion(cv.current_version ?? "—");
        setControllerLatestVersion(cv.latest_version ?? "—");
        const upgradeNote = cv.upgrade_available ? "（有新版本可升级）" : "";
        setVersionStatus(`manager ${mv} | controller ${cv.current_version ?? "—"}${upgradeNote}`);
      } else {
        setVersionStatus(`manager ${mv} | 主控版本查询失败`);
      }
    } catch (error) {
      const msg = error instanceof Error ? error.message : "unknown";
      setVersionStatus(`版本检查失败：${msg}`);
    }
  }, []);

  /** GET /api/upgrade/release?project=... (直连 GitHub) */
  const checkManagerReleaseDirect = useCallback(async (project: string) => {
    setIsCheckingDirect(true);
    try {
      const info = await apiGetRelease(project);
      setDirectRelease(info as unknown as ReleaseInfo);
    } catch (error) {
      const msg = error instanceof Error ? error.message : "unknown";
      setMergedUpgradeStatus(`直连检查失败：${msg}`);
    } finally {
      setIsCheckingDirect(false);
    }
  }, []);

  /** GET /api/upgrade/release?project=... (代理) */
  const checkManagerReleaseProxy = useCallback(async (project: string) => {
    setIsCheckingProxy(true);
    try {
      const info = await apiGetRelease(`${project}:proxy`);
      setProxyRelease(info as unknown as ReleaseInfo);
    } catch (error) {
      const msg = error instanceof Error ? error.message : "unknown";
      setMergedUpgradeStatus(`代理检查失败：${msg}`);
    } finally {
      setIsCheckingProxy(false);
    }
  }, []);

  /** POST /api/upgrade/manager */
  const upgradeManagerDirect = useCallback(async () => {
    setIsUpgradingManager(true);
    setManagerUpgradeProgress({ active: true, phase: "running", percent: 0, message: "正在请求升级..." });
    try {
      const result = await apiUpgradeManager();
      const msg = result.supported
        ? "升级请求已发送"
        : `升级说明：${result.reason}${result.docs_url ? `（文档：${result.docs_url}）` : ""}`;
      setManagerUpgradeProgress({ active: false, phase: result.supported ? "done" : "idle", percent: 100, message: msg });
      setMergedUpgradeStatus(msg);
      setMergedUpgradeMessages([msg]);
    } catch (error) {
      const msg = error instanceof Error ? error.message : "unknown";
      setManagerUpgradeProgress({ active: false, phase: "error", percent: 0, message: `升级失败：${msg}` });
      setMergedUpgradeStatus(`升级失败：${msg}`);
    } finally {
      setIsUpgradingManager(false);
    }
  }, []);

  const upgradeManagerProxy = useCallback(async () => {
    await upgradeManagerDirect();
  }, [upgradeManagerDirect]);

  // ── W4: 主控升级（实装） ──────────────────────────────────────────────────────

  /** POST /api/system/controller-upgrade (W4) */
  const upgradeController = useCallback(async () => {
    setIsUpgradingController(true);
    setControllerUpgradeProgress({ active: true, phase: "running", percent: 0, message: "正在触发主控升级..." });
    setMergedUpgradeStatus("正在触发主控升级...");
    try {
      const result = await apiUpgradeController();
      const msg = result.message ?? "主控升级请求已发送";
      setControllerUpgradeProgress({ active: false, phase: "done", percent: 100, message: msg });
      setMergedUpgradeStatus(msg);
      setMergedUpgradeMessages([msg]);
    } catch (error) {
      const msg = error instanceof Error ? error.message : "unknown";
      setControllerUpgradeProgress({ active: false, phase: "error", percent: 0, message: `主控升级失败：${msg}` });
      setMergedUpgradeStatus(`主控升级失败：${msg}`);
    } finally {
      setIsUpgradingController(false);
    }
  }, []);

  /** GET /api/system/controller-upgrade-progress (轮询, 外部调用) */
  const refreshControllerUpgradeProgress = useCallback(async () => {
    try {
      const progress = await apiGetControllerUpgradeProgress();
      setControllerUpgradeProgress({
        active: progress.active ?? false,
        phase: (progress.phase as UpgradeProgress["phase"]) ?? "idle",
        percent: progress.percent ?? 0,
        message: progress.message ?? "",
      });
    } catch {
      // 静默忽略进度查询失败
    }
  }, []);

  // ── R8-BE: 备份设置 ───────────────────────────────────────────────────────────

  /** GET /api/system/backup-settings */
  const refreshBackupSettings = useCallback(async () => {
    setIsLoadingBackupSettings(true);
    setBackupSettingsStatus("正在加载备份设置...");
    try {
      const data = await apiGetBackupSettings();
      setBackupEnabled(data.enabled);
      setBackupRcloneRemote(data.rclone_remote);
      setBackupSettingsStatus(data.enabled ? `备份已启用（${data.rclone_remote}）` : "备份未启用");
    } catch (error) {
      const msg = error instanceof Error ? error.message : "unknown";
      setBackupSettingsStatus(`备份设置加载失败：${msg}`);
    } finally {
      setIsLoadingBackupSettings(false);
    }
  }, []);

  /** POST /api/system/backup-settings */
  const saveBackupSettings = useCallback(async (enabled: boolean, value: string) => {
    setIsSavingBackupSettings(true);
    setBackupSettingsStatus("正在保存备份设置...");
    try {
      const data = await apiSetBackupSettings(enabled, value);
      setBackupEnabled(data.enabled);
      setBackupRcloneRemote(data.rclone_remote);
      setBackupSettingsStatus(data.enabled ? `备份设置已保存（${data.rclone_remote}）` : "备份设置已保存（未启用）");
    } catch (error) {
      const msg = error instanceof Error ? error.message : "unknown";
      setBackupSettingsStatus(`备份设置保存失败：${msg}`);
    } finally {
      setIsSavingBackupSettings(false);
    }
  }, []);

  /** POST /api/system/backup-settings/test */
  const testBackupSettings = useCallback(async (value: string) => {
    setIsTestingBackupSettings(true);
    setBackupSettingsStatus("正在测试备份连接...");
    try {
      const result = await apiTestBackupSettings(value);
      setBackupSettingsStatus(result.ok ? `连接成功：${result.message}` : `连接失败：${result.message}`);
    } catch (error) {
      const msg = error instanceof Error ? error.message : "unknown";
      setBackupSettingsStatus(`备份连接测试失败：${msg}`);
    } finally {
      setIsTestingBackupSettings(false);
    }
  }, []);

  return {
    managerVersion,
    controllerVersion,
    controllerLatestVersion,
    versionStatus,
    mergedUpgradeStatus,
    mergedUpgradeMessages,
    controllerUpgradeProgress,
    managerUpgradeProgress,
    isUpgradingController,
    isUpgradingManager,
    isCheckingDirect,
    isCheckingProxy,
    directRelease,
    proxyRelease,
    backupEnabled,
    backupRcloneRemote,
    backupSettingsStatus,
    isLoadingBackupSettings,
    isSavingBackupSettings,
    isTestingBackupSettings,
    refreshSystemVersions,
    upgradeController,
    refreshControllerUpgradeProgress,
    checkManagerReleaseDirect,
    upgradeManagerDirect,
    checkManagerReleaseProxy,
    upgradeManagerProxy,
    refreshBackupSettings,
    saveBackupSettings,
    testBackupSettings,
  };
}
