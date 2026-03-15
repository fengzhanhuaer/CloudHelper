import type { ReleaseInfo, UpgradeProgress } from "../types";

type SystemSettingsTabProps = {
  managerVersion: string;
  controllerVersion: string;
  controllerLatestVersion: string;
  versionStatus: string;
  upgradeStatus: string;
  controllerUpgradeProgress: UpgradeProgress;
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
  managerUpgradeStatus: string;
  managerUpgradeProgress: UpgradeProgress;
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

export function SystemSettingsTab(props: SystemSettingsTabProps) {
  return (
    <div className="content-block">
      <h2>系统设置</h2>

      <div className="identity-card">
        <div>管理程序版本：{props.managerVersion}</div>
        <div>主控当前版本：{props.controllerVersion}</div>
        <div>主控最新版本：{props.controllerLatestVersion || "未知"}</div>
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
      <div className="status">{props.upgradeStatus}</div>
      {(props.isUpgradingController || props.controllerUpgradeProgress.percent > 0) && (
        <ProgressLine title="主控升级进度" progress={props.controllerUpgradeProgress} />
      )}

      <div className="identity-card" style={{ marginTop: 14 }}>
        <div>管理端升级项目（GitHub）：</div>
        <input
          className="input"
          value={props.upgradeProject}
          onChange={(e) => props.onUpgradeProjectChange(e.target.value)}
          placeholder="owner/repo 或 https://github.com/owner/repo"
        />
        <div>提示：代理模式会通过已鉴权的 `/api/admin/proxy/*` 接口进行。</div>
      </div>

      <div className="content-actions">
        <button className="btn" onClick={props.onCheckManagerReleaseDirect} disabled={props.isCheckingDirect || props.isUpgradingManager || props.isUpgradingController}>
          {props.isCheckingDirect ? "直连检查中..." : "直连检查"}
        </button>
        <button className="btn" onClick={props.onUpgradeManagerDirect} disabled={props.isUpgradingManager || props.isUpgradingController}>
          {props.isUpgradingManager ? "升级中..." : "直连升级"}
        </button>
      </div>

      <div className="content-actions">
        <button className="btn" onClick={props.onCheckManagerReleaseProxy} disabled={props.isCheckingProxy || props.isUpgradingManager || props.isUpgradingController || !props.sessionToken}>
          {props.isCheckingProxy ? "代理检查中..." : "代理检查"}
        </button>
        <button className="btn" onClick={props.onUpgradeManagerProxy} disabled={props.isUpgradingManager || props.isUpgradingController || !props.sessionToken}>
          {props.isUpgradingManager ? "升级中..." : "代理升级"}
        </button>
      </div>

      <ReleaseInfoStatus title="直连结果" release={props.directRelease} />
      <ReleaseInfoStatus title="代理结果" release={props.proxyRelease} />
      <div className="status">{props.managerUpgradeStatus}</div>
      {(props.isUpgradingManager || props.managerUpgradeProgress.percent > 0) && (
        <ProgressLine title="管理端升级进度" progress={props.managerUpgradeProgress} />
      )}
    </div>
  );
}
