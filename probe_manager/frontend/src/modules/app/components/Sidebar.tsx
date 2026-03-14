import type { TabItem, TabKey } from "../types";

type SidebarProps = {
  username: string;
  userRole: string;
  certType: string;
  tabs: TabItem[];
  activeTab: TabKey;
  onTabChange: (tab: TabKey) => void;
  onLogout: () => void;
};

export function Sidebar(props: SidebarProps) {
  return (
    <aside className="sidebar">
      <div className="sidebar-title">CloudHelper Manager</div>
      <div className="sidebar-identity">user={props.username}</div>
      <div className="sidebar-identity">role={props.userRole}</div>
      <div className="sidebar-identity">cert={props.certType}</div>

      <div className="tab-list">
        {props.tabs.map((tab) => (
          <button
            key={tab.key}
            className={`tab-btn ${props.activeTab === tab.key ? "active" : ""}`}
            onClick={() => props.onTabChange(tab.key)}
          >
            {tab.label}
          </button>
        ))}
      </div>

      <div className="sidebar-actions">
        <button className="btn" onClick={props.onLogout}>退出登录</button>
      </div>
    </aside>
  );
}
