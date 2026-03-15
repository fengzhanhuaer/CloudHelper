import { useState } from "react";
import type { NetworkAssistantStatus } from "../types";

type NetworkAssistantTabProps = {
  status: NetworkAssistantStatus;
  selectedNode: string;
  onSelectedNodeChange: (value: string) => void;
  isOperating: boolean;
  operateStatus: string;
  onRefreshStatus: () => void;
  onSwitchDirect: () => void;
  onSwitchGlobal: () => void;
  onRestoreDirect: () => void;
};

export function NetworkAssistantTab(props: NetworkAssistantTabProps) {
  const [subTab, setSubTab] = useState<"settings" | "status">("settings");

  return (
    <div className="content-block">
      <h2>网络助手</h2>

      <div className="subtab-list" style={{ marginBottom: 12 }}>
        <button className={`subtab-btn ${subTab === "settings" ? "active" : ""}`} onClick={() => setSubTab("settings")}>设置</button>
        <button className={`subtab-btn ${subTab === "status" ? "active" : ""}`} onClick={() => setSubTab("status")}>状态</button>
      </div>

      {subTab === "status" ? (
        <>
          <div className="identity-card">
            <div>当前模式：{props.status.mode || "direct"}</div>
            <div>SOCKS5 监听：{props.status.socks5_listen || "127.0.0.1:10808"}</div>
            <div>隧道路由：{props.status.tunnel_route || "/api/ws/tunnel/cloudserver"}</div>
            <div>隧道状态：{props.status.tunnel_status || "未启用"}</div>
            <div>系统代理：{props.status.system_proxy_status || "未设置"}</div>
            <div>复用连接：{props.status.mux_connected ? "已连接" : "未连接"}</div>
            <div>活跃流数：{props.status.mux_active_streams ?? 0}</div>
            <div>重连次数：{props.status.mux_reconnects ?? 0}</div>
            <div>最近收包：{props.status.mux_last_recv || "-"}</div>
            <div>最近心跳：{props.status.mux_last_pong || "-"}</div>
          </div>

          <div className="content-actions">
            <button className="btn" onClick={props.onRefreshStatus} disabled={props.isOperating}>刷新状态</button>
          </div>
        </>
      ) : (
        <>
          <div className="identity-card">
            <div>探针节点：</div>
            <select
              className="input"
              value={props.selectedNode}
              onChange={(event) => props.onSelectedNodeChange(event.target.value)}
              disabled={props.isOperating}
            >
              {(props.status.available_nodes?.length ? props.status.available_nodes : ["cloudserver"]).map((node) => (
                <option key={node} value={node}>{node}</option>
              ))}
            </select>
          </div>

          <div className="content-actions">
            <button className="btn" onClick={props.onSwitchDirect} disabled={props.isOperating}>切换直连</button>
            <button className="btn" onClick={props.onSwitchGlobal} disabled={props.isOperating}>切换全局</button>
            <button className="btn" onClick={props.onRestoreDirect} disabled={props.isOperating}>恢复系统代理</button>
          </div>
        </>
      )}

      <div className="status">{props.operateStatus}</div>
      <div className="status">{props.status.last_error}</div>
      <div className="status">规则模式将在 V2 开放。</div>
    </div>
  );
}
