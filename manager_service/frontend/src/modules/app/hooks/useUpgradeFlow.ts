/**
 * useUpgradeFlow.ts — 升级流程状态管理
 *
 * 重构说明 (PKG-FE-R03 / RQ-003 / C-FE-01 / C-FE-04):
 * - 已实现: GET /api/upgrade/release, POST /api/upgrade/manager, GET /api/system/version
 * - 未实现 (W4+): 主控升级流程、备份设置、主控升级进度
 *   → 保留函数语义，显式返回不可用状态，禁止静默降级
 * - 禁止引用 controller-api 中的 WS-RPC 升级函数
 */
import { useCallback, useState } from "react";
import { apiGetRelease, apiGetVersion, apiUpgradeManager } from "../manager-api";
import type { ReleaseInfo, UpgradeProgress } from "../types";

const emptyProgress: UpgradeProgress = {
  active: false,
  phase: "idle",
  percent: 0,
  message: "",
};

function notImplementedError(feature: string): Error {
  return new Error(`[W4-PENDING] ${feature} 功能暂未在 manager_service 实现，请等待 W4 后端代理端点就绪`);
}

export function useUpgradeFlow() {
  const [managerVersion, setManagerVersion] = useState("...");
  const [controllerVersion] = useState("—"); // [W4-PENDING]
  const [controllerLatestVersion] = useState("—"); // [W4-PENDING]
  const [versionStatus, setVersionStatus] = useState("未检查版本");
  const [mergedUpgradeStatus, setMergedUpgradeStatus] = useState("未升级");
  const [mergedUpgradeMessages, setMergedUpgradeMessages] = useState<string[]>([]);
  const [controllerUpgradeProgress] = useState<UpgradeProgress>(emptyProgress);
  const [managerUpgradeProgress, setManagerUpgradeProgress] = useState<UpgradeProgress>(emptyProgress);
  const [isUpgradingController] = useState(false); // [W4-PENDING]
  const [isUpgradingManager, setIsUpgradingManager] = useState(false);
  const [isCheckingDirect, setIsCheckingDirect] = useState(false);
  const [isCheckingProxy, setIsCheckingProxy] = useState(false);
  const [directRelease, setDirectRelease] = useState<ReleaseInfo | null>(null);
  const [proxyRelease, setProxyRelease] = useState<ReleaseInfo | null>(null);
  const [backupEnabled] = useState(false); // [W4-PENDING]
  const [backupRcloneRemote] = useState(""); // [W4-PENDING]
  const [backupSettingsStatus] = useState("[W4-PENDING] 备份设置功能暂未实现"); // [W4-PENDING]
  const [isLoadingBackupSettings] = useState(false);
  const [isSavingBackupSettings] = useState(false);
  const [isTestingBackupSettings] = useState(false);

  // ── 已实现 ──────────────────────────────────────────────────────────────────

  /** GET /api/system/version + 主控版本 [W4-PENDING] */
  const refreshSystemVersions = useCallback(async () => {
    setVersionStatus("正在检查版本...");
    try {
      const data = await apiGetVersion();
      setManagerVersion(data.version || "unknown");
      // 主控版本查询 [W4-PENDING]
      setVersionStatus(`manager_service ${data.version}（主控版本查询需 W4 代理端点）`);
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

  /** POST /api/upgrade/manager — 返回 supported=false 说明 (架构决策 RQ-008) */
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
    // 代理升级语义同直连 (manager_service 决定是否支持)
    await upgradeManagerDirect();
  }, [upgradeManagerDirect]);

  // ── 待实现 (W4+) ────────────────────────────────────────────────────────────

  /** @w4-pending 主控升级触发 */
  const upgradeController = useCallback(async () => {
    setMergedUpgradeStatus(notImplementedError("主控升级").message);
  }, []);

  /** @w4-pending 备份设置读取 */
  const refreshBackupSettings = useCallback(async () => {
    // [W4-PENDING] controller backup settings
  }, []);

  /** @w4-pending 备份设置保存 */
  const saveBackupSettings = useCallback(async (_enabled: boolean, _value: string) => {
    setMergedUpgradeStatus(notImplementedError("备份设置保存").message);
  }, []);

  /** @w4-pending 备份设置测试 */
  const testBackupSettings = useCallback(async (_value: string) => {
    setMergedUpgradeStatus(notImplementedError("备份连接测试").message);
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
    checkManagerReleaseDirect,
    upgradeManagerDirect,
    checkManagerReleaseProxy,
    upgradeManagerProxy,
    refreshBackupSettings,
    saveBackupSettings,
    testBackupSettings,
  };
}
