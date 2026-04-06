import { useEffect, useState } from "react";
import type { ReleaseInfo, UpgradeProgress } from "../types";

type SystemSettingsTabProps = {
  managerVersion: string;
  controllerVersion: string;
  controllerLatestVersion: string;
  versionStatus: string;
  mergedUpgradeStatus: string;
  controllerUpgradeProgress: UpgradeProgress;
  mergedUpgradeMessages: string[];
  isUpgradingController: boolean;
  isUpgradingManager: boolean;
  onRefreshSystemVersions: () => void;
  onUpgradeController: () => void;
  upgradeProject: string;
  onUpgradeProjectChange: (value: string) => void;
  isCheckingDirect: boolean;
  isCheckingProxy: boolean;
  sessionToken: string;
  onCheckManagerReleaseDirect: () => void;
  onUpgradeManagerDirect: () => void;
  onCheckManagerReleaseProxy: () => void;
  onUpgradeManagerProxy: () => void;
  directRelease: ReleaseInfo | null;
  proxyRelease: ReleaseInfo | null;
  managerUpgradeProgress: UpgradeProgress;
  controllerPreferredIP: string;
  controllerPreferredIPStatus: string;
  isLoadingControllerPreferredIP: boolean;
  isSavingControllerPreferredIP: boolean;
  onRefreshControllerPreferredIP: () => void;
  onSaveControllerPreferredIP: (value: string) => void;
  backupEnabled: boolean;
  backupRcloneRemote: string;
  backupSettingsStatus: string;
  isLoadingBackupSettings: boolean;
  isSavingBackupSettings: boolean;
  isTestingBackupSettings: boolean;
  onRefreshBackupSettings: () => void;
  onSaveBackupSettings: (enabled: boolean, value: string) => void;
  onTestBackupSettings: (value: string) => void;
  aiDebugListenEnabled: boolean;
  aiDebugListenStatus: string;
  isLoadingAIDebugListenEnabled: boolean;
  isSavingAIDebugListenEnabled: boolean;
  onRefreshAIDebugListenEnabled: () => void;
  onSetAIDebugListenEnabled: (enabled: boolean) => void;
};

function ProgressLine(props: { title: string; progress: UpgradeProgress }) {
  return (
    <div className="status">
      <strong>{props.title}</strong>
      {`：${props.progress.percent}% ${props.progress.message || ""}`}
      <div className="progress-bar">
        <div className="progress-bar-fill" style={{ width: `${props.progress.percent}%` }} />
      </div>
    </div>
  );
}

function ReleaseInfoStatus(props: { title: string; release: ReleaseInfo | null }) {
  return (
    <div className="status">
      <strong>{props.title}</strong>
      {!props.release ? "：未查询" : `：${props.release.repo} @ ${props.release.tag_name}，assets=${props.release.assets.length}`}
    </div>
  );
}

function UpgradeTimeline(props: { title: string; lines: string[] }) {
  return (
    <div className="status">
      <strong>{props.title}</strong>
      <pre className="log-viewer-output" style={{ minHeight: 120, maxHeight: 240, marginTop: 8 }}>
        {props.lines.length > 0 ? props.lines.join("\n") : "暂无升级消息"}
      </pre>
    </div>
  );
}

export function SystemSettingsTab(props: SystemSettingsTabProps) {
  const [subTab, setSubTab] = useState<"upgrade" | "controller" | "ai-debug">("upgrade");
  const [controllerPreferredIPInput, setControllerPreferredIPInput] = useState(props.controllerPreferredIP || "");
  const [backupEnabledInput, setBackupEnabledInput] = useState(Boolean(props.backupEnabled));
  const [backupRemoteInput, setBackupRemoteInput] = useState(props.backupRcloneRemote || "");

  useEffect(() => {
    setControllerPreferredIPInput(props.controllerPreferredIP || "");
  }, [props.controllerPreferredIP]);

  useEffect(() => {
    setBackupEnabledInput(Boolean(props.backupEnabled));
  }, [props.backupEnabled]);

  useEffect(() => {
    setBackupRemoteInput(props.backupRcloneRemote || "");
  }, [props.backupRcloneRemote]);

  return (
    <div className="content-block">
      <h2>系统设置</h2>

      <div className="subtab-list" style={{ marginBottom: 12 }}>
        <button className={`subtab-btn ${subTab === "upgrade" ? "active" : ""}`} onClick={() => setSubTab("upgrade")}>升级设置</button>
        <button className={`subtab-btn ${subTab === "controller" ? "active" : ""}`} onClick={() => setSubTab("controller")}>主控设置</button>
        <button className={`subtab-btn ${subTab === "ai-debug" ? "active" : ""}`} onClick={() => setSubTab("ai-debug")}>AI调试</button>
      </div>

      {subTab === "upgrade" && (
        <>
          <div className="identity-card">
            <div>管理程序版本：{props.managerVersion}　|　主控当前版本：{props.controllerVersion}　|　主控最新版本：{props.controllerLatestVersion || "未知"}</div>
          </div>

          <div className="content-actions">
            <button className="btn" onClick={props.onRefreshSystemVersions} disabled={props.isUpgradingController || props.isUpgradingManager}>
              刷新版本信息
            </button>
            <button className="btn" onClick={props.onUpgradeController} disabled={props.isUpgradingController || props.isUpgradingManager}>
              {props.isUpgradingController ? "主控升级中..." : "升级主控"}
            </button>
          </div>

          <div className="status">{props.versionStatus}</div>
          <div className="status">{props.mergedUpgradeStatus}</div>
          {(props.isUpgradingController || props.controllerUpgradeProgress.percent > 0) && (
            <ProgressLine title="主控升级进度" progress={props.controllerUpgradeProgress} />
          )}

          <div className="content-actions">
            <button className="btn" onClick={props.onCheckManagerReleaseDirect} disabled={props.isCheckingDirect || props.isUpgradingManager || props.isUpgradingController}>
              {props.isCheckingDirect ? "直连检查中..." : "直连检查"}
            </button>
            <button className="btn" onClick={props.onUpgradeManagerDirect} disabled={props.isUpgradingManager || props.isUpgradingController}>
              {props.isUpgradingManager ? "升级中..." : "直连升级"}
            </button>
            <button className="btn" onClick={props.onCheckManagerReleaseProxy} disabled={props.isCheckingProxy || props.isUpgradingManager || props.isUpgradingController || !props.sessionToken}>
              {props.isCheckingProxy ? "代理检查中..." : "代理检查"}
            </button>
            <button className="btn" onClick={props.onUpgradeManagerProxy} disabled={props.isUpgradingManager || props.isUpgradingController || !props.sessionToken}>
              {props.isUpgradingManager ? "升级中..." : "代理升级"}
            </button>
          </div>

          <ReleaseInfoStatus title="直连结果" release={props.directRelease} />
          <ReleaseInfoStatus title="代理结果" release={props.proxyRelease} />
          {(props.isUpgradingManager || props.managerUpgradeProgress.percent > 0) && (
            <ProgressLine title="管理端升级进度" progress={props.managerUpgradeProgress} />
          )}
          <UpgradeTimeline title="升级消息" lines={props.mergedUpgradeMessages} />
        </>
      )}

      {subTab === "controller" && (
        <>
          <div className="identity-card" style={{ marginBottom: 12 }}>
            <div><strong>controller_ip</strong></div>
            <div className="status" style={{ marginTop: 8 }}>
              独立配置字段，留空表示不配置。若 controller_url 的 host 使用域名且这里填写了 IP，则连接主控时会直接按该 IP 拨号，跳过 DNS 解析；controller_url 本身保持不变。
            </div>
            <div className="row" style={{ marginBottom: 0 }}>
              <label>controller_ip</label>
              <input
                className="input"
                value={controllerPreferredIPInput}
                onChange={(event) => setControllerPreferredIPInput(event.target.value)}
                placeholder="例如：203.0.113.10；留空表示不配置"
                disabled={props.isLoadingControllerPreferredIP || props.isSavingControllerPreferredIP}
              />
            </div>
            <div className="content-actions inline">
              <button
                className="btn"
                onClick={props.onRefreshControllerPreferredIP}
                disabled={props.isLoadingControllerPreferredIP || props.isSavingControllerPreferredIP}
              >
                {props.isLoadingControllerPreferredIP ? "读取中..." : "读取设置"}
              </button>
              <button
                className="btn"
                onClick={() => props.onSaveControllerPreferredIP(controllerPreferredIPInput)}
                disabled={props.isLoadingControllerPreferredIP || props.isSavingControllerPreferredIP}
              >
                {props.isSavingControllerPreferredIP ? "保存中..." : "保存设置"}
              </button>
            </div>
          </div>
          <div className="status">{props.controllerPreferredIPStatus}</div>

          <div className="identity-card" style={{ marginTop: 12 }}>
            <div><strong>主控备份设置</strong></div>
            <label className="probe-direct-toggle" style={{ marginTop: 0 }}>
              <input
                type="checkbox"
                checked={backupEnabledInput}
                onChange={(event) => setBackupEnabledInput(event.target.checked)}
                disabled={props.isLoadingBackupSettings || props.isSavingBackupSettings}
              />
              <span>开启 rclone 异地备份同步</span>
            </label>
            <div className="row" style={{ marginBottom: 0 }}>
              <label>rclone 远端</label>
              <input
                className="input"
                value={backupRemoteInput}
                onChange={(event) => setBackupRemoteInput(event.target.value)}
                placeholder="例如：remote:/probe_controller"
                disabled={props.isLoadingBackupSettings || props.isSavingBackupSettings || !backupEnabledInput}
              />
            </div>
            <div className="content-actions inline">
              <button className="btn" onClick={props.onRefreshBackupSettings} disabled={props.isLoadingBackupSettings || props.isSavingBackupSettings}>
                {props.isLoadingBackupSettings ? "读取中..." : "读取设置"}
              </button>
              <button
                className="btn"
                onClick={() => props.onSaveBackupSettings(backupEnabledInput, backupRemoteInput)}
                disabled={props.isLoadingBackupSettings || props.isSavingBackupSettings}
              >
                {props.isSavingBackupSettings ? "保存中..." : "保存设置"}
              </button>
              <button
                className="btn"
                onClick={() => props.onTestBackupSettings(backupRemoteInput)}
                disabled={props.isLoadingBackupSettings || props.isSavingBackupSettings || props.isTestingBackupSettings || !backupEnabledInput}
              >
                {props.isTestingBackupSettings ? "测试中..." : "测试连接"}
              </button>
            </div>
          </div>
          <div className="status">{props.backupSettingsStatus}</div>
        </>
      )}

      {subTab === "ai-debug" && (
        <>
          <div className="identity-card" style={{ marginBottom: 12 }}>
            <div><strong>AI 调试入口</strong></div>
            <div className="status" style={{ marginTop: 8 }}>
              监听地址：0.0.0.0:16031，默认关闭，用于后续向 AI 提供实时调试信息。
            </div>
            <label className="probe-direct-toggle" style={{ marginTop: 12 }}>
              <input
                type="checkbox"
                checked={props.aiDebugListenEnabled}
                onChange={(event) => props.onSetAIDebugListenEnabled(event.target.checked)}
                disabled={props.isLoadingAIDebugListenEnabled || props.isSavingAIDebugListenEnabled}
              />
              <span>启用 AI 调试 HTTP 入口</span>
            </label>
            <div className="content-actions inline">
              <button
                className="btn"
                onClick={props.onRefreshAIDebugListenEnabled}
                disabled={props.isLoadingAIDebugListenEnabled || props.isSavingAIDebugListenEnabled}
              >
                {props.isLoadingAIDebugListenEnabled ? "读取中..." : "读取状态"}
              </button>
            </div>
          </div>
          <div className="status">{props.aiDebugListenStatus}</div>
        </>
      )}
    </div>
  );
}
