import { CloudflareAssistantTab } from "./CloudflareAssistantTab";
import { LinkManageTab } from "./LinkManageTab";
import { LogViewerTab } from "./LogViewerTab";
import { NetworkAssistantTab } from "./NetworkAssistantTab";
import { OverviewTab } from "./OverviewTab";
import { ProbeManageTab } from "./ProbeManageTab";
import { SystemSettingsTab } from "./SystemSettingsTab";
import { TGAssistantTab } from "./TGAssistantTab";
import type { LogSource, NetworkAssistantLogFilterSource, NetworkAssistantStatus, ReleaseInfo, TabKey, UpgradeProgress } from "../types";

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
  controllerBaseUrl: string;
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
  backupEnabled: boolean;
  backupRcloneRemote: string;
  backupSettingsStatus: string;
  isLoadingBackupSettings: boolean;
  isSavingBackupSettings: boolean;
  isTestingBackupSettings: boolean;
  onRefreshBackupSettings: () => void;
  onSaveBackupSettings: (enabled: boolean, value: string) => void;
  onTestBackupSettings: (value: string) => void;
  networkAssistantStatus: NetworkAssistantStatus;
  networkSelectedNode: string;
  onNetworkSelectedNodeChange: (value: string) => void;
  isOperatingNetworkAssistant: boolean;
  networkOperateStatus: string;
  onRefreshNetworkAssistantStatus: () => void;
  onSwitchNetworkDirect: () => void;
  onSwitchNetworkGlobal: () => void;
  onInstallNetworkTUN: () => void;
  onEnableNetworkTUN: () => void;
  onRestoreNetworkDirect: () => void;
  networkLogLines: number;
  onNetworkLogLinesChange: (value: number) => void;
  isLoadingNetworkLogs: boolean;
  networkLogStatus: string;
  networkLogCopyStatus: string;
  networkLogContent: string;
  networkLogSourceFilter: NetworkAssistantLogFilterSource;
  onNetworkLogSourceFilterChange: (value: NetworkAssistantLogFilterSource) => void;
  networkLogCategoryFilter: string;
  onNetworkLogCategoryFilterChange: (value: string) => void;
  networkLogCategories: string[];
  networkLogVisibleCount: number;
  networkLogTotalCount: number;
  networkLogAutoScroll: boolean;
  onNetworkLogAutoScrollChange: (value: boolean) => void;
  onRefreshNetworkLogs: () => void;
  onCopyNetworkLogs: () => void;
  logSource: LogSource;
  onLogSourceChange: (value: LogSource) => void;
  logLines: number;
  onLogLinesChange: (value: number) => void;
  logSinceMinutes: number;
  onLogSinceMinutesChange: (value: number) => void;
  logAutoScroll: boolean;
  onLogAutoScrollChange: (value: boolean) => void;
  isLoadingLogs: boolean;
  logStatus: string;
  logCopyStatus: string;
  logFilePath: string;
  logContent: string;
  onRefreshLogs: () => void;
  onCopyLogs: () => void;
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
    case "probe-manage":
      return <ProbeManageTab controllerBaseUrl={props.controllerBaseUrl} sessionToken={props.sessionToken} />;
    case "link-manage":
      return <LinkManageTab controllerBaseUrl={props.controllerBaseUrl} sessionToken={props.sessionToken} />;
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
          onInstallTUN={props.onInstallNetworkTUN}
          onEnableTUN={props.onEnableNetworkTUN}
          onRestoreDirect={props.onRestoreNetworkDirect}
          logLines={props.networkLogLines}
          onLogLinesChange={props.onNetworkLogLinesChange}
          isLoadingLogs={props.isLoadingNetworkLogs}
          logStatus={props.networkLogStatus}
          logCopyStatus={props.networkLogCopyStatus}
          logContent={props.networkLogContent}
          logSourceFilter={props.networkLogSourceFilter}
          onLogSourceFilterChange={props.onNetworkLogSourceFilterChange}
          logCategoryFilter={props.networkLogCategoryFilter}
          onLogCategoryFilterChange={props.onNetworkLogCategoryFilterChange}
          logCategories={props.networkLogCategories}
          logVisibleCount={props.networkLogVisibleCount}
          logTotalCount={props.networkLogTotalCount}
          logAutoScroll={props.networkLogAutoScroll}
          onLogAutoScrollChange={props.onNetworkLogAutoScrollChange}
          onRefreshLogs={props.onRefreshNetworkLogs}
          onCopyLogs={props.onCopyNetworkLogs}
        />
      );
    case "cloudflare-assistant":
      return <CloudflareAssistantTab controllerBaseUrl={props.controllerBaseUrl} sessionToken={props.sessionToken} />;
    case "tg-assistant":
      return <TGAssistantTab controllerBaseUrl={props.controllerBaseUrl} sessionToken={props.sessionToken} />;
    case "log-viewer":
      return (
        <LogViewerTab
          source={props.logSource}
          onSourceChange={props.onLogSourceChange}
          lines={props.logLines}
          onLinesChange={props.onLogLinesChange}
          sinceMinutes={props.logSinceMinutes}
          onSinceMinutesChange={props.onLogSinceMinutesChange}
          autoScroll={props.logAutoScroll}
          onAutoScrollChange={props.onLogAutoScrollChange}
          isLoading={props.isLoadingLogs}
          status={props.logStatus}
          copyStatus={props.logCopyStatus}
          logFilePath={props.logFilePath}
          content={props.logContent}
          onRefresh={props.onRefreshLogs}
          onCopy={props.onCopyLogs}
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
          backupEnabled={props.backupEnabled}
          backupRcloneRemote={props.backupRcloneRemote}
          backupSettingsStatus={props.backupSettingsStatus}
          isLoadingBackupSettings={props.isLoadingBackupSettings}
          isSavingBackupSettings={props.isSavingBackupSettings}
          isTestingBackupSettings={props.isTestingBackupSettings}
          onRefreshBackupSettings={props.onRefreshBackupSettings}
          onSaveBackupSettings={props.onSaveBackupSettings}
          onTestBackupSettings={props.onTestBackupSettings}
        />
      );
    default:
      return null;
  }
}
