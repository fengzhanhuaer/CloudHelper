import { useEffect, useMemo, useRef, useState } from "react";
import logo from "./assets/images/site-icon.png";
import "./App.css";
import { resolveTabs } from "./modules/app/authz";
import { LoginPanel } from "./modules/app/components/LoginPanel";
import { Sidebar } from "./modules/app/components/Sidebar";
import { TabContent } from "./modules/app/components/TabContent";
import { useAuthFlow } from "./modules/app/hooks/useAuthFlow";
import { useConnectionFlow } from "./modules/app/hooks/useConnectionFlow";
import { useLogViewer } from "./modules/app/hooks/useLogViewer";
import { useLocalSettings } from "./modules/app/hooks/useLocalSettings";
import { useNetworkAssistant } from "./modules/app/hooks/useNetworkAssistant";
import { useUpgradeFlow } from "./modules/app/hooks/useUpgradeFlow";
import type { TabKey } from "./modules/app/types";

type WailsHeartbeatWindow = Window & {
  go?: {
    main?: {
      App?: {
        NotifyFrontendHeartbeat?: () => Promise<void>;
      };
    };
  };
};

function App() {
  const [activeTab, setActiveTab] = useState<TabKey>("overview");
  const [networkLogAutoScroll, setNetworkLogAutoScroll] = useState(true);
  const networkAssistantInitKeyRef = useRef("");

  const settings = useLocalSettings();
  const auth = useAuthFlow();
  const connection = useConnectionFlow(settings.baseUrl, auth.sessionToken);
  const upgrade = useUpgradeFlow();
  const networkAssistant = useNetworkAssistant();
  const logViewer = useLogViewer();

  const tabs = useMemo(() => resolveTabs(auth.userRole, auth.certType), [auth.userRole, auth.certType]);

  useEffect(() => {
    if (!tabs.some((item) => item.key === activeTab)) {
      setActiveTab(tabs[0].key);
    }
  }, [activeTab, tabs]);

  useEffect(() => {
    const heartbeatWindow = window as WailsHeartbeatWindow;
    const sendHeartbeat = () => {
      void heartbeatWindow.go?.main?.App?.NotifyFrontendHeartbeat?.();
    };

    sendHeartbeat();
    const timer = window.setInterval(sendHeartbeat, 5000);
    return () => window.clearInterval(timer);
  }, []);

  useEffect(() => {
    if (auth.sessionToken && activeTab === "system-settings") {
      void upgrade.refreshSystemVersions(settings.baseUrl, auth.sessionToken, reauthenticateSession);
      void upgrade.refreshBackupSettings(settings.baseUrl, auth.sessionToken, reauthenticateSession);
    }
  }, [activeTab, auth.sessionToken, settings.baseUrl]);

  useEffect(() => {
    if (!auth.sessionToken) {
      return;
    }
    if (activeTab !== "network-assistant") {
      networkAssistantInitKeyRef.current = "";
      return;
    }
    if (networkAssistantInitKeyRef.current) {
      return;
    }
    const initKey = `${activeTab}|${auth.sessionToken}|${settings.baseUrl}`;
    networkAssistantInitKeyRef.current = initKey;
    void networkAssistant.refreshStatus(settings.baseUrl, auth.sessionToken);
    void networkAssistant.refreshLogs();
    void networkAssistant.refreshRuleConfig();
  }, [
    activeTab,
    auth.sessionToken,
    networkAssistant.refreshLogs,
    networkAssistant.refreshRuleConfig,
    networkAssistant.refreshStatus,
    settings.baseUrl,
  ]);

  useEffect(() => {
    if (!auth.sessionToken) {
      return;
    }
    if (activeTab === "log-viewer") {
      void logViewer.refreshLogs(settings.baseUrl, auth.sessionToken, reauthenticateSession);
    }
  }, [activeTab, auth.sessionToken, logViewer.refreshLogs, settings.baseUrl]);

  async function reauthenticateSession(): Promise<string> {
    const result = await auth.login(settings.baseUrl);
    if (!result.ok || !result.sessionToken) {
      throw new Error("自动重新登录失败，请手动点击 Logout 后重新登录");
    }
    const allowedTabs = resolveTabs(result.userRole ?? auth.userRole, result.certType ?? auth.certType);
    if (!allowedTabs.some((item) => item.key === activeTab)) {
      setActiveTab(allowedTabs[0].key);
    }
    return result.sessionToken;
  }

  async function handleLogin() {
    const result = await auth.login(settings.baseUrl);
    if (result.ok) {
      const allowedTabs = resolveTabs(result.userRole ?? auth.userRole, result.certType ?? auth.certType);
      setActiveTab(allowedTabs[0].key);
      connection.clearStatusMessages();
    }
  }

  function handleLogout() {
    auth.logout();
    setActiveTab("overview");
    connection.clearStatusMessages();
    upgrade.clearUpgradeMessages();
    logViewer.clearLogs();
    networkAssistant.clearLogs();
  }

  if (!auth.sessionToken) {
    return (
      <div id="App">
        <img src={logo} id="logo" alt="logo" />
        <LoginPanel
          baseUrl={settings.baseUrl}
          onBaseUrlChange={settings.setBaseUrl}
          privateKeyStatus={auth.privateKeyStatus}
          privateKeyPath={auth.privateKeyPath}
          isAuthenticating={auth.isAuthenticating}
          onRefreshPrivateKey={auth.refreshPrivateKeyStatus}
          onLogin={handleLogin}
          loginTone={auth.loginTone}
          loginStatus={auth.loginStatus}
        />
      </div>
    );
  }

  return (
    <div id="App">
      <div className="app-shell">
        <Sidebar
          tabs={tabs}
          activeTab={activeTab}
          onTabChange={setActiveTab}
          onLogout={handleLogout}
        />

        <main className="content">
          <TabContent
            activeTab={activeTab}
            username={auth.username}
            userRole={auth.userRole}
            certType={auth.certType}
            privateKeyStatus={auth.privateKeyStatus}
            privateKeyPath={auth.privateKeyPath}
            wsStatus={connection.wsStatus}
            serverStatus={connection.serverStatus}
            adminStatus={connection.adminStatus}
            onPingServer={() => connection.pingServer(settings.baseUrl, auth.sessionToken)}
            onCheckAdminStatus={() => connection.checkAdminStatus(settings.baseUrl, auth.sessionToken, reauthenticateSession)}
            controllerBaseUrl={settings.baseUrl}
            controllerPreferredIP={settings.controllerIP}
            controllerPreferredIPStatus={settings.controllerIPStatus}
            isLoadingControllerPreferredIP={settings.isLoadingControllerIP}
            isSavingControllerPreferredIP={settings.isSavingControllerIP}
            onRefreshControllerPreferredIP={() => void settings.refreshControllerIP()}
            onSaveControllerPreferredIP={(value) => void settings.saveControllerIP(value)}
            onRefreshPrivateKeyStatus={auth.refreshPrivateKeyStatus}
            managerVersion={upgrade.managerVersion}
            controllerVersion={upgrade.controllerVersion}
            controllerLatestVersion={upgrade.controllerLatestVersion}
            versionStatus={upgrade.versionStatus}
            mergedUpgradeStatus={upgrade.mergedUpgradeStatus}
            controllerUpgradeProgress={upgrade.controllerUpgradeProgress}
            mergedUpgradeMessages={upgrade.mergedUpgradeMessages}
            isUpgradingController={upgrade.isUpgradingController}
            isUpgradingManager={upgrade.isUpgradingManager}
            onRefreshSystemVersions={() => upgrade.refreshSystemVersions(settings.baseUrl, auth.sessionToken, reauthenticateSession)}
            onUpgradeController={() => upgrade.upgradeController(settings.baseUrl, auth.sessionToken, reauthenticateSession)}
            upgradeProject={settings.upgradeProject}
            onUpgradeProjectChange={settings.setUpgradeProject}
            isCheckingDirect={upgrade.isCheckingDirect}
            isCheckingProxy={upgrade.isCheckingProxy}
            sessionToken={auth.sessionToken}
            onCheckManagerReleaseDirect={() => upgrade.checkManagerReleaseDirect(settings.upgradeProject)}
            onUpgradeManagerDirect={() => upgrade.upgradeManagerDirect(settings.upgradeProject)}
            onCheckManagerReleaseProxy={() => upgrade.checkManagerReleaseProxy(settings.baseUrl, auth.sessionToken, settings.upgradeProject, reauthenticateSession)}
            onUpgradeManagerProxy={() => upgrade.upgradeManagerProxy(settings.baseUrl, auth.sessionToken, settings.upgradeProject, reauthenticateSession)}
            directRelease={upgrade.directRelease}
            proxyRelease={upgrade.proxyRelease}
            managerUpgradeProgress={upgrade.managerUpgradeProgress}
            backupEnabled={upgrade.backupEnabled}
            backupRcloneRemote={upgrade.backupRcloneRemote}
            backupSettingsStatus={upgrade.backupSettingsStatus}
            isLoadingBackupSettings={upgrade.isLoadingBackupSettings}
            isSavingBackupSettings={upgrade.isSavingBackupSettings}
            isTestingBackupSettings={upgrade.isTestingBackupSettings}
            onRefreshBackupSettings={() => upgrade.refreshBackupSettings(settings.baseUrl, auth.sessionToken, reauthenticateSession)}
            onSaveBackupSettings={(enabled, value) => void upgrade.saveBackupSettings(settings.baseUrl, auth.sessionToken, enabled, value, reauthenticateSession)}
            onTestBackupSettings={(value) => void upgrade.testBackupSettings(settings.baseUrl, auth.sessionToken, value, reauthenticateSession)}
            aiDebugListenEnabled={settings.aiDebugListenEnabled}
            aiDebugListenStatus={settings.aiDebugListenStatus}
            isLoadingAIDebugListenEnabled={settings.isLoadingAIDebugListenEnabled}
            isSavingAIDebugListenEnabled={settings.isSavingAIDebugListenEnabled}
            onRefreshAIDebugListenEnabled={() => void settings.refreshAIDebugListenEnabled()}
            onSetAIDebugListenEnabled={(enabled) => void settings.setAIDebugListenEnabled(enabled)}
            networkAssistantStatus={networkAssistant.status}
            isOperatingNetworkAssistant={networkAssistant.isOperating}
            networkOperateStatus={networkAssistant.operateStatus}
            onRefreshNetworkAssistantStatus={() => networkAssistant.refreshStatus(settings.baseUrl, auth.sessionToken)}
            onSwitchNetworkDirect={() => networkAssistant.switchMode(settings.baseUrl, auth.sessionToken, "direct")}
            onSwitchNetworkTUN={() => networkAssistant.switchMode(settings.baseUrl, auth.sessionToken, "tun")}
            networkRuleConfig={networkAssistant.ruleConfig}
            isLoadingNetworkRuleConfig={networkAssistant.isLoadingRuleConfig}
            networkRuleConfigStatus={networkAssistant.ruleConfigStatus}
            isSyncingNetworkRuleRoutes={networkAssistant.isSyncingRuleRoutes}
            networkRuleRoutesSyncStatus={networkAssistant.ruleRoutesSyncStatus}
            onRefreshNetworkRuleConfig={networkAssistant.refreshRuleConfig}
            onUploadNetworkRuleRoutes={() => void networkAssistant.uploadRuleRoutes(settings.baseUrl, auth.sessionToken)}
            onDownloadNetworkRuleRoutes={() => void networkAssistant.downloadRuleRoutes(settings.baseUrl, auth.sessionToken)}
            onSetNetworkRulePolicy={(group, action, tunnelNodeID) => void networkAssistant.setRulePolicy(group, action, tunnelNodeID)}
            onInstallNetworkTUN={() => networkAssistant.installTUN()}
            onEnableNetworkTUN={() => networkAssistant.enableTUN()}
            onCloseNetworkTUN={() => networkAssistant.closeTUN()}
            networkDNSCacheEntries={networkAssistant.dnsCacheEntries}
            networkDNSCacheQuery={networkAssistant.dnsCacheQuery}
            isNetworkDNSCacheLoading={networkAssistant.isDNSCacheLoading}
            networkDNSCacheStatus={networkAssistant.dnsCacheStatus}
            onNetworkDNSCacheQueryChange={networkAssistant.setDnsCacheQuery}
            onQueryNetworkDNSCache={networkAssistant.queryDNSCache}
            networkProcessList={networkAssistant.processList}
            isNetworkProcessListLoading={networkAssistant.isLoadingProcesses}
            networkProcessListStatus={networkAssistant.processListStatus}
            networkMonitorProcess={networkAssistant.monitorProcessName}
            isNetworkMonitoring={networkAssistant.isMonitoring}
            networkProcessEvents={networkAssistant.processEvents}
            networkProcessEventsStatus={networkAssistant.processEventsStatus}
            onRefreshNetworkProcessList={networkAssistant.refreshProcessList}
            onSelectNetworkMonitorProcess={networkAssistant.setMonitorProcessName}
            onStartNetworkMonitor={networkAssistant.startProcessMonitor}
            onStopNetworkMonitor={networkAssistant.stopProcessMonitor}
            onClearNetworkMonitorEvents={networkAssistant.clearProcessEvents}
            networkLogLines={networkAssistant.logLines}
            onNetworkLogLinesChange={networkAssistant.setLogLines}
            isLoadingNetworkLogs={networkAssistant.isLoadingLogs}
            networkLogStatus={networkAssistant.logStatus}
            networkLogCopyStatus={networkAssistant.logCopyStatus}
            networkLogContent={networkAssistant.logContent}
            networkLogSourceFilter={networkAssistant.logSourceFilter}
            onNetworkLogSourceFilterChange={networkAssistant.setLogSourceFilter}
            networkLogCategoryFilter={networkAssistant.logCategoryFilter}
            onNetworkLogCategoryFilterChange={networkAssistant.setLogCategoryFilter}
            networkLogCategories={networkAssistant.logCategories}
            networkLogVisibleCount={networkAssistant.logVisibleCount}
            networkLogTotalCount={networkAssistant.logTotalCount}
            networkLogAutoScroll={networkLogAutoScroll}
            onNetworkLogAutoScrollChange={setNetworkLogAutoScroll}
            onRefreshNetworkLogs={networkAssistant.refreshLogs}
            onCopyNetworkLogs={networkAssistant.copyLogs}
            logSource={logViewer.source}
            onLogSourceChange={logViewer.setSource}
            logLines={logViewer.lines}
            onLogLinesChange={logViewer.setLines}
            logSinceMinutes={logViewer.sinceMinutes}
            onLogSinceMinutesChange={logViewer.setSinceMinutes}
            logMinLevel={logViewer.minLevel}
            onLogMinLevelChange={logViewer.setMinLevel}
            logAutoScroll={logViewer.autoScroll}
            onLogAutoScrollChange={logViewer.setAutoScroll}
            isLoadingLogs={logViewer.isLoading}
            logStatus={logViewer.status}
            logCopyStatus={logViewer.copyStatus}
            logFilePath={logViewer.logFilePath}
            logContent={logViewer.content}
            onRefreshLogs={() => logViewer.refreshLogs(settings.baseUrl, auth.sessionToken, reauthenticateSession)}
            onCopyLogs={() => logViewer.copyLogs()}
          />
        </main>
      </div>
    </div>
  );
}

export default App;
