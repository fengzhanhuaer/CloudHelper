import { useEffect, useState } from "react";
import { PingProbeChain } from "../../../../wailsjs/go/main/App";
import { LinkManageTab } from "./LinkManageTab";
import { NetworkAssistantDNSCachePanel } from "./NetworkAssistantDNSCachePanel";
import { NetworkAssistantLogsPanel } from "./NetworkAssistantLogsPanel";
import { NetworkAssistantMonitorPanel } from "./NetworkAssistantMonitorPanel";
import type {
  NetworkAssistantDNSCacheEntry,
  NetworkAssistantDNSUpstreamConfig,
  NetworkAssistantLogFilterSource,
  NetworkAssistantRuleAction,
  NetworkAssistantRuleConfig,
  NetworkAssistantRuleGroupConfig,
  NetworkAssistantStatus,
  NetworkProcessInfo,
  NetworkProcessEvent,
} from "../types";

const modeLabels: Record<string, string> = {
  direct: "直连模式",
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
  onSwitchTUN: () => void;
  ruleConfig: NetworkAssistantRuleConfig | null;
  isLoadingRuleConfig: boolean;
  ruleConfigStatus: string;
  isSyncingRuleRoutes: boolean;
  ruleRoutesSyncStatus: string;
  onRefreshRuleConfig: () => void;
  onUploadRuleRoutes: () => void;
  onDownloadRuleRoutes: () => void;
  onSetRulePolicy: (group: string, action: NetworkAssistantRuleAction, tunnelNodeID?: string) => void;
  onInstallTUN: () => void;
  onEnableTUN: () => void;
  onCloseTUN: () => void;
  dnsUpstreamConfig: NetworkAssistantDNSUpstreamConfig;
  isLoadingDNSConfig: boolean;
  dnsConfigStatus: string;
  onRefreshDNSConfig: () => void;
  onSaveDNSConfig: (cfg: NetworkAssistantDNSUpstreamConfig) => void;
  dnsCacheEntries: NetworkAssistantDNSCacheEntry[];
  dnsCacheQuery: string;
  isDNSCacheLoading: boolean;
  dnsCacheStatus: string;
  onDNSCacheQueryChange: (value: string) => void;
  onQueryDNSCache: (query: string) => void;
  processList: NetworkProcessInfo[];
  isLoadingProcessList: boolean;
  processListStatus: string;
  selectedProcess: string;
  isMonitoring: boolean;
  processEvents: NetworkProcessEvent[];
  processEventsStatus: string;
  onRefreshProcessList: () => void;
  onSelectProcess: (name: string) => void;
  onStartMonitor: () => void;
  onStopMonitor: () => void;
  onClearEvents: () => void;
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

export function NetworkAssistantTab(props: NetworkAssistantTabProps) {
  const [subTab, setSubTab] = useState<"settings" | "dns" | "cache" | "monitor" | "link" | "forward" | "driver" | "status" | "logs">("settings");
  const [dnsEditCIDR, setDnsEditCIDR] = useState("");
  const [dnsEditDirty, setDnsEditDirty] = useState(false);

  type TunnelPingState = { ok: boolean | null; durationMS: number | null; message: string };
  const [tunnelPingStates, setTunnelPingStates] = useState<Record<string, TunnelPingState>>({});
  const [tunnelPingingID, setTunnelPingingID] = useState("");

  async function handlePingTunnel(chainID: string) {
    if (tunnelPingingID || !chainID.trim()) return;
    setTunnelPingingID(chainID);
    setTunnelPingStates((prev) => ({ ...prev, [chainID]: { ok: null, durationMS: null, message: "测试中..." } }));
    try {
      // 按当前选中的链路目标进行测试。
      const result = await PingProbeChain(chainID);
      setTunnelPingStates((prev) => ({
        ...prev,
        [chainID]: { ok: result.ok, durationMS: result.duration_ms ?? null, message: result.message ?? (result.ok ? "成功" : "失败") },
      }));
      // 测试成功后立即刷新状态，避免“测试已建链”与“保活未建立”短时不同步。
      if (result.ok) {
        props.onRefreshStatus();
      }
    } catch (err: unknown) {
      const msg = err instanceof Error ? err.message : String(err);
      setTunnelPingStates((prev) => ({ ...prev, [chainID]: { ok: false, durationMS: null, message: msg } }));
    } finally {
      setTunnelPingingID("");
    }
  }

  useEffect(() => {
    if (subTab !== "settings" || props.status.mode !== "tun") {
      return;
    }
    if (props.ruleConfig || props.isLoadingRuleConfig) {
      return;
    }
    props.onRefreshRuleConfig();
  }, [props.isLoadingRuleConfig, props.onRefreshRuleConfig, props.ruleConfig, props.status.mode, subTab]);

  useEffect(() => {
    if (subTab !== "dns") {
      return;
    }
    if (!dnsEditDirty) {
      setDnsEditCIDR(props.dnsUpstreamConfig.fake_ip_cidr ?? "");
    }
  }, [subTab, props.dnsUpstreamConfig, dnsEditDirty]);

  useEffect(() => {
    if (subTab !== "dns") {
      return;
    }
    if (!props.isLoadingDNSConfig) {
      props.onRefreshDNSConfig();
    }
  // eslint-disable-next-line react-hooks/exhaustive-deps
  }, [subTab]);

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
    const keepalive = (props.status.group_keepalive || []).find(
      (item) => (item.group || "").trim().toLowerCase() === (group.group || "").trim().toLowerCase(),
    );

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
        {(activeTunnelID || keepalive) && (
          <div style={{ display: "flex", alignItems: "center", gap: 8, marginTop: 6, flexWrap: "wrap" }}>
            {activeTunnelID && (
              <>
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
              </>
            )}
            {keepalive && (
              <span
                style={{
                  fontSize: 12,
                  color:
                    keepalive.action === "tunnel"
                      ? keepalive.connected
                        ? "#4ade80"
                        : "#f87171"
                      : "#aaa",
                }}
                title={
                  keepalive.action === "tunnel"
                    ? `最近心跳：${keepalive.last_pong || "-"}，最近收包：${keepalive.last_recv || "-"}`
                    : undefined
                }
              >
                {keepalive.action === "tunnel"
                  ? `保活：${keepalive.status || "-"}${keepalive.tunnel_label ? ` (${keepalive.tunnel_label})` : ""}`
                  : `保活：${keepalive.status || "-"}`}
              </span>
            )}
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
                    ? `✅ ${pingState.message}`
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
        <button className={`subtab-btn ${subTab === "dns" ? "active" : ""}`} onClick={() => setSubTab("dns")}>DNS 配置</button>
        <button className={`subtab-btn ${subTab === "cache" ? "active" : ""}`} onClick={() => setSubTab("cache")}>DNS 缓存</button>
        <button className={`subtab-btn ${subTab === "monitor" ? "active" : ""}`} onClick={() => setSubTab("monitor")}>网络监视</button>
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
            <div>隧道路由：{props.status.tunnel_route || "/api/ws/tunnel/direct"}</div>
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
            <div>
              当前模式：{modeLabels[props.status.mode] || props.status.mode || "直连模式"}
              {props.ruleRoutesSyncStatus ? ` ｜ ${props.ruleRoutesSyncStatus}` : ""}
            </div>
          </div>

          <div className="content-actions">
            <button className="btn" onClick={props.onSwitchDirect} disabled={props.isOperating}>直连模式</button>
            <button className="btn" onClick={props.onSwitchTUN} disabled={props.isOperating}>TUN 模式</button>
            <button className="btn" onClick={props.onRefreshRuleConfig} disabled={props.isOperating || props.isLoadingRuleConfig}>
              {props.isLoadingRuleConfig ? "加载规则中..." : "刷新规则组"}
            </button>
            <button
              className="btn"
              onClick={props.onUploadRuleRoutes}
              disabled={props.isOperating || props.isSyncingRuleRoutes}
            >
              {props.isSyncingRuleRoutes ? "处理中..." : "上传 rule_routes.txt"}
            </button>
            <button className="btn" onClick={props.onDownloadRuleRoutes} disabled={props.isOperating || props.isSyncingRuleRoutes}>
              {props.isSyncingRuleRoutes ? "处理中..." : "下载 rule_routes.txt"}
            </button>
          </div>
          {props.ruleConfig ? (
            <div className="rule-policy-group-list">
              {props.ruleConfig.groups.map((group) => renderRuleGroupRow(group, `组：${group.group}`))}
              {renderRuleGroupRow(props.ruleConfig.fallback, "兜底组（未命中规则）")}
            </div>
          ) : null}
        </>
      ) : subTab === "dns" ? (
        <>
          <div className="identity-card">
            <div>Fake IP 模式：当 DNS 解析命中规则时，返回虚假 IP，实际流量由 TUN 层按域名路由</div>
          </div>
          <div className="content-actions">
            <button
              className="btn"
              onClick={() => {
                setDnsEditCIDR(props.dnsUpstreamConfig.fake_ip_cidr ?? "");
                setDnsEditDirty(false);
              }}
              disabled={props.isLoadingDNSConfig}
            >
              重置编辑
            </button>
            <button
              className="btn"
              onClick={() => {
                props.onSaveDNSConfig({
                  ...props.dnsUpstreamConfig,
                  fake_ip_cidr: dnsEditCIDR.trim(),
                });
                setDnsEditDirty(false);
              }}
              disabled={props.isLoadingDNSConfig || !dnsEditDirty}
            >
              {props.isLoadingDNSConfig ? "保存中..." : "保存配置"}
            </button>
            <button
              className="btn"
              onClick={props.onRefreshDNSConfig}
              disabled={props.isLoadingDNSConfig}
            >
              {props.isLoadingDNSConfig ? "加载中..." : "刷新配置"}
            </button>
          </div>
          <div className="status">{props.dnsConfigStatus}</div>
          <div className="rule-policy-group-list">
            <div className="rule-policy-group">
              <div className="rule-policy-group-title">Fake IP CIDR 段</div>
              <div className="rule-policy-group-desc">分配给 Fake IP 的地址段，例如：198.18.0.0/15</div>
              <input
                className="text-input"
                type="text"
                value={dnsEditCIDR}
                placeholder="198.18.0.0/15"
                onChange={(e) => { setDnsEditCIDR(e.target.value); setDnsEditDirty(true); }}
                disabled={props.isLoadingDNSConfig}
              />
            </div>
            <div className="rule-policy-group">
              <div className="rule-policy-group-title">Fake IP 排除来源</div>
              <div className="rule-policy-group-desc">已改为使用规则组 direct 作为排除来源。命中 direct 组的域名不会分配 Fake IP，请在规则组中配置 direct 策略。</div>
            </div>
          </div>
        </>
      ) : subTab === "link" ? (
        <LinkManageTab key="link-tab" controllerBaseUrl={props.controllerBaseUrl} sessionToken={props.sessionToken} initialSubTab="list" />
      ) : subTab === "forward" ? (
        <LinkManageTab key="forward-tab" controllerBaseUrl={props.controllerBaseUrl} sessionToken={props.sessionToken} initialSubTab="forward" />
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
      ) : subTab === "cache" ? (
        <NetworkAssistantDNSCachePanel
          dnsCacheEntries={props.dnsCacheEntries}
          dnsCacheQuery={props.dnsCacheQuery}
          isDNSCacheLoading={props.isDNSCacheLoading}
          dnsCacheStatus={props.dnsCacheStatus}
          onDNSCacheQueryChange={props.onDNSCacheQueryChange}
          onQueryDNSCache={props.onQueryDNSCache}
        />
      ) : subTab === "monitor" ? (
        <NetworkAssistantMonitorPanel
          isMonitoring={props.isMonitoring}
          processList={props.processList}
          isLoadingProcessList={props.isLoadingProcessList}
          processListStatus={props.processListStatus}
          selectedProcess={props.selectedProcess}
          processEvents={props.processEvents}
          processEventsStatus={props.processEventsStatus}
          onRefreshProcessList={props.onRefreshProcessList}
          onSelectProcess={props.onSelectProcess}
          onStartMonitor={props.onStartMonitor}
          onStopMonitor={props.onStopMonitor}
          onClearEvents={props.onClearEvents}
        />
      ) : subTab === "logs" ? (
        <NetworkAssistantLogsPanel
          logLines={props.logLines}
          onLogLinesChange={props.onLogLinesChange}
          isLoadingLogs={props.isLoadingLogs}
          logStatus={props.logStatus}
          logCopyStatus={props.logCopyStatus}
          logContent={props.logContent}
          logSourceFilter={props.logSourceFilter}
          onLogSourceFilterChange={props.onLogSourceFilterChange}
          logCategoryFilter={props.logCategoryFilter}
          onLogCategoryFilterChange={props.onLogCategoryFilterChange}
          logCategories={props.logCategories}
          logVisibleCount={props.logVisibleCount}
          logTotalCount={props.logTotalCount}
          logAutoScroll={props.logAutoScroll}
          onLogAutoScrollChange={props.onLogAutoScrollChange}
          onRefreshLogs={props.onRefreshLogs}
          onCopyLogs={props.onCopyLogs}
          active={subTab === "logs"}
        />
      ) : null}

      {subTab !== "link" && subTab !== "forward" ? (
        <>
          <div className="status">{props.operateStatus}</div>
          <div className="status">{props.status.last_error}</div>
        </>
      ) : null}
    </div>
  );
}
