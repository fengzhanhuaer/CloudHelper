import type {
  NetworkProcessEvent,
} from "../types";

type NetworkAssistantMonitorPanelProps = {
  isMonitoring: boolean;
  processEvents: NetworkProcessEvent[];
  processEventsStatus: string;
  onStartMonitor: () => void;
  onStopMonitor: () => void;
  onClearEvents: () => void;
};

function formatTimestamp(ms: number): string {
  const d = new Date(ms);
  return d.toLocaleTimeString("zh-CN", { hour12: false, hour: "2-digit", minute: "2-digit", second: "2-digit" });
}

function routeLabel(event: NetworkProcessEvent): string {
  if (event.direct) return "直连";
  if (event.node_id) return `代理(${event.node_id})`;
  return "-";
}

export function NetworkAssistantMonitorPanel(props: NetworkAssistantMonitorPanelProps) {
  return (
    <>
      <div className="content-actions" style={{ gap: 8, alignItems: "center", flexWrap: "wrap" }}>
        {!props.isMonitoring ? (
          <button
            className="btn"
            onClick={props.onStartMonitor}
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
          清空
        </button>
        {props.isMonitoring && (
          <span style={{ fontSize: 12, color: "#888" }}>监视中，每 2 秒刷新…</span>
        )}
      </div>
      {props.processEventsStatus && <div className="status">{props.processEventsStatus}</div>}
      {props.processEvents.length === 0 ? (
        <div className="status">暂无事件</div>
      ) : (
        <table style={{ width: "100%", borderCollapse: "collapse", fontSize: 13, marginTop: 8 }}>
          <thead>
            <tr style={{ background: "#f0f0f0" }}>
              <th style={{ padding: "4px 8px", textAlign: "left", borderBottom: "1px solid #ddd" }}>时间</th>
              <th style={{ padding: "4px 8px", textAlign: "left", borderBottom: "1px solid #ddd" }}>进程</th>
              <th style={{ padding: "4px 8px", textAlign: "left", borderBottom: "1px solid #ddd" }}>类型</th>
              <th style={{ padding: "4px 8px", textAlign: "left", borderBottom: "1px solid #ddd" }}>域名 / 目标</th>
              <th style={{ padding: "4px 8px", textAlign: "left", borderBottom: "1px solid #ddd" }}>路由 / 代理组</th>
            </tr>
          </thead>
          <tbody>
            {props.processEvents.slice().reverse().map((ev, i) => {
              const target = ev.kind === "dns"
                ? (ev.domain || "-")
                : `${ev.target_ip || "-"}:${ev.target_port ?? "-"}`;
              const routeGroup = [routeLabel(ev), ev.group].filter(Boolean).join(" / ");
              return (
                <tr key={i} style={{ borderBottom: "1px solid #eee" }}>
                  <td style={{ padding: "4px 8px", fontFamily: "monospace", fontSize: 12, whiteSpace: "nowrap" }}>{formatTimestamp(ev.timestamp)}</td>
                  <td style={{ padding: "4px 8px", fontFamily: "monospace" }}>{ev.process_name || "-"}</td>
                  <td style={{ padding: "4px 8px" }}>{ev.kind.toUpperCase()}</td>
                  <td style={{ padding: "4px 8px", fontFamily: "monospace" }}>{target}</td>
                  <td style={{ padding: "4px 8px" }}>{routeGroup || "-"}</td>
                </tr>
              );
            })}
          </tbody>
        </table>
      )}
    </>
  );
}
