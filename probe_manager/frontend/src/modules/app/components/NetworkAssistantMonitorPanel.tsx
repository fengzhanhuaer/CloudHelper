import { useMemo, useState } from "react";
import type { NetworkProcessEvent } from "../types";

type NetworkAssistantMonitorPanelProps = {
  isMonitoring: boolean;
  processEvents: NetworkProcessEvent[];
  processEventsStatus: string;
  onStartMonitor: () => void;
  onStopMonitor: () => void;
  onClearEvents: () => void;
};

type ProcessGroup = {
  processName: string;
  totalCount: number;
  latestTimestamp: number;
  records: NetworkProcessEvent[];
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

function protocolLabel(event: NetworkProcessEvent): string {
  if (event.kind === "dns") return "DNS";
  return event.kind.toUpperCase();
}

function domainLabel(event: NetworkProcessEvent): string {
  if (event.domain && event.domain.trim()) return event.domain.trim();
  return "-";
}

function ipLabel(event: NetworkProcessEvent): string {
  if (event.kind === "dns") {
    const ips = Array.isArray(event.resolved_ips) ? event.resolved_ips.filter(Boolean) : [];
    return ips.length > 0 ? ips.join(", ") : "-";
  }
  if (!event.target_ip) return "-";
  return event.target_port ? `${event.target_ip}:${event.target_port}` : event.target_ip;
}

function eventCount(event: NetworkProcessEvent): number {
  const c = event.count ?? 1;
  return c > 0 ? c : 1;
}

export function NetworkAssistantMonitorPanel(props: NetworkAssistantMonitorPanelProps) {
  const [pinnedProcessName, setPinnedProcessName] = useState("");

  const groups = useMemo<ProcessGroup[]>(() => {
    const map = new Map<string, ProcessGroup>();
    for (const event of props.processEvents) {
      const processName = (event.process_name || "unknown").trim() || "unknown";
      const key = processName.toLowerCase();
      const existing = map.get(key);
      if (!existing) {
        map.set(key, {
          processName,
          totalCount: eventCount(event),
          latestTimestamp: event.timestamp || 0,
          records: [event],
        });
        continue;
      }
      existing.totalCount += eventCount(event);
      existing.records.push(event);
      if ((event.timestamp || 0) > existing.latestTimestamp) {
        existing.latestTimestamp = event.timestamp || 0;
      }
    }

    const list = Array.from(map.values());
    list.forEach((group) => {
      group.records.sort((a, b) => (b.timestamp || 0) - (a.timestamp || 0));
    });
    list.sort((a, b) => {
      const aPinned = pinnedProcessName && a.processName.toLowerCase() === pinnedProcessName.toLowerCase();
      const bPinned = pinnedProcessName && b.processName.toLowerCase() === pinnedProcessName.toLowerCase();
      if (aPinned && !bPinned) return -1;
      if (!aPinned && bPinned) return 1;
      return b.latestTimestamp - a.latestTimestamp;
    });
    return list;
  }, [pinnedProcessName, props.processEvents]);

  return (
    <>
      <div className="content-actions" style={{ gap: 8, alignItems: "center", flexWrap: "wrap" }}>
        {!props.isMonitoring ? (
          <button className="btn" onClick={props.onStartMonitor}>开始监视</button>
        ) : (
          <button className="btn" onClick={props.onStopMonitor}>停止监视</button>
        )}
        <button className="btn" onClick={props.onClearEvents}>清空</button>
        {props.isMonitoring && <span style={{ fontSize: 12, color: "#888" }}>监视中，每 2 秒刷新…</span>}
      </div>
      {props.processEventsStatus && <div className="status">{props.processEventsStatus}</div>}
      {groups.length === 0 ? (
        <div className="status">暂无事件</div>
      ) : (
        <div style={{ display: "grid", gap: 10, marginTop: 8 }}>
          {groups.map((group) => {
            const pinned = pinnedProcessName && group.processName.toLowerCase() === pinnedProcessName.toLowerCase();
            return (
              <div key={group.processName.toLowerCase()} style={{ border: "1px solid #2a2a2a", borderRadius: 8, overflow: "hidden" }}>
                <div style={{ display: "flex", justifyContent: "space-between", alignItems: "center", gap: 8, padding: "8px 10px", background: pinned ? "#2b3650" : "#1f1f1f" }}>
                  <div style={{ display: "flex", alignItems: "center", gap: 10, flexWrap: "wrap" }}>
                    <strong style={{ fontFamily: "monospace" }}>{group.processName}</strong>
                    <span style={{ fontSize: 12, color: "#aaa" }}>记录数：{group.records.length}</span>
                    <span style={{ fontSize: 12, color: "#aaa" }}>连接次数：{group.totalCount}</span>
                    <span style={{ fontSize: 12, color: "#aaa" }}>最近：{formatTimestamp(group.latestTimestamp)}</span>
                  </div>
                  <button
                    className="btn"
                    style={{ padding: "2px 10px", minWidth: 70, fontSize: 12 }}
                    onClick={() => setPinnedProcessName(pinned ? "" : group.processName)}
                  >
                    {pinned ? "取消置顶" : "铆定置顶"}
                  </button>
                </div>
                <table style={{ width: "100%", borderCollapse: "collapse", fontSize: 13 }}>
                  <thead>
                    <tr style={{ background: "#f0f0f0" }}>
                      <th style={{ padding: "4px 8px", textAlign: "left", borderBottom: "1px solid #ddd" }}>时间</th>
                      <th style={{ padding: "4px 8px", textAlign: "left", borderBottom: "1px solid #ddd" }}>协议</th>
                      <th style={{ padding: "4px 8px", textAlign: "left", borderBottom: "1px solid #ddd" }}>域名</th>
                      <th style={{ padding: "4px 8px", textAlign: "left", borderBottom: "1px solid #ddd" }}>IP</th>
                      <th style={{ padding: "4px 8px", textAlign: "left", borderBottom: "1px solid #ddd" }}>路由</th>
                      <th style={{ padding: "4px 8px", textAlign: "right", borderBottom: "1px solid #ddd" }}>次数</th>
                    </tr>
                  </thead>
                  <tbody>
                    {group.records.map((ev, i) => {
                      const routeGroup = [routeLabel(ev), ev.group].filter(Boolean).join(" / ");
                      return (
                        <tr key={`${group.processName}-${ev.kind}-${ev.timestamp}-${i}`} style={{ borderBottom: "1px solid #eee" }}>
                          <td style={{ padding: "4px 8px", fontFamily: "monospace", fontSize: 12, whiteSpace: "nowrap" }}>{formatTimestamp(ev.timestamp)}</td>
                          <td style={{ padding: "4px 8px" }}>{protocolLabel(ev)}</td>
                          <td style={{ padding: "4px 8px", fontFamily: "monospace" }}>{domainLabel(ev)}</td>
                          <td style={{ padding: "4px 8px", fontFamily: "monospace" }}>{ipLabel(ev)}</td>
                          <td style={{ padding: "4px 8px" }}>{routeGroup || "-"}</td>
                          <td style={{ padding: "4px 8px", textAlign: "right", fontFamily: "monospace" }}>{eventCount(ev)}</td>
                        </tr>
                      );
                    })}
                  </tbody>
                </table>
              </div>
            );
          })}
        </div>
      )}
    </>
  );
}
