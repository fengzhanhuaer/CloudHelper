import type {
  NetworkProcessEvent,
  NetworkProcessInfo,
} from "../types";

type NetworkAssistantMonitorPanelProps = {
  processList: NetworkProcessInfo[];
  isLoadingProcessList: boolean;
  processListStatus: string;
  selectedProcess: string;
  isMonitoring: boolean;
  processEvents: NetworkProcessEvent[];
  onRefreshProcessList: () => void;
  onSelectProcess: (name: string) => void;
  onStartMonitor: () => void;
  onStopMonitor: () => void;
  onClearEvents: () => void;
};

export function NetworkAssistantMonitorPanel(props: NetworkAssistantMonitorPanelProps) {
  return (
    <>
      <div className="content-actions" style={{ gap: 8, alignItems: "center", flexWrap: "wrap" }}>
        <select
          className="input"
          style={{ flex: 1, minWidth: 120 }}
          value={props.selectedProcess}
          onChange={(e) => props.onSelectProcess(e.target.value)}
          disabled={props.isMonitoring}
        >
          <option value="">-- 选择进程 --</option>
          {props.processList.map((p) => (
            <option key={p.pid} value={p.name}>{p.name}</option>
          ))}
        </select>
        <button
          className="btn"
          onClick={props.onRefreshProcessList}
          disabled={props.isLoadingProcessList || props.isMonitoring}
        >
          {props.isLoadingProcessList ? "加载中..." : "刷新进程"}
        </button>
        {!props.isMonitoring ? (
          <button
            className="btn"
            onClick={props.onStartMonitor}
            disabled={!props.selectedProcess || props.isMonitoring}
          >
            开始监视
          </button>
        ) : (
          <button className="btn" onClick={props.onStopMonitor}>
            停止监视
          </button>
        )}
        <button
          className="btn"
          onClick={props.onClearEvents}
          disabled={props.isMonitoring}
        >
          清空记录
        </button>
      </div>
      {props.processListStatus && <div className="status">{props.processListStatus}</div>}
      {props.isMonitoring && (
        <div className="status" style={{ color: "#4ade80" }}>监视中：{props.selectedProcess}</div>
      )}
      {props.processEvents.length === 0 ? (
        <div className="status">暂无网络事件</div>
      ) : (
        <table style={{ width: "100%", borderCollapse: "collapse", fontSize: 12, marginTop: 8 }}>
          <thead>
            <tr style={{ background: "#f0f0f0" }}>
              <th style={{ padding: "4px 8px", textAlign: "left", borderBottom: "1px solid #ddd" }}>时间</th>
              <th style={{ padding: "4px 8px", textAlign: "left", borderBottom: "1px solid #ddd" }}>类型</th>
              <th style={{ padding: "4px 8px", textAlign: "left", borderBottom: "1px solid #ddd" }}>域名/IP</th>
              <th style={{ padding: "4px 8px", textAlign: "left", borderBottom: "1px solid #ddd" }}>端口</th>
              <th style={{ padding: "4px 8px", textAlign: "left", borderBottom: "1px solid #ddd" }}>路由</th>
              <th style={{ padding: "4px 8px", textAlign: "left", borderBottom: "1px solid #ddd" }}>解析 IP</th>
            </tr>
          </thead>
          <tbody>
            {[...props.processEvents].reverse().map((ev, i) => {
              const t = new Date(ev.timestamp);
              const timeStr = `${t.getHours().toString().padStart(2, "0")}:${t.getMinutes().toString().padStart(2, "0")}:${t.getSeconds().toString().padStart(2, "0")}.${t.getMilliseconds().toString().padStart(3, "0")}`;
              const target = ev.kind === "dns" ? (ev.domain || "-") : (ev.target_ip || "-");
              const route = ev.direct ? "直连" : (ev.node_id ? `代理(${ev.node_id})` : "-");
              const resolvedIPs = ev.resolved_ips ? ev.resolved_ips.join(", ") : "-";
              return (
                <tr key={i} style={{ borderBottom: "1px solid #eee" }}>
                  <td style={{ padding: "4px 8px", fontFamily: "monospace", whiteSpace: "nowrap" }}>{timeStr}</td>
                  <td style={{ padding: "4px 8px", fontWeight: "bold", color: ev.kind === "dns" ? "#60a5fa" : ev.kind === "tcp" ? "#4ade80" : "#facc15" }}>{ev.kind.toUpperCase()}</td>
                  <td style={{ padding: "4px 8px", fontFamily: "monospace" }}>{target}</td>
                  <td style={{ padding: "4px 8px", fontFamily: "monospace" }}>{ev.target_port ? ev.target_port : "-"}</td>
                  <td style={{ padding: "4px 8px" }}>{route}</td>
                  <td style={{ padding: "4px 8px", fontFamily: "monospace", fontSize: 11 }}>{ev.kind === "dns" ? resolvedIPs : "-"}</td>
                </tr>
              );
            })}
          </tbody>
        </table>
      )}
    </>
  );
}
