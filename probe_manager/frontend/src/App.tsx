import { useEffect, useMemo, useState } from "react";
import logo from "./assets/images/logo-universal.png";
import "./App.css";
import { resolveTabs } from "./modules/app/authz";
import { LoginPanel } from "./modules/app/components/LoginPanel";
import { Sidebar } from "./modules/app/components/Sidebar";
import { TabContent } from "./modules/app/components/TabContent";
import { useAuthFlow } from "./modules/app/hooks/useAuthFlow";
import { useConnectionFlow } from "./modules/app/hooks/useConnectionFlow";
import { useLocalSettings } from "./modules/app/hooks/useLocalSettings";
import { useUpgradeFlow } from "./modules/app/hooks/useUpgradeFlow";
import type { TabKey } from "./modules/app/types";

function App() {
  const [activeTab, setActiveTab] = useState<TabKey>("overview");

  const settings = useLocalSettings();
  const auth = useAuthFlow();
  const connection = useConnectionFlow(settings.baseUrl, auth.sessionToken);
  const upgrade = useUpgradeFlow();

  const tabs = useMemo(() => resolveTabs(auth.userRole, auth.certType), [auth.userRole, auth.certType]);

  useEffect(() => {
    if (!tabs.some((item) => item.key === activeTab)) {
      setActiveTab(tabs[0].key);
    }
  }, [activeTab, tabs]);

  useEffect(() => {
    if (auth.sessionToken && activeTab === "system-settings") {
      void upgrade.refreshSystemVersions(settings.baseUrl, auth.sessionToken);
    }
  }, [activeTab, auth.sessionToken, settings.baseUrl]);

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
          username={auth.username}
          userRole={auth.userRole}
          certType={auth.certType}
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
            onPingServer={() => connection.pingServer(settings.baseUrl)}
            onCheckAdminStatus={() => connection.checkAdminStatus(settings.baseUrl, auth.sessionToken)}
            onRefreshPrivateKeyStatus={auth.refreshPrivateKeyStatus}
            managerVersion={upgrade.managerVersion}
            controllerVersion={upgrade.controllerVersion}
            controllerLatestVersion={upgrade.controllerLatestVersion}
            versionStatus={upgrade.versionStatus}
            upgradeStatus={upgrade.upgradeStatus}
            isUpgradingController={upgrade.isUpgradingController}
            isUpgradingManager={upgrade.isUpgradingManager}
            onRefreshSystemVersions={() => upgrade.refreshSystemVersions(settings.baseUrl, auth.sessionToken)}
            onUpgradeController={() => upgrade.upgradeController(settings.baseUrl, auth.sessionToken)}
            upgradeProject={settings.upgradeProject}
            onUpgradeProjectChange={settings.setUpgradeProject}
            isCheckingDirect={upgrade.isCheckingDirect}
            isCheckingProxy={upgrade.isCheckingProxy}
            sessionToken={auth.sessionToken}
            onCheckManagerReleaseDirect={() => upgrade.checkManagerReleaseDirect(settings.upgradeProject)}
            onUpgradeManagerDirect={() => upgrade.upgradeManagerDirect(settings.upgradeProject)}
            onCheckManagerReleaseProxy={() => upgrade.checkManagerReleaseProxy(settings.baseUrl, auth.sessionToken, settings.upgradeProject)}
            onUpgradeManagerProxy={() => upgrade.upgradeManagerProxy(settings.baseUrl, auth.sessionToken, settings.upgradeProject)}
            directRelease={upgrade.directRelease}
            proxyRelease={upgrade.proxyRelease}
            managerUpgradeStatus={upgrade.managerUpgradeStatus}
          />
        </main>
      </div>
    </div>
  );
}

export default App;
