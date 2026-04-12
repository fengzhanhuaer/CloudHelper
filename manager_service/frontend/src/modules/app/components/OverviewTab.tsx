type OverviewTabProps = {
  username: string;
  userRole: string;
  certType: string;
  wsStatus: string;
  serverStatus: string;
  adminStatus: string;
  onPingServer: () => void;
  onCheckAdminStatus: () => void;
};

export function OverviewTab(props: OverviewTabProps) {
  return (
    <div className="content-block">
      <h2>概要状态</h2>
      <div className="identity-card">
        <div>用户名：{props.username}</div>
        <div>当前角色：{props.userRole}</div>
        <div>证书类型：{props.certType}</div>
      </div>

      <div className="content-actions">
        <button className="btn" onClick={props.onPingServer}>公开状态检测</button>
        <button className="btn" onClick={props.onCheckAdminStatus}>管理接口检测</button>
      </div>

      <div className="status">{props.wsStatus}</div>
      <div className="status">{props.serverStatus}</div>
      <div className="status">{props.adminStatus}</div>
    </div>
  );
}
