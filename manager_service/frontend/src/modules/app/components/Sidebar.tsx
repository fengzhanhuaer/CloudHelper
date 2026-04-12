import type { TabItem, TabKey } from "../types";

type SidebarProps = {
  tabs: TabItem[];
  activeTab: TabKey;
  onTabChange: (tab: TabKey) => void;
  onLogout: () => void;
};

export function Sidebar(props: SidebarProps) {
  return (
    <aside className="sidebar">
      <div className="sidebar-title">CloudHelper Manager</div>

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
