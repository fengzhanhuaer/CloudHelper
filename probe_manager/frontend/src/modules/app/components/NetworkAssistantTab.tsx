import { useEffect, useRef, useState } from "react";
import { PingProbeChain } from "../../../../wailsjs/go/main/App";
import { LinkManageTab } from "./LinkManageTab";
import type {
  NetworkAssistantLogFilterSource,
  NetworkAssistantRuleAction,
  NetworkAssistantRuleConfig,
  NetworkAssistantRuleGroupConfig,
  NetworkAssistantStatus,
} from "../types";

const modeLabels: Record<string, string> = {
  direct: "直连模式",
  rule: "规则模式",
  global: "全局模式",
  tun: "TUN 模式",
};

type NetworkAssistantTabProps = {
  controllerBaseUrl: string;
  sessionToken: string;
  status: NetworkAssistantStatus;
  isOperating: boolean;
  operateStatus: string;
  onRefreshStatus: () => void;
  onSwitchDirect: () => void;
  onSwitchRule: () => void;
  ruleConfig: NetworkAssistantRuleConfig | null;
  isLoadingRuleConfig: boolean;
  ruleConfigStatus: string;
  onRefreshRuleConfig: () => void;
  onSetRulePolicy: (group: string, action: NetworkAssistantRuleAction, tunnelNodeID?: string) => void;
  onInstallTUN: () => void;
  onEnableTUN: () => void;
  onCloseTUN: () => void;
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
  rule: "规则",
  whitelist: "白名单",
  error: "错误",
  open: "打开流",
  read: "读取",
  write: "写入",
  state: "状态",
};

export function NetworkAssistantTab(props: NetworkAssistantTabProps) {
  const [subTab, setSubTab] = useState<"settings" | "link" | "forward" | "driver" | "status" | "logs">("settings");
  const outputRef = useRef<HTMLPreElement | null>(null);

  type TunnelPingState = { ok: boolean | null; durationMS: number | null; message: string };
  const [tunnelPingStates, setTunnelPingStates] = useState<Record<string, TunnelPingState>>({});
  const [tunnelPingingID, setTunnelPingingID] = useState("");

  async function handlePingTunnel(chainID: string) {
    if (tunnelPingingID || !chainID.trim()) return;
    setTunnelPingingID(chainID);
    setTunnelPingStates((prev) => ({ ...prev, [chainID]: { ok: null, durationMS: null, message: "测试中..." } }));
    try {
      const result = await PingProbeChain(chainID);
      setTunnelPingStates((prev) => ({
        ...prev,
        [chainID]: { ok: result.ok, durationMS: result.duration_ms ?? null, message: result.message ?? (result.ok ? "成功" : "失败") },
      }));
    } catch (err: unknown) {
      const msg = err instanceof Error ? err.message : String(err);
      setTunnelPingStates((prev) => ({ ...prev, [chainID]: { ok: false, durationMS: null, message: msg } }));
    } finally {
      setTunnelPingingID("");
    }
  }

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

  useEffect(() => {
    if (subTab !== "settings" || props.status.mode !== "rule") {
      return;
    }
    if (props.ruleConfig || props.isLoadingRuleConfig) {
      return;
    }
    props.onRefreshRuleConfig();
  }, [props.isLoadingRuleConfig, props.onRefreshRuleConfig, props.ruleConfig, props.status.mode, subTab]);

  function isRuleOptionSelected(group: NetworkAssistantRuleGroupConfig, action: NetworkAssistantRuleAction, tunnelNodeID?: string): boolean {
    if (group.action !== action) {
      return false;
    }
    if (action !== "tunnel") {
      return true;
    }
    const selectedTunnel = (group.tunnel_node_id || "").trim().toLowerCase();
    const optionTunnel = (tunnelNodeID || "").trim().toLowerCase();
    return selectedTunnel === optionTunnel;
  }

  function renderRuleGroupRow(group: NetworkAssistantRuleGroupConfig, title: string) {
    const tunnelOptions = Array.isArray(group.tunnel_options) ? group.tunnel_options : [];
    const tunnelOptionLabels = group.tunnel_option_labels || {};
    const optionItems: Array<{ key: string; label: string; action: NetworkAssistantRuleAction; tunnelNodeID?: string }> = [
      { key: `${group.group}:direct`, label: "直连", action: "direct" },
      { key: `${group.group}:reject`, label: "拒绝", action: "reject" },
      ...tunnelOptions.map((nodeID) => ({
        key: `${group.group}:tunnel:${nodeID}`,
        label: `隧道 ${tunnelOptionLabels[nodeID] || nodeID}`,
        action: "tunnel" as const,
        tunnelNodeID: nodeID,
      })),
    ];

    // The currently selected chain ID when action is tunnel
    const activeTunnelID = group.action === "tunnel" ? (group.tunnel_node_id || "").trim() : "";
    const activeTunnelLabel = activeTunnelID ? (tunnelOptionLabels[activeTunnelID] || activeTunnelID) : "";
    const pingState = activeTunnelID ? tunnelPingStates[activeTunnelID] : undefined;

    return (
      <div key={group.group} className="rule-policy-group-row">
        <div className="rule-policy-group-title">{title}</div>
        <div className="rule-policy-options-flat">
          {optionItems.map((item) => {
            const selected = isRuleOptionSelected(group, item.action, item.tunnelNodeID);
            return (
              <button
                key={item.key}
                className={`rule-policy-option-flat ${selected ? "active" : ""}`}
                disabled={props.isOperating || props.isLoadingRuleConfig}
                onClick={() => props.onSetRulePolicy(group.group, item.action, item.tunnelNodeID)}
              >
                {item.label}
              </button>
            );
          })}
        </div>
        {activeTunnelID && (
          <div style={{ display: "flex", alignItems: "center", gap: 8, marginTop: 6, flexWrap: "wrap" }}>
            <span style={{ fontSize: 12, color: "#aaa" }}>
              当前链路：{activeTunnelLabel}
            </span>
            <button
              className="btn"
              id={`tunnel-ping-btn-${group.group}`}
              onClick={() => void handlePingTunnel(activeTunnelID)}
              disabled={!!tunnelPingingID}
              style={{
                fontSize: 11,
                padding: "2px 10px",
                minWidth: 52,
                background: tunnelPingingID === activeTunnelID ? "#555" : undefined,
              }}
            >
              {tunnelPingingID === activeTunnelID ? "测试中" : "测试链路"}
            </button>
            {pingState && (
              <span
                style={{
                  fontSize: 12,
                  color:
                    pingState.ok === null
                      ? "#aaa"
                      : pingState.ok
                        ? "#4ade80"
                        : "#f87171",
                }}
              >
                {pingState.ok === null
                  ? `⏳ ${pingState.message}`
                  : pingState.ok
                    ? `✅ 已通 (${pingState.durationMS}ms)`
                    : `❌ ${pingState.message}`}
              </span>
            )}
          </div>
        )}
      </div>
    );
  }

  return (
    <div className="content-block">
      <h2>网络助手</h2>

      <div className="subtab-list" style={{ marginBottom: 12 }}>
        <button className={`subtab-btn ${subTab === "settings" ? "active" : ""}`} onClick={() => setSubTab("settings")}>模式切换</button>
        <button className={`subtab-btn ${subTab === "link" ? "active" : ""}`} onClick={() => setSubTab("link")}>链路管理</button>
        <button className={`subtab-btn ${subTab === "forward" ? "active" : ""}`} onClick={() => setSubTab("forward")}>端口转发</button>
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
            <div>当前模式：{modeLabels[props.status.mode] || props.status.mode || "直连模式"}</div>
          </div>

          <div className="content-actions">
            <button className="btn" onClick={props.onSwitchDirect} disabled={props.isOperating}>直连模式</button>
            <button className="btn" onClick={props.onSwitchRule} disabled={props.isOperating}>规则模式</button>
            <button className="btn" onClick={props.onRefreshRuleConfig} disabled={props.isOperating || props.isLoadingRuleConfig}>
              {props.isLoadingRuleConfig ? "加载规则中..." : "刷新规则组"}
            </button>
          </div>
          <div className="status">规则文件：{props.ruleConfig?.rule_file_path || "data/rule_routes.txt"}（每行：网址后缀/IP/CIDR,代理组）</div>
          <div className="status">策略文件：data/rule_policies.txt（每行：代理组或@fallback,动作[,链路ID]）</div>
          <div className="status">{props.ruleConfigStatus}</div>
          {props.ruleConfig ? (
            <div className="rule-policy-group-list">
              {renderRuleGroupRow(props.ruleConfig.fallback, "兜底组（未命中规则）")}
              {props.ruleConfig.groups.map((group) => renderRuleGroupRow(group, `组：${group.group}`))}
            </div>
          ) : null}
        </>
      ) : subTab === "link" ? (
        <LinkManageTab controllerBaseUrl={props.controllerBaseUrl} sessionToken={props.sessionToken} initialSubTab="list" />
      ) : subTab === "forward" ? (
        <LinkManageTab controllerBaseUrl={props.controllerBaseUrl} sessionToken={props.sessionToken} initialSubTab="forward" />
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
            <button
              className="btn"
              onClick={props.onCloseTUN}
              disabled={props.isOperating || (!props.status.tun_enabled && props.status.mode !== "tun")}
            >
              关闭 TUN
            </button>
            <button className="btn" onClick={props.onRefreshStatus} disabled={props.isOperating}>刷新状态</button>
          </div>
          <div className="status">关闭 TUN 将切回直连模式，并恢复系统 DNS 与系统代理设置。</div>
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

      {subTab !== "link" && subTab !== "forward" ? (
        <>
          <div className="status">{props.operateStatus}</div>
          <div className="status">{props.status.last_error}</div>
        </>
      ) : null}
    </div>
  );
}
