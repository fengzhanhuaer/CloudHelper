import { useMemo, useState } from "react";
import type { NetworkProcessEvent, NetworkProcessInfo } from "../types";

type NetworkAssistantMonitorPanelProps = {
  isMonitoring: boolean;
  processList: NetworkProcessInfo[];
  isLoadingProcessList: boolean;
  processListStatus: string;
  selectedProcess: string;
  processEvents: NetworkProcessEvent[];
  processEventsStatus: string;
  onRefreshProcessList: () => void;
  onSelectProcess: (name: string) => void;
  onStartMonitor: () => void;
  onStopMonitor: () => void;
  onClearEvents: () => void;
};

type ProcessRow = {
  key: string;
  processName: string;
  processInfo?: NetworkProcessInfo;
  totalCount: number;
  latestTimestamp: number;
  records: NetworkProcessEvent[];
};

function formatTimestamp(ms: number): string {
  if (!ms) return "-";
  const d = new Date(ms);
  return d.toLocaleTimeString("zh-CN", {
    hour12: false,
    hour: "2-digit",
    minute: "2-digit",
    second: "2-digit",
  });
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

function normalizeProcessName(name?: string): string {
  const trimmed = (name || "unknown").trim();
  return trimmed || "unknown";
}

export function NetworkAssistantMonitorPanel(props: NetworkAssistantMonitorPanelProps) {
  const [pinnedProcessName, setPinnedProcessName] = useState("");
  const [expandedMap, setExpandedMap] = useState<Record<string, boolean>>({});

  const rows = useMemo<ProcessRow[]>(() => {
    const selectedLower = props.selectedProcess.trim().toLowerCase();
    const eventsMap = new Map<string, NetworkProcessEvent[]>();

    for (const event of props.processEvents) {
      const processName = normalizeProcessName(event.process_name);
      const key = processName.toLowerCase();
      if (selectedLower && key !== selectedLower) {
        continue;
      }
      const list = eventsMap.get(key);
      if (list) {
        list.push(event);
      } else {
        eventsMap.set(key, [event]);
      }
    }

    const result: ProcessRow[] = [];
    const seen = new Set<string>();

    for (const processInfo of props.processList) {
      const processName = normalizeProcessName(processInfo.name);
      const key = processName.toLowerCase();
      if (selectedLower && key !== selectedLower) {
        continue;
      }
      seen.add(key);
      const records = (eventsMap.get(key) || []).slice().sort((a, b) => (b.timestamp || 0) - (a.timestamp || 0));
      let totalCount = 0;
      let latestTimestamp = 0;
      for (const ev of records) {
        totalCount += eventCount(ev);
        latestTimestamp = Math.max(latestTimestamp, ev.timestamp || 0);
      }
      result.push({ key, processName, processInfo, totalCount, latestTimestamp, records });
    }

    for (const [key, recordsRaw] of eventsMap.entries()) {
      if (seen.has(key)) continue;
      const records = recordsRaw.slice().sort((a, b) => (b.timestamp || 0) - (a.timestamp || 0));
      let totalCount = 0;
      let latestTimestamp = 0;
      for (const ev of records) {
        totalCount += eventCount(ev);
        latestTimestamp = Math.max(latestTimestamp, ev.timestamp || 0);
      }
      result.push({
        key,
        processName: normalizeProcessName(records[0]?.process_name || key),
        totalCount,
        latestTimestamp,
        records,
      });
    }

    result.sort((a, b) => {
      const aPinned = pinnedProcessName && a.processName.toLowerCase() === pinnedProcessName.toLowerCase();
      const bPinned = pinnedProcessName && b.processName.toLowerCase() === pinnedProcessName.toLowerCase();
      if (aPinned && !bPinned) return -1;
      if (!aPinned && bPinned) return 1;
      if (b.latestTimestamp !== a.latestTimestamp) return b.latestTimestamp - a.latestTimestamp;
      return a.processName.localeCompare(b.processName, "zh-CN");
    });

    return result;
  }, [pinnedProcessName, props.processEvents, props.processList, props.selectedProcess]);

  return (
    <>
      <div className="content-actions" style={{ gap: 8, alignItems: "center", flexWrap: "wrap" }}>
        <button className="btn" onClick={props.onRefreshProcessList} disabled={props.isLoadingProcessList}>刷新进程列表</button>
        {!props.isMonitoring ? (
          <button className="btn" onClick={props.onStartMonitor}>开始监视</button>
        ) : (
          <button className="btn" onClick={props.onStopMonitor}>停止监视</button>
        )}
        <button className="btn" onClick={props.onClearEvents}>清空</button>
        {props.isMonitoring && <span style={{ fontSize: 12, color: "#888" }}>监视中，每 2 秒刷新…</span>}
      </div>

      <div className="content-actions" style={{ gap: 8, alignItems: "center", flexWrap: "wrap", marginTop: 6 }}>
        <span style={{ fontSize: 12, color: "#aaa" }}>进程：</span>
        <select
          className="text-input"
          style={{ minWidth: 280 }}
          value={props.selectedProcess}
          onChange={(e) => props.onSelectProcess(e.target.value)}
        >
          <option value="">全部进程</option>
          {props.processList.map((p) => (
            <option key={p.name.toLowerCase()} value={p.name}>{p.name}</option>
          ))}
        </select>
        <span style={{ fontSize: 12, color: "#888" }}>共 {rows.length} 个（含监视事件）</span>
      </div>

      {props.processListStatus && <div className="status">{props.processListStatus}</div>}
      {props.processEventsStatus && <div className="status">{props.processEventsStatus}</div>}

      {rows.length === 0 ? (
        <div className="status">暂无事件</div>
      ) : (
        <div style={{ marginTop: 8, border: "1px solid #2a2a2a", borderRadius: 8, overflow: "hidden" }}>
          {rows.map((row, idx) => {
            const pinned = pinnedProcessName && row.processName.toLowerCase() === pinnedProcessName.toLowerCase();
            const expanded = expandedMap[row.key] ?? true;
            return (
              <div key={row.key} style={{ borderTop: idx === 0 ? "none" : "1px solid #2a2a2a" }}>
                <div
                  style={{
                    display: "grid",
                    gridTemplateColumns: "minmax(240px, 2fr) minmax(90px, 0.8fr) minmax(90px, 0.8fr) minmax(90px, 0.8fr) minmax(120px, 1fr) auto",
                    gap: 8,
                    alignItems: "center",
                    padding: "8px 10px",
                    background: pinned ? "#2b3650" : "#1f1f1f",
                  }}
                >
                  <div style={{ minWidth: 0 }}>
                    <div style={{ display: "flex", alignItems: "center", gap: 8, minWidth: 0 }}>
                      <strong style={{ fontFamily: "monospace", overflow: "hidden", textOverflow: "ellipsis", whiteSpace: "nowrap" }}>{row.processName}</strong>
                      {row.processInfo?.pid ? <span style={{ fontSize: 12, color: "#aaa" }}>PID {row.processInfo.pid}</span> : null}
                    </div>
                    {row.processInfo?.exe_path ? (
                      <div style={{ fontSize: 11, color: "#888", marginTop: 2, overflow: "hidden", textOverflow: "ellipsis", whiteSpace: "nowrap" }}>
                        {row.processInfo.exe_path}
                      </div>
                    ) : null}
                  </div>

                  <div style={{ fontSize: 12, color: "#aaa" }}>记录 {row.records.length}</div>
                  <div style={{ fontSize: 12, color: "#aaa" }}>次数 {row.totalCount}</div>
                  <div style={{ fontSize: 12, color: "#aaa" }}>连接 {row.records.length}</div>
                  <div style={{ fontSize: 12, color: "#aaa", fontFamily: "monospace" }}>最近 {formatTimestamp(row.latestTimestamp)}</div>

                  <div style={{ display: "flex", alignItems: "center", gap: 6 }}>
                    <button
                      className="btn"
                      style={{ padding: "2px 8px", minWidth: 56, fontSize: 12 }}
                      onClick={() => setExpandedMap((prev) => ({ ...prev, [row.key]: !(prev[row.key] ?? true) }))}
                    >
                      {expanded ? "折叠" : "展开"}
                    </button>
                    <button
                      className="btn"
                      style={{ padding: "2px 8px", minWidth: 72, fontSize: 12 }}
                      onClick={() => setPinnedProcessName(pinned ? "" : row.processName)}
                    >
                      {pinned ? "取消置顶" : "置顶"}
                    </button>
                  </div>
                </div>

                {expanded ? (
                  row.records.length === 0 ? (
                    <div style={{ padding: "8px 12px", fontSize: 12, color: "#888", background: "#161616" }}>暂无连接事件</div>
                  ) : (
                    <table style={{ width: "100%", borderCollapse: "collapse", fontSize: 12, background: "#161616" }}>
                      <thead>
                        <tr style={{ background: "#222" }}>
                          <th style={{ padding: "6px 8px", textAlign: "left", borderBottom: "1px solid #333" }}>时间</th>
                          <th style={{ padding: "6px 8px", textAlign: "left", borderBottom: "1px solid #333" }}>协议</th>
                          <th style={{ padding: "6px 8px", textAlign: "left", borderBottom: "1px solid #333" }}>DNS</th>
                          <th style={{ padding: "6px 8px", textAlign: "left", borderBottom: "1px solid #333" }}>IP / 目标</th>
                          <th style={{ padding: "6px 8px", textAlign: "left", borderBottom: "1px solid #333" }}>路由</th>
                          <th style={{ padding: "6px 8px", textAlign: "right", borderBottom: "1px solid #333" }}>次数</th>
                        </tr>
                      </thead>
                      <tbody>
                        {row.records.map((ev, i) => {
                          const routeGroup = [routeLabel(ev), ev.group].filter(Boolean).join(" / ");
                          return (
                            <tr key={`${row.key}-${ev.kind}-${ev.timestamp}-${i}`} style={{ borderBottom: "1px solid #222" }}>
                              <td style={{ padding: "4px 8px", fontFamily: "monospace", whiteSpace: "nowrap" }}>{formatTimestamp(ev.timestamp)}</td>
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
                  )
                ) : null}
              </div>
            );
          })}
        </div>
      )}
    </>
  );
}
