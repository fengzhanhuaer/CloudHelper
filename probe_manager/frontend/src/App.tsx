import { useEffect, useMemo, useState } from "react";
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

function App() {
  const [activeTab, setActiveTab] = useState<TabKey>("overview");
  const [networkLogAutoScroll, setNetworkLogAutoScroll] = useState(true);

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
    if (auth.sessionToken && activeTab === "system-settings") {
      void upgrade.refreshSystemVersions(settings.baseUrl, auth.sessionToken, reauthenticateSession);
    }
  }, [activeTab, auth.sessionToken, settings.baseUrl]);

  useEffect(() => {
    if (!auth.sessionToken) {
      return;
    }
    if (activeTab === "network-assistant") {
      void networkAssistant.refreshStatus(settings.baseUrl, auth.sessionToken);
      void networkAssistant.refreshLogs();
    }
  }, [activeTab, auth.sessionToken, networkAssistant.refreshLogs, networkAssistant.refreshStatus, settings.baseUrl]);

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
            onRefreshPrivateKeyStatus={auth.refreshPrivateKeyStatus}
            managerVersion={upgrade.managerVersion}
            controllerVersion={upgrade.controllerVersion}
            controllerLatestVersion={upgrade.controllerLatestVersion}
            versionStatus={upgrade.versionStatus}
            upgradeStatus={upgrade.upgradeStatus}
            controllerUpgradeProgress={upgrade.controllerUpgradeProgress}
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
            managerUpgradeStatus={upgrade.managerUpgradeStatus}
            managerUpgradeProgress={upgrade.managerUpgradeProgress}
            networkAssistantStatus={networkAssistant.status}
            networkSelectedNode={networkAssistant.selectedNode}
            onNetworkSelectedNodeChange={networkAssistant.setSelectedNode}
            isOperatingNetworkAssistant={networkAssistant.isOperating}
            networkOperateStatus={networkAssistant.operateStatus}
            onRefreshNetworkAssistantStatus={() => networkAssistant.refreshStatus(settings.baseUrl, auth.sessionToken)}
            onSwitchNetworkDirect={() => networkAssistant.switchMode(settings.baseUrl, auth.sessionToken, "direct", networkAssistant.selectedNode)}
            onSwitchNetworkGlobal={() => networkAssistant.switchMode(settings.baseUrl, auth.sessionToken, "global", networkAssistant.selectedNode)}
            onRestoreNetworkDirect={() => networkAssistant.restoreDirect()}
            networkLogLines={networkAssistant.logLines}
            onNetworkLogLinesChange={networkAssistant.setLogLines}
            isLoadingNetworkLogs={networkAssistant.isLoadingLogs}
            networkLogStatus={networkAssistant.logStatus}
            networkLogCopyStatus={networkAssistant.logCopyStatus}
            networkLogContent={networkAssistant.logContent}
            networkLogAutoScroll={networkLogAutoScroll}
            onNetworkLogAutoScrollChange={setNetworkLogAutoScroll}
            onRefreshNetworkLogs={() => networkAssistant.refreshLogs()}
            onCopyNetworkLogs={() => networkAssistant.copyLogs()}
            logSource={logViewer.source}
            onLogSourceChange={logViewer.setSource}
            logLines={logViewer.lines}
            onLogLinesChange={logViewer.setLines}
            logSinceMinutes={logViewer.sinceMinutes}
            onLogSinceMinutesChange={logViewer.setSinceMinutes}
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
