import { useEffect, useRef, useState } from "react";
import { LinkManageTab } from "./LinkManageTab";
import type { NetworkAssistantLogFilterSource, NetworkAssistantStatus } from "../types";

type NetworkAssistantTabProps = {
  controllerBaseUrl: string;
  sessionToken: string;
  status: NetworkAssistantStatus;
  selectedNode: string;
  onSelectedNodeChange: (value: string) => void;
  isOperating: boolean;
  operateStatus: string;
  onRefreshStatus: () => void;
  onSwitchDirect: () => void;
  onInstallTUN: () => void;
  onEnableTUN: () => void;
  onRestoreDirect: () => void;
  logLines: number;
  onLogLinesChange: (value: number) => void;
  isLoadingLogs: boolean;
  logStatus: string;
  logCopyStatus: string;
  logContent: string;
  logSourceFilter: NetworkAssistantLogFilterSource;
  onLogSourceFilterChange: (value: NetworkAssistantLogFilterSource) => void;
  logCategoryFilter: string;
  onLogCategoryFilterChange: (value: string) => void;
  logCategories: string[];
  logVisibleCount: number;
  logTotalCount: number;
  logAutoScroll: boolean;
  onLogAutoScrollChange: (value: boolean) => void;
  onRefreshLogs: () => void;
  onCopyLogs: () => void;
};

const categoryLabels: Record<string, string> = {
  general: "通用",
  init: "初始化",
  mode: "模式",
  proxy: "系统代理",
  socks: "本地代理",
  mux: "隧道复用",
  tunnel: "隧道",
  node: "节点",
  whitelist: "白名单",
  error: "错误",
  open: "打开流",
  read: "读取",
  write: "写入",
  state: "状态",
};

export function NetworkAssistantTab(props: NetworkAssistantTabProps) {
  const [subTab, setSubTab] = useState<"settings" | "link" | "driver" | "status" | "logs">("settings");
  const outputRef = useRef<HTMLPreElement | null>(null);

  useEffect(() => {
    if (!props.logAutoScroll || !outputRef.current || subTab !== "logs") {
      return;
    }
    outputRef.current.scrollTop = outputRef.current.scrollHeight;
  }, [props.logAutoScroll, props.logContent, subTab]);

  useEffect(() => {
    if (subTab !== "logs") {
      return;
    }
    props.onRefreshLogs();
    const timer = window.setInterval(() => {
      props.onRefreshLogs();
    }, 2000);
    return () => window.clearInterval(timer);
  }, [props.onRefreshLogs, subTab]);

  return (
    <div className="content-block">
      <h2>网络助手</h2>

      <div className="subtab-list" style={{ marginBottom: 12 }}>
        <button className={`subtab-btn ${subTab === "settings" ? "active" : ""}`} onClick={() => setSubTab("settings")}>模式切换</button>
        <button className={`subtab-btn ${subTab === "link" ? "active" : ""}`} onClick={() => setSubTab("link")}>链路管理</button>
        <button className={`subtab-btn ${subTab === "driver" ? "active" : ""}`} onClick={() => setSubTab("driver")}>驱动设置</button>
        <button className={`subtab-btn ${subTab === "status" ? "active" : ""}`} onClick={() => setSubTab("status")}>状态</button>
        <button className={`subtab-btn ${subTab === "logs" ? "active" : ""}`} onClick={() => setSubTab("logs")}>日志</button>
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
      ) : subTab === "settings" ? (
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
            <button className="btn" onClick={props.onRestoreDirect} disabled={props.isOperating}>恢复系统代理</button>
          </div>
        </>
      ) : subTab === "link" ? (
        <LinkManageTab controllerBaseUrl={props.controllerBaseUrl} sessionToken={props.sessionToken} />
      ) : subTab === "driver" ? (
        <>
          <div className="identity-card">
            <div>TUN 支持：{props.status.tun_supported ? "是" : "否"}</div>
            <div>TUN 状态：{props.status.tun_status || "未安装"}</div>
            <div>TUN 库：{props.status.tun_library_path || "-"}</div>
            <div>已安装：{props.status.tun_installed ? "是" : "否"}</div>
            <div>已启用：{props.status.tun_enabled ? "是" : "否"}</div>
          </div>

          <div className="content-actions">
            <button className="btn" onClick={props.onInstallTUN} disabled={props.isOperating || !props.status.tun_supported}>安装 TUN</button>
            <button className="btn" onClick={props.onEnableTUN} disabled={props.isOperating || !props.status.tun_supported}>启用 TUN</button>
            <button className="btn" onClick={props.onRefreshStatus} disabled={props.isOperating}>刷新状态</button>
          </div>
        </>
      ) : (
        <>
          <div className="identity-card">
            <div className="row" style={{ marginBottom: 0 }}>
              <label htmlFor="network-log-lines">显示行数</label>
              <input
                id="network-log-lines"
                className="input"
                type="number"
                min={1}
                max={2000}
                value={props.logLines}
                onChange={(event) => props.onLogLinesChange(Number(event.target.value) || 200)}
                disabled={props.isLoadingLogs}
              />
            </div>
            <div className="row" style={{ marginBottom: 0 }}>
              <label htmlFor="network-log-source">来源</label>
              <select
                id="network-log-source"
                className="input"
                value={props.logSourceFilter}
                onChange={(event) => props.onLogSourceFilterChange(event.target.value as NetworkAssistantLogFilterSource)}
                disabled={props.isLoadingLogs}
              >
                <option value="all">全部</option>
                <option value="manager">管理端</option>
                <option value="controller">主控端</option>
              </select>
            </div>
            <div className="row" style={{ marginBottom: 0 }}>
              <label htmlFor="network-log-category">分类</label>
              <select
                id="network-log-category"
                className="input"
                value={props.logCategoryFilter}
                onChange={(event) => props.onLogCategoryFilterChange(event.target.value)}
                disabled={props.isLoadingLogs}
              >
                <option value="all">全部</option>
                {props.logCategories.map((item) => (
                  <option key={item} value={item}>{categoryLabels[item] || item}</option>
                ))}
              </select>
            </div>
          </div>

          <div className="content-actions">
            <button className="btn" onClick={props.onRefreshLogs} disabled={props.isLoadingLogs}>
              {props.isLoadingLogs ? "刷新中..." : "刷新日志"}
            </button>
            <button className="btn" onClick={props.onCopyLogs} disabled={props.isLoadingLogs || !props.logContent.trim()}>
              复制日志
            </button>
            <label className="log-auto-scroll-toggle">
              <input
                type="checkbox"
                checked={props.logAutoScroll}
                onChange={(event) => props.onLogAutoScrollChange(event.target.checked)}
                disabled={props.isLoadingLogs}
              />
              自动滚动到底部
            </label>
          </div>

          <div className="status">{props.logStatus}</div>
          <div className="status">日志筛选：{props.logVisibleCount}/{props.logTotalCount}</div>
          <div className="status">{props.logCopyStatus || "复制状态：未执行"}</div>
          <pre ref={outputRef} className="log-viewer-output">{props.logContent || "暂无网络助手日志"}</pre>
        </>
      )}

      {subTab !== "link" ? (
        <>
          <div className="status">{props.operateStatus}</div>
          <div className="status">{props.status.last_error}</div>
          <div className="status">规则模式将在 V2 开放。</div>
        </>
      ) : null}
    </div>
  );
}
