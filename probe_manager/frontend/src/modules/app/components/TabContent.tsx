import { NetworkAssistantTab } from "./NetworkAssistantTab";
import { OverviewTab } from "./OverviewTab";
import { PlaceholderTab } from "./PlaceholderTab";
import { SystemSettingsTab } from "./SystemSettingsTab";
import type { NetworkAssistantStatus, ReleaseInfo, TabKey, UpgradeProgress } from "../types";

type TabContentProps = {
  activeTab: TabKey;
  username: string;
  userRole: string;
  certType: string;
  privateKeyStatus: string;
  privateKeyPath: string;
  wsStatus: string;
  serverStatus: string;
  adminStatus: string;
  onPingServer: () => void;
  onCheckAdminStatus: () => void;
  onRefreshPrivateKeyStatus: () => void;
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
  networkAssistantStatus: NetworkAssistantStatus;
  networkSelectedNode: string;
  onNetworkSelectedNodeChange: (value: string) => void;
  isOperatingNetworkAssistant: boolean;
  networkOperateStatus: string;
  onRefreshNetworkAssistantStatus: () => void;
  onSwitchNetworkDirect: () => void;
  onSwitchNetworkGlobal: () => void;
  onRestoreNetworkDirect: () => void;
};

export function TabContent(props: TabContentProps) {
  switch (props.activeTab) {
    case "overview":
      return (
        <OverviewTab
          username={props.username}
          userRole={props.userRole}
          certType={props.certType}
          privateKeyStatus={props.privateKeyStatus}
          privateKeyPath={props.privateKeyPath}
          wsStatus={props.wsStatus}
          serverStatus={props.serverStatus}
          adminStatus={props.adminStatus}
          onPingServer={props.onPingServer}
          onCheckAdminStatus={props.onCheckAdminStatus}
          onRefreshPrivateKeyStatus={props.onRefreshPrivateKeyStatus}
        />
      );
    case "probe-status":
      return <PlaceholderTab title="探针状态" description="该页面将用于展示探针在线状态与基础健康信息。" />;
    case "probe-manage":
      return <PlaceholderTab title="探针管理" description="该页面将用于展示探针增删改查、分组与策略下发能力。" />;
    case "link-manage":
      return <PlaceholderTab title="链路管理" description="该页面将用于展示链路拓扑、探测任务与阈值配置。" />;
    case "network-assistant":
      return (
        <NetworkAssistantTab
          status={props.networkAssistantStatus}
          selectedNode={props.networkSelectedNode}
          onSelectedNodeChange={props.onNetworkSelectedNodeChange}
          isOperating={props.isOperatingNetworkAssistant}
          operateStatus={props.networkOperateStatus}
          onRefreshStatus={props.onRefreshNetworkAssistantStatus}
          onSwitchDirect={props.onSwitchNetworkDirect}
          onSwitchGlobal={props.onSwitchNetworkGlobal}
          onRestoreDirect={props.onRestoreNetworkDirect}
        />
      );
    case "system-settings":
      return (
        <SystemSettingsTab
          managerVersion={props.managerVersion}
          controllerVersion={props.controllerVersion}
          controllerLatestVersion={props.controllerLatestVersion}
          versionStatus={props.versionStatus}
          upgradeStatus={props.upgradeStatus}
          controllerUpgradeProgress={props.controllerUpgradeProgress}
          isUpgradingController={props.isUpgradingController}
          isUpgradingManager={props.isUpgradingManager}
          onRefreshSystemVersions={props.onRefreshSystemVersions}
          onUpgradeController={props.onUpgradeController}
          upgradeProject={props.upgradeProject}
          onUpgradeProjectChange={props.onUpgradeProjectChange}
          isCheckingDirect={props.isCheckingDirect}
          isCheckingProxy={props.isCheckingProxy}
          sessionToken={props.sessionToken}
          onCheckManagerReleaseDirect={props.onCheckManagerReleaseDirect}
          onUpgradeManagerDirect={props.onUpgradeManagerDirect}
          onCheckManagerReleaseProxy={props.onCheckManagerReleaseProxy}
          onUpgradeManagerProxy={props.onUpgradeManagerProxy}
          directRelease={props.directRelease}
          proxyRelease={props.proxyRelease}
          managerUpgradeStatus={props.managerUpgradeStatus}
          managerUpgradeProgress={props.managerUpgradeProgress}
        />
      );
    default:
      return null;
  }
}
