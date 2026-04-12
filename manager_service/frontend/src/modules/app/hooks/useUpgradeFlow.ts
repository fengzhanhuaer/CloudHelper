import { useState } from "react";
import { fetchJson } from "../api";
import {
  fetchControllerBackupSettings,
  testControllerBackupSettings,
  fetchControllerUpgradeProgress,
  fetchControllerVersion,
  setControllerBackupSettings,
  triggerControllerUpgrade,
} from "../services/controller-api";
import { normalizeBaseUrl } from "../utils/url";
import type { ControllerUpgradeResponse, ControllerVersionResponse, ManagerUpgradeResult, ReleaseInfo, UpgradeProgress } from "../types";


function isUnauthorizedError(error: unknown): boolean {
  if (!(error instanceof Error)) {
    return false;
  }
  const message = error.message.toLowerCase();
  return message.includes("401") || message.includes("invalid or expired session token");
}

function isControllerUpgradeTransientError(error: unknown): boolean {
  if (!(error instanceof Error)) {
    return false;
  }
  const message = error.message.toLowerCase();
  if (!message.includes("admin.upgrade")) {
    return false;
  }
  return (
    message.includes("timeout")
    || message.includes("failed")
    || message.includes("close")
    || message.includes("disconnect")
    || message.includes("network")
  );
}

type UpgradeErrorCategory = "auth" | "timeout" | "network" | "upstream" | "storage" | "config" | "unknown";

type UpgradeErrorHint = {
  category: UpgradeErrorCategory;
  summary: string;
  raw: string;
};

function normalizeErrorMessage(error: unknown): string {
  if (error instanceof Error) {
    return (error.message || "").trim() || "unknown error";
  }
  if (typeof error === "string") {
    return error.trim() || "unknown error";
  }
  return "unknown error";
}

function truncateText(text: string, max = 220): string {
  const raw = (text || "").trim();
  if (!raw) {
    return "";
  }
  if (raw.length <= max) {
    return raw;
  }
  return `${raw.slice(0, Math.max(0, max - 3))}...`;
}

function parseStatusCode(text: string): number | null {
  const m = text.match(/status\s*[=:]\s*(\d{3})/i);
  if (!m) {
    return null;
  }
  const v = Number.parseInt(m[1], 10);
  return Number.isFinite(v) ? v : null;
}

function resolveUpgradeErrorHint(error: unknown): UpgradeErrorHint {
  const raw = normalizeErrorMessage(error);
  const message = raw.toLowerCase();

  if (message.includes("invalid or expired session token") || message.includes("unauthorized") || message.includes(" 401") || message.includes("status=401")) {
    return {
      category: "auth",
      summary: "登录会话已失效，请重新登录后重试。",
      raw,
    };
  }

  if (message.includes("project must be owner/repo")
    || message.includes("invalid github project url")
    || message.includes("latest release tag is empty")
    || message.includes("no matching manager asset")
    || message.includes("invalid release asset")) {
    return {
      category: "config",
      summary: "升级参数或发布资产不匹配，请检查项目地址与发布包。",
      raw,
    };
  }

  if (message.includes("stage=upstream.status") || message.includes("upstream status=")) {
    const code = parseStatusCode(raw);
    return {
      category: "upstream",
      summary: code ? `上游下载源返回异常状态码 ${code}，请稍后重试或更换网络出口。` : "上游下载源返回异常状态码，请稍后重试。",
      raw,
    };
  }

  if (message.includes("timeout") || message.includes("deadline exceeded")) {
    return {
      category: "timeout",
      summary: "请求或下载超时，请稍后重试；若持续出现，请检查主控与网络质量。",
      raw,
    };
  }

  if (message.includes("connection") || message.includes("websocket") || message.includes("disconnect") || message.includes("unexpected eof") || message.includes("broken pipe")) {
    return {
      category: "network",
      summary: "网络连接中断，已建议重试；如频繁出现请检查链路稳定性。",
      raw,
    };
  }

  if (message.includes("stage=file.") || message.includes("rename") || message.includes("short write") || message.includes("no space left")) {
    return {
      category: "storage",
      summary: "本地写入升级包失败，请检查磁盘空间与文件权限。",
      raw,
    };
  }

  return {
    category: "unknown",
    summary: "升级流程执行失败，请查看日志后重试。",
    raw,
  };
}

function formatUpgradeFailureStatus(prefix: string, hint: UpgradeErrorHint): string {
  const raw = truncateText(hint.raw, 180);
  return `${prefix}：${hint.summary}${raw ? `（原始错误：${raw}）` : ""}`;
}

function formatUpgradeFailureLog(prefix: string, hint: UpgradeErrorHint): string {
  return `${prefix}：category=${hint.category} summary=${hint.summary} raw=${hint.raw || "unknown error"}`;
}

function formatUpgradeLogLine(text: string, timeInput?: string): string {
  const content = text.trim();
  if (!content) {
    return "";
  }
  const raw = (timeInput || "").trim();
  const dt = raw ? new Date(raw) : new Date();
  const label = Number.isNaN(dt.getTime()) ? new Date().toLocaleString() : dt.toLocaleString();
  return `[${label}] ${content}`;
}

function appendUpgradeLog(setter: (fn: (prev: string[]) => string[]) => void, text: string, timeInput?: string) {
  const line = formatUpgradeLogLine(text, timeInput);
  if (!line) {
    return;
  }
  setter((prev) => {
    const next = [...prev, line];
    if (next.length <= 300) {
      return next;
    }
    return next.slice(next.length - 300);
  });
}

function formatProgressLog(prefix: string, progress: UpgradeProgress): string {
  const phase = (progress.phase || "running").trim();
  const msg = (progress.message || "").trim();
  return `${prefix} ${phase} ${progress.percent}%${msg ? ` - ${msg}` : ""}`;
}

function sleepMs(ms: number): Promise<void> {
  return new Promise((resolve) => {
    window.setTimeout(resolve, ms);
  });
}

export function useUpgradeFlow() {
  const [managerVersion, setManagerVersion] = useState("unknown");
  const [controllerVersion, setControllerVersion] = useState("unknown");
  const [controllerLatestVersion, setControllerLatestVersion] = useState("");
  const [versionStatus, setVersionStatus] = useState("");

  const [upgradeStatus, setUpgradeStatus] = useState("");
  const [isUpgradingController, setIsUpgradingController] = useState(false);
  const [controllerUpgradeProgress, setControllerUpgradeProgress] = useState<UpgradeProgress>({
    active: false,
    phase: "idle",
    percent: 0,
    message: "",
  });
  const [controllerUpgradeMessages, setControllerUpgradeMessages] = useState<string[]>([]);

  const [directRelease, setDirectRelease] = useState<ReleaseInfo | null>(null);
  const [proxyRelease, setProxyRelease] = useState<ReleaseInfo | null>(null);
  const [managerUpgradeStatus, setManagerUpgradeStatus] = useState("");
  const [isCheckingDirect, setIsCheckingDirect] = useState(false);
  const [isCheckingProxy, setIsCheckingProxy] = useState(false);
  const [isUpgradingManager, setIsUpgradingManager] = useState(false);
  const [managerUpgradeProgress, setManagerUpgradeProgress] = useState<UpgradeProgress>({
    active: false,
    phase: "idle",
    percent: 0,
    message: "",
  });
  const [managerUpgradeMessages, setManagerUpgradeMessages] = useState<string[]>([]);
  const [mergedUpgradeStatus, setMergedUpgradeStatus] = useState("");
  const [mergedUpgradeMessages, setMergedUpgradeMessages] = useState<string[]>([]);
  const [backupRcloneRemote, setBackupRcloneRemote] = useState("");
  const [backupEnabled, setBackupEnabled] = useState(false);
  const [backupSettingsStatus, setBackupSettingsStatus] = useState("");
  const [isLoadingBackupSettings, setIsLoadingBackupSettings] = useState(false);
  const [isSavingBackupSettings, setIsSavingBackupSettings] = useState(false);
  const [isTestingBackupSettings, setIsTestingBackupSettings] = useState(false);

  function updateControllerUpgradeStatus(message: string) {
    setUpgradeStatus(message);
    setMergedUpgradeStatus(message);
  }

  function updateManagerUpgradeStatus(message: string) {
    setManagerUpgradeStatus(message);
    setMergedUpgradeStatus(message);
  }

  function appendControllerUpgradeMessage(text: string, timeInput?: string) {
    appendUpgradeLog(setControllerUpgradeMessages, text, timeInput);
    appendUpgradeLog(setMergedUpgradeMessages, text, timeInput);
  }

  function appendManagerUpgradeMessage(text: string, timeInput?: string) {
    appendUpgradeLog(setManagerUpgradeMessages, text, timeInput);
    appendUpgradeLog(setMergedUpgradeMessages, text, timeInput);
  }

  function beginProgress(setter: (value: UpgradeProgress) => void, message: string) {
    setter({ active: true, phase: "prepare", percent: 1, message });
  }

  function resetProgress(setter: (value: UpgradeProgress) => void) {
    setter({ active: false, phase: "idle", percent: 0, message: "" });
  }

  function normalizeProgress(value: unknown): UpgradeProgress {
    const input = (value ?? {}) as Partial<UpgradeProgress>;
    return {
      active: Boolean(input.active),
      phase: input.phase || "running",
      percent: typeof input.percent === "number" ? Math.max(0, Math.min(100, input.percent)) : 0,
      message: input.message || "",
    };
  }

  function startPolling(
    task: () => Promise<UpgradeProgress>,
    setter: (value: UpgradeProgress) => void,
    onProgress?: (progress: UpgradeProgress) => void,
  ): () => void {
    let active = true;
    const tick = async () => {
      if (!active) {
        return;
      }
      try {
        const progress = normalizeProgress(await task());
        if (active) {
          setter(progress);
          if (onProgress) {
            onProgress(progress);
          }
        }
      } catch {
        // keep previous state
      }
    };

    void tick();
    const timer = window.setInterval(() => {
      void tick();
    }, 500);

    return () => {
      active = false;
      window.clearInterval(timer);
    };
  }

  async function confirmControllerUpgradeAfterTransientError(
    base: string,
    token: string,
    oldVersion: string,
    oldLatestVersion: string,
    reauthenticate?: () => Promise<string>,
    onStep?: (text: string) => void,
  ): Promise<{ confirmed: boolean; data?: ControllerVersionResponse }> {
    let activeToken = token;
    let lastVersion: ControllerVersionResponse | undefined;
    const prevVersion = oldVersion.trim();
    const prevLatest = oldLatestVersion.trim();

    for (let attempt = 1; attempt <= 20; attempt++) {
      try {
        const data = await fetchControllerVersion(base, activeToken);
        lastVersion = data;

        const current = (data.current_version || "").trim();
        const latest = (data.latest_version || "").trim();
        if (onStep) {
          onStep(`确认升级状态：第 ${attempt}/20 次，current=${current || "-"} latest=${latest || "-"}`);
        }

        if (current && prevVersion && current !== prevVersion) {
          return { confirmed: true, data };
        }
        if (current && prevLatest && current === prevLatest) {
          return { confirmed: true, data };
        }
        if (current && latest && current === latest) {
          return { confirmed: true, data };
        }
      } catch (error) {
        if (onStep) {
          const msg = error instanceof Error ? error.message : "unknown error";
          onStep(`确认升级状态：第 ${attempt}/20 次失败，${msg}`);
        }
        if (reauthenticate && isUnauthorizedError(error)) {
          try {
            activeToken = await reauthenticate();
            if (onStep) {
              onStep("确认升级状态：会话过期，自动重新登录后继续确认");
            }
          } catch {
            // keep retrying with old token
          }
        }
      }

      if (attempt < 20) {
        await sleepMs(3000);
      }
    }

    return { confirmed: false, data: lastVersion };
  }

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
      const localManagerVersionReq = await fetchJson<{ version: string }>('/system/version').catch(() => ({ version: "dev" }));
      setManagerVersion(localManagerVersionReq.version || "dev");

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
      updateControllerUpgradeStatus("Controller URL is required");
      return;
    }
    if (!token) {
      updateControllerUpgradeStatus("未登录，无法执行主控升级");
      return;
    }

    setIsUpgradingController(true);
    updateControllerUpgradeStatus("已发送升级命令，正在检查 GitHub Release...");
    beginProgress(setControllerUpgradeProgress, "准备升级主控");
    appendControllerUpgradeMessage(`开始主控升级：current=${controllerVersion || "-"} latest=${controllerLatestVersion || "-"}`);
    const versionBeforeUpgrade = (controllerVersion || "").trim();
    const latestBeforeUpgrade = (controllerLatestVersion || "").trim();
    let lastControllerProgressLogKey = "";

    const stopPolling = startPolling(
      () => fetchControllerUpgradeProgress(base, token),
      setControllerUpgradeProgress,
      (progress) => {
        const bucket = progress.phase === "download" ? Math.floor(progress.percent / 5) * 5 : progress.percent;
        const key = `${progress.phase}|${bucket}|${progress.message || ""}`;
        if (key === lastControllerProgressLogKey) {
          return;
        }
        lastControllerProgressLogKey = key;
        appendControllerUpgradeMessage(formatProgressLog("主控升级进度", progress));
      },
    );
    try {
      let activeToken = token;
      let data: ControllerUpgradeResponse;
      try {
        appendControllerUpgradeMessage("主控升级：发送 admin.upgrade 命令");
        data = (await triggerControllerUpgrade(base, activeToken)) as ControllerUpgradeResponse;
      } catch (error) {
        if (!reauthenticate || !isUnauthorizedError(error)) {
          throw error;
        }

        updateControllerUpgradeStatus("会话已过期，正在自动重新登录并重试升级...");
        appendControllerUpgradeMessage("主控升级：会话已过期，自动重新登录并重试");
        activeToken = await reauthenticate();
        data = (await triggerControllerUpgrade(base, activeToken)) as ControllerUpgradeResponse;
      }

      setControllerVersion(data.current_version || controllerVersion);
      setControllerLatestVersion(data.latest_version || "");
      updateControllerUpgradeStatus(data.message || "升级命令执行完成");
      if (data.updated) {
        appendControllerUpgradeMessage(`主控升级完成：current=${data.current_version || "-"} latest=${data.latest_version || "-"}`);
        setVersionStatus("主控二进制已替换，服务正在重启，请稍后刷新版本");
        appendControllerUpgradeMessage("主控升级成功：二进制已替换，等待服务重启");
      } else {
        // 后端立即返回"已发起"信号时 updated=false、latest_version 为空属正常情况
        // 真正的升级结果通过进度轮询（fetchControllerUpgradeProgress）获取
        const isStarted = !data.latest_version && (data.message || "").toLowerCase().includes("started");
        appendControllerUpgradeMessage(
          isStarted
            ? `主控升级任务已发起：current=${data.current_version || "-"}，正在后台执行，请等待进度更新`
            : `主控升级命令返回：updated=false current=${data.current_version || "-"} latest=${data.latest_version || "-"} msg=${data.message || "-"}`,
        );
      }
    } catch (error) {
      if (isControllerUpgradeTransientError(error)) {
        updateControllerUpgradeStatus("升级命令返回超时/断开，正在确认主控是否已完成升级...");
        appendControllerUpgradeMessage("主控升级命令超时/断开，开始自动确认升级结果");
        const confirmed = await confirmControllerUpgradeAfterTransientError(
          base,
          token,
          versionBeforeUpgrade,
          latestBeforeUpgrade,
          reauthenticate,
          (text) => appendControllerUpgradeMessage(text),
        );

        if (confirmed.data) {
          setControllerVersion(confirmed.data.current_version || controllerVersion);
          setControllerLatestVersion(confirmed.data.latest_version || "");
        }

        if (confirmed.confirmed) {
          const current = (confirmed.data?.current_version || "").trim();
          if (current && versionBeforeUpgrade && current !== versionBeforeUpgrade) {
            updateControllerUpgradeStatus(`主控升级成功：${versionBeforeUpgrade} -> ${current}（RPC 超时已自动纠正）`);
            appendControllerUpgradeMessage(`主控升级确认成功：${versionBeforeUpgrade} -> ${current}（RPC 超时自动纠正）`);
          } else {
            updateControllerUpgradeStatus("主控升级成功（RPC 超时已自动纠正）");
            appendControllerUpgradeMessage("主控升级确认成功（RPC 超时自动纠正）");
          }
          setVersionStatus("主控升级已完成，如需确认可点击刷新版本");
          return;
        }

        const msg = error instanceof Error ? error.message : "unknown error";
        updateControllerUpgradeStatus(`主控升级状态待确认：${msg}（版本未在预期时间内变化）`);
        appendControllerUpgradeMessage(`主控升级状态待确认：${msg}`);
        return;
      }

      const hint = resolveUpgradeErrorHint(error);
      updateControllerUpgradeStatus(formatUpgradeFailureStatus("主控升级失败", hint));
      appendControllerUpgradeMessage(formatUpgradeFailureLog("主控升级失败", hint));
    } finally {
      stopPolling();
      resetProgress(setControllerUpgradeProgress);
      setIsUpgradingController(false);
    }
  }

  async function refreshBackupSettings(baseUrlInput: string, token: string, reauthenticate?: () => Promise<string>) {
    const base = normalizeBaseUrl(baseUrlInput);
    if (!base) {
      setBackupSettingsStatus("Controller URL is required");
      return;
    }
    if (!token) {
      setBackupSettingsStatus("未登录，无法读取主控备份设置");
      return;
    }

    setIsLoadingBackupSettings(true);
    setBackupSettingsStatus("正在读取主控备份设置...");
    try {
      let activeToken = token;
      let reloginUsed = false;
      let data: { enabled: boolean; rclone_remote: string };
      try {
        data = await fetchControllerBackupSettings(base, activeToken);
      } catch (error) {
        if (!reauthenticate || !isUnauthorizedError(error)) {
          throw error;
        }
        setBackupSettingsStatus("会话已过期，正在自动重新登录...");
        activeToken = await reauthenticate();
        reloginUsed = true;
        data = await fetchControllerBackupSettings(base, activeToken);
      }

      setBackupEnabled(Boolean(data.enabled));
      setBackupRcloneRemote((data.rclone_remote || "").trim());
      setBackupSettingsStatus(reloginUsed ? "已自动重新登录，主控备份设置已更新" : "主控备份设置已更新");
    } catch (error) {
      const msg = error instanceof Error ? error.message : "unknown error";
      setBackupSettingsStatus(`读取主控备份设置失败：${msg}`);
    } finally {
      setIsLoadingBackupSettings(false);
    }
  }

  async function saveBackupSettings(baseUrlInput: string, token: string, enabled: boolean, rcloneRemoteInput: string, reauthenticate?: () => Promise<string>) {
    const base = normalizeBaseUrl(baseUrlInput);
    if (!base) {
      setBackupSettingsStatus("Controller URL is required");
      return;
    }
    if (!token) {
      setBackupSettingsStatus("未登录，无法保存主控备份设置");
      return;
    }

    const remote = rcloneRemoteInput.trim();
    if (enabled && !remote) {
      setBackupSettingsStatus("开启备份时必须填写 rclone 远端路径，例如 remote:/path");
      return;
    }

    setIsSavingBackupSettings(true);
    setBackupSettingsStatus("正在保存主控备份设置...");
    try {
      let activeToken = token;
      let reloginUsed = false;
      let data: { enabled: boolean; rclone_remote: string };
      try {
        data = await setControllerBackupSettings(base, activeToken, enabled, remote);
      } catch (error) {
        if (!reauthenticate || !isUnauthorizedError(error)) {
          throw error;
        }
        setBackupSettingsStatus("会话已过期，正在自动重新登录并重试保存...");
        activeToken = await reauthenticate();
        reloginUsed = true;
        data = await setControllerBackupSettings(base, activeToken, enabled, remote);
      }

      setBackupEnabled(Boolean(data.enabled));
      setBackupRcloneRemote((data.rclone_remote || remote).trim());
      setBackupSettingsStatus(`${reloginUsed ? "已自动重新登录，" : ""}主控备份设置已保存`);
    } catch (error) {
      const msg = error instanceof Error ? error.message : "unknown error";
      setBackupSettingsStatus(`保存主控备份设置失败：${msg}`);
    } finally {
      setIsSavingBackupSettings(false);
    }
  }

  async function testBackupSettings(baseUrlInput: string, token: string, rcloneRemoteInput: string, reauthenticate?: () => Promise<string>) {
    const base = normalizeBaseUrl(baseUrlInput);
    if (!base) {
      setBackupSettingsStatus("Controller URL is required");
      return;
    }
    if (!token) {
      setBackupSettingsStatus("未登录，无法测试主控备份设置");
      return;
    }

    const remote = rcloneRemoteInput.trim();
    if (!remote) {
      setBackupSettingsStatus("请先填写 rclone 远端路径后再测试");
      return;
    }

    setIsTestingBackupSettings(true);
    setBackupSettingsStatus("正在测试 rclone 远端连通性...");
    try {
      let activeToken = token;
      let reloginUsed = false;
      try {
        await testControllerBackupSettings(base, activeToken, remote);
      } catch (error) {
        if (!reauthenticate || !isUnauthorizedError(error)) {
          throw error;
        }
        setBackupSettingsStatus("会话已过期，正在自动重新登录并重试测试...");
        activeToken = await reauthenticate();
        reloginUsed = true;
        await testControllerBackupSettings(base, activeToken, remote);
      }
      setBackupSettingsStatus(`${reloginUsed ? "已自动重新登录，" : ""}rclone 远端测试成功`);
    } catch (error) {
      const msg = error instanceof Error ? error.message : "unknown error";
      setBackupSettingsStatus(`rclone 远端测试失败：${msg}`);
    } finally {
      setIsTestingBackupSettings(false);
    }
  }

  function ensureUpgradeProject(projectInput: string): string | null {
    const project = projectInput.trim();
    if (!project) {
      updateManagerUpgradeStatus("请先填写 GitHub 项目地址（owner/repo 或 github URL）");
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
    updateManagerUpgradeStatus("直连检查中：正在请求 GitHub 最新 Release...");
    appendManagerUpgradeMessage(`管理端直连检查开始：project=${project}`);
    try {
      const release = await fetchJson<ReleaseInfo>(`/upgrade/release?project=${encodeURIComponent(project)}`);
      setDirectRelease(release);
      updateManagerUpgradeStatus(`直连检查完成：latest=${release.tag_name}, assets=${release.assets.length}`);
      appendManagerUpgradeMessage(`管理端直连检查完成：latest=${release.tag_name} assets=${release.assets.length}`);
    } catch (error) {
      const hint = resolveUpgradeErrorHint(error);
      updateManagerUpgradeStatus(formatUpgradeFailureStatus("直连检查失败", hint));
      appendManagerUpgradeMessage(formatUpgradeFailureLog("管理端直连检查失败", hint));
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
      updateManagerUpgradeStatus("未登录，无法执行代理检查");
      return;
    }

    setIsCheckingProxy(true);
    updateManagerUpgradeStatus("代理检查中：正在通过主控请求 GitHub 最新 Release...");
    appendManagerUpgradeMessage(`管理端代理检查开始：project=${project}`);
    try {
      let activeToken = token;
      let reloginUsed = false;
      let release: ReleaseInfo;
      try {
        release = await fetchJson<ReleaseInfo>(`/upgrade/release?project=${encodeURIComponent(project)}`);
      } catch (error) {
        if (!reauthenticate || !isUnauthorizedError(error)) {
          throw error;
        }

        updateManagerUpgradeStatus("会话已过期，正在自动重新登录并重试代理检查...");
        appendManagerUpgradeMessage("管理端代理检查：会话过期，自动重新登录并重试");
        activeToken = await reauthenticate();
        reloginUsed = true;
        release = await fetchJson<ReleaseInfo>(`/upgrade/release?project=${encodeURIComponent(project)}`);
      }

      setProxyRelease(release);
      updateManagerUpgradeStatus(`${reloginUsed ? "已自动重新登录，" : ""}代理检查完成：latest=${release.tag_name}, assets=${release.assets.length}`);
      appendManagerUpgradeMessage(`管理端代理检查完成：latest=${release.tag_name} assets=${release.assets.length}`);
    } catch (error) {
      const hint = resolveUpgradeErrorHint(error);
      updateManagerUpgradeStatus(formatUpgradeFailureStatus("代理检查失败", hint));
      appendManagerUpgradeMessage(formatUpgradeFailureLog("管理端代理检查失败", hint));
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
    updateManagerUpgradeStatus("直连升级中：下载并应用管理端更新...");
    beginProgress(setManagerUpgradeProgress, "准备升级管理端");
    appendManagerUpgradeMessage(`管理端直连升级开始：project=${project}`);
    let lastManagerProgressLogKey = "";
    const stopPolling = startPolling(
      async () => ({ active: false, phase: "idle", percent: 0, message: "" }),
      setManagerUpgradeProgress,
      (progress) => {
        const bucket = progress.phase === "download" ? Math.floor(progress.percent / 5) * 5 : progress.percent;
        const key = `${progress.phase}|${bucket}|${progress.message || ""}`;
        if (key === lastManagerProgressLogKey) {
          return;
        }
        lastManagerProgressLogKey = key;
        appendManagerUpgradeMessage(formatProgressLog("管理端升级进度", progress));
      },
    );
    try {
      throw new Error("管理端自身的二进制升级已停止支持，请手动下载新版替换或使用安装脚本更新。");
    } catch (error) {
      const hint = resolveUpgradeErrorHint(error);
      updateManagerUpgradeStatus(formatUpgradeFailureStatus("直连升级失败", hint));
      appendManagerUpgradeMessage(formatUpgradeFailureLog("管理端直连升级失败", hint));
    } finally {
      stopPolling();
      resetProgress(setManagerUpgradeProgress);
      setIsUpgradingManager(false);
    }
  }

  async function upgradeManagerProxy(baseUrlInput: string, token: string, projectInput: string, reauthenticate?: () => Promise<string>) {
    const project = ensureUpgradeProject(projectInput);
    if (!project) {
      return;
    }
    if (!token) {
      updateManagerUpgradeStatus("未登录，无法执行代理升级");
      return;
    }

    setIsUpgradingManager(true);
    updateManagerUpgradeStatus("代理升级中：通过主控下载并转发升级包...");
    beginProgress(setManagerUpgradeProgress, "准备升级管理端");
    appendManagerUpgradeMessage(`管理端代理升级开始：project=${project}`);
    let lastManagerProgressLogKey = "";
    const stopPolling = startPolling(
      async () => ({ active: false, phase: "idle", percent: 0, message: "" }),
      setManagerUpgradeProgress,
      (progress) => {
        const bucket = progress.phase === "download" ? Math.floor(progress.percent / 5) * 5 : progress.percent;
        const key = `${progress.phase}|${bucket}|${progress.message || ""}`;
        if (key === lastManagerProgressLogKey) {
          return;
        }
        lastManagerProgressLogKey = key;
        appendManagerUpgradeMessage(formatProgressLog("管理端升级进度", progress));
      },
    );
    try {
      throw new Error("管理端自身的二进制升级已停止支持，请手动下载新版替换或使用安装脚本更新。");      // Removed for stub
    } catch (error) {
      const hint = resolveUpgradeErrorHint(error);
      updateManagerUpgradeStatus(formatUpgradeFailureStatus("代理升级失败", hint));
      appendManagerUpgradeMessage(formatUpgradeFailureLog("管理端代理升级失败", hint));
    } finally {
      stopPolling();
      resetProgress(setManagerUpgradeProgress);
      setIsUpgradingManager(false);
    }
  }

  function clearUpgradeMessages() {
    setVersionStatus("");
    setUpgradeStatus("");
    setManagerUpgradeStatus("");
    setMergedUpgradeStatus("");
    setBackupSettingsStatus("");
    setControllerUpgradeMessages([]);
    setManagerUpgradeMessages([]);
    setMergedUpgradeMessages([]);
    resetProgress(setControllerUpgradeProgress);
    resetProgress(setManagerUpgradeProgress);
  }

  return {
    managerVersion,
    controllerVersion,
    controllerLatestVersion,
    versionStatus,
    upgradeStatus,
    mergedUpgradeStatus,
    controllerUpgradeProgress,
    controllerUpgradeMessages,
    mergedUpgradeMessages,
    isUpgradingController,
    directRelease,
    proxyRelease,
    managerUpgradeStatus,
    managerUpgradeProgress,
    managerUpgradeMessages,
    backupEnabled,
    backupRcloneRemote,
    backupSettingsStatus,
    isLoadingBackupSettings,
    isSavingBackupSettings,
    isTestingBackupSettings,
    isCheckingDirect,
    isCheckingProxy,
    isUpgradingManager,
    refreshSystemVersions,
    refreshBackupSettings,
    saveBackupSettings,
    testBackupSettings,
    upgradeController,
    checkManagerReleaseDirect,
    checkManagerReleaseProxy,
    upgradeManagerDirect,
    upgradeManagerProxy,
    clearUpgradeMessages,
  };
}
