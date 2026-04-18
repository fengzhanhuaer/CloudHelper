import { CloudflareAssistantTab } from "./CloudflareAssistantTab";
import { LogViewerTab } from "./LogViewerTab";
import { NetworkAssistantTab } from "./NetworkAssistantTab";
import { OverviewTab } from "./OverviewTab";
import { ProbeManageTab } from "./ProbeManageTab";
import { SystemSettingsTab } from "./SystemSettingsTab";
import { TGAssistantTab } from "./TGAssistantTab";
import type {
  LogSource,
  NetworkAssistantDNSCacheEntry,
  NetworkAssistantLogFilterSource,
  NetworkAssistantRuleAction,
  NetworkAssistantRuleConfig,
  NetworkAssistantStatus,
  NetworkProcessEvent,
  NetworkProcessInfo,
  ReleaseInfo,
  TabKey,
  UpgradeProgress,
} from "../types";

type TabContentProps = {
  activeTab: TabKey;
  username: string;
  userRole: string;
  certType: string;
  wsStatus: string;
  serverStatus: string;
  adminStatus: string;
  onPingServer: () => void;
  onCheckAdminStatus: () => void;
  controllerBaseUrl: string;
  controllerBaseUrlStatus: string;
  isLoadingControllerBaseUrl: boolean;
  isSavingControllerBaseUrl: boolean;
  onRefreshControllerBaseUrl: () => void;
  onSaveControllerBaseUrl: (value: string) => void;
  controllerPreferredIP: string;
  controllerPreferredIPStatus: string;
  isLoadingControllerPreferredIP: boolean;
  isSavingControllerPreferredIP: boolean;
  onRefreshControllerPreferredIP: () => void;
  onSaveControllerPreferredIP: (value: string) => void;
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
  networkAssistantStatus: NetworkAssistantStatus;
  isOperatingNetworkAssistant: boolean;
  networkOperateStatus: string;
  onRefreshNetworkAssistantStatus: () => void;
  onSwitchNetworkDirect: () => void;
  onSwitchNetworkTUN: () => void;
  networkRuleConfig: NetworkAssistantRuleConfig | null;
  isLoadingNetworkRuleConfig: boolean;
  networkRuleConfigStatus: string;
  isSyncingNetworkRuleRoutes: boolean;
  networkRuleRoutesSyncStatus: string;
  onRefreshNetworkRuleConfig: () => void;
  onUploadNetworkRuleRoutes: () => void;
  onDownloadNetworkRuleRoutes: () => void;
  onSetNetworkRulePolicy: (group: string, action: NetworkAssistantRuleAction, tunnelNodeID?: string) => void;
  onInstallNetworkTUN: () => void;
  onEnableNetworkTUN: () => void;
  onCloseNetworkTUN: () => void;
  networkDNSCacheEntries: NetworkAssistantDNSCacheEntry[];
  networkDNSCacheQuery: string;
  isNetworkDNSCacheLoading: boolean;
  networkDNSCacheStatus: string;
  onNetworkDNSCacheQueryChange: (value: string) => void;
  onQueryNetworkDNSCache: (query: string) => void;
  networkProcessList: NetworkProcessInfo[];
  isNetworkProcessListLoading: boolean;
  networkProcessListStatus: string;
  networkMonitorProcess: string;
  isNetworkMonitoring: boolean;
  networkProcessEvents: NetworkProcessEvent[];
  networkProcessEventsStatus: string;
  onRefreshNetworkProcessList: () => void;
  onSelectNetworkMonitorProcess: (name: string) => void;
  onStartNetworkMonitor: () => void;
  onStopNetworkMonitor: () => void;
  onClearNetworkMonitorEvents: () => void;
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
  logMinLevel: "realtime" | "normal" | "warning" | "error";
  onLogMinLevelChange: (value: "realtime" | "normal" | "warning" | "error") => void;
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
          wsStatus={props.wsStatus}
          serverStatus={props.serverStatus}
          adminStatus={props.adminStatus}
          onPingServer={props.onPingServer}
          onCheckAdminStatus={props.onCheckAdminStatus}
        />
      );
    case "probe-manage":
      return <ProbeManageTab controllerBaseUrl={props.controllerBaseUrl} sessionToken={props.sessionToken} />;
    case "network-assistant":
      return (
        <NetworkAssistantTab
          controllerBaseUrl={props.controllerBaseUrl}
          sessionToken={props.sessionToken}
          status={props.networkAssistantStatus}
          isOperating={props.isOperatingNetworkAssistant}
          operateStatus={props.networkOperateStatus}
          onRefreshStatus={props.onRefreshNetworkAssistantStatus}
          onSwitchDirect={props.onSwitchNetworkDirect}
          onSwitchTUN={props.onSwitchNetworkTUN}
          ruleConfig={props.networkRuleConfig}
          isLoadingRuleConfig={props.isLoadingNetworkRuleConfig}
          ruleConfigStatus={props.networkRuleConfigStatus}
          isSyncingRuleRoutes={props.isSyncingNetworkRuleRoutes}
          ruleRoutesSyncStatus={props.networkRuleRoutesSyncStatus}
          onRefreshRuleConfig={props.onRefreshNetworkRuleConfig}
          onUploadRuleRoutes={props.onUploadNetworkRuleRoutes}
          onDownloadRuleRoutes={props.onDownloadNetworkRuleRoutes}
          onSetRulePolicy={props.onSetNetworkRulePolicy}
          onInstallTUN={props.onInstallNetworkTUN}
          onEnableTUN={props.onEnableNetworkTUN}
          onCloseTUN={props.onCloseNetworkTUN}
          dnsCacheEntries={props.networkDNSCacheEntries}
          dnsCacheQuery={props.networkDNSCacheQuery}
          isDNSCacheLoading={props.isNetworkDNSCacheLoading}
          dnsCacheStatus={props.networkDNSCacheStatus}
          onDNSCacheQueryChange={props.onNetworkDNSCacheQueryChange}
          onQueryDNSCache={props.onQueryNetworkDNSCache}
          processList={props.networkProcessList}
          isLoadingProcessList={props.isNetworkProcessListLoading}
          processListStatus={props.networkProcessListStatus}
          selectedProcess={props.networkMonitorProcess}
          isMonitoring={props.isNetworkMonitoring}
          processEvents={props.networkProcessEvents}
          processEventsStatus={props.networkProcessEventsStatus}
          onRefreshProcessList={props.onRefreshNetworkProcessList}
          onSelectProcess={props.onSelectNetworkMonitorProcess}
          onStartMonitor={props.onStartNetworkMonitor}
          onStopMonitor={props.onStopNetworkMonitor}
          onClearEvents={props.onClearNetworkMonitorEvents}
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
          minLevel={props.logMinLevel}
          onMinLevelChange={props.onLogMinLevelChange}
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
          mergedUpgradeStatus={props.mergedUpgradeStatus}
          controllerUpgradeProgress={props.controllerUpgradeProgress}
          mergedUpgradeMessages={props.mergedUpgradeMessages}
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
          managerUpgradeProgress={props.managerUpgradeProgress}
          controllerBaseUrl={props.controllerBaseUrl}
          controllerBaseUrlStatus={props.controllerBaseUrlStatus}
          isLoadingControllerBaseUrl={props.isLoadingControllerBaseUrl}
          isSavingControllerBaseUrl={props.isSavingControllerBaseUrl}
          onRefreshControllerBaseUrl={props.onRefreshControllerBaseUrl}
          onSaveControllerBaseUrl={props.onSaveControllerBaseUrl}
          controllerPreferredIP={props.controllerPreferredIP}
          controllerPreferredIPStatus={props.controllerPreferredIPStatus}
          isLoadingControllerPreferredIP={props.isLoadingControllerPreferredIP}
          isSavingControllerPreferredIP={props.isSavingControllerPreferredIP}
          onRefreshControllerPreferredIP={props.onRefreshControllerPreferredIP}
          onSaveControllerPreferredIP={props.onSaveControllerPreferredIP}
          backupEnabled={props.backupEnabled}
          backupRcloneRemote={props.backupRcloneRemote}
          backupSettingsStatus={props.backupSettingsStatus}
          isLoadingBackupSettings={props.isLoadingBackupSettings}
          isSavingBackupSettings={props.isSavingBackupSettings}
          isTestingBackupSettings={props.isTestingBackupSettings}
          onRefreshBackupSettings={props.onRefreshBackupSettings}
          onSaveBackupSettings={props.onSaveBackupSettings}
          onTestBackupSettings={props.onTestBackupSettings}
          aiDebugListenEnabled={props.aiDebugListenEnabled}
          aiDebugListenStatus={props.aiDebugListenStatus}
          isLoadingAIDebugListenEnabled={props.isLoadingAIDebugListenEnabled}
          isSavingAIDebugListenEnabled={props.isSavingAIDebugListenEnabled}
          onRefreshAIDebugListenEnabled={props.onRefreshAIDebugListenEnabled}
          onSetAIDebugListenEnabled={props.onSetAIDebugListenEnabled}
        />
      );
    default:
      return null;
  }
}
