import { useEffect, useState } from "react";
import {
  applyCloudflareDDNS,
  fetchCloudflareAPIKey,
  fetchCloudflareDDNSRecords,
  fetchCloudflareZeroTrustWhitelist,
  runCloudflareZeroTrustWhitelistSync,
  setCloudflareAPIKey,
  setCloudflareZeroTrustWhitelist,
  setCloudflareZone,
} from "../services/controller-api";
import type { CloudflareDDNSRecord, CloudflareZeroTrustWhitelistState } from "../types";

type CloudflareAssistantTabProps = {
  controllerBaseUrl: string;
  sessionToken: string;
};

type CloudflareSubTab = "settings" | "ddns" | "zerotrust";

const defaultZeroTrustState: CloudflareZeroTrustWhitelistState = {
  enabled: false,
  policy_name: "",
  whitelist_raw: "",
  sync_interval_sec: 300,
  running: false,
  last_run_at: "",
  last_success_at: "",
  last_status: "",
  last_message: "",
  last_policy_id: "",
  last_policy_name: "",
  last_applied_ips: [],
  last_source_lines: 0,
};

export function CloudflareAssistantTab(props: CloudflareAssistantTabProps) {
  const [subTab, setSubTab] = useState<CloudflareSubTab>("settings");
  const [apiKeyInput, setAPIKeyInput] = useState("");
  const [apiConfigured, setAPIConfigured] = useState(false);
  const [zoneNameInput, setZoneNameInput] = useState("");
  const [records, setRecords] = useState<CloudflareDDNSRecord[]>([]);

  const [zeroTrust, setZeroTrust] = useState<CloudflareZeroTrustWhitelistState>(defaultZeroTrustState);
  const [zeroTrustEnabled, setZeroTrustEnabled] = useState(false);
  const [zeroTrustPolicyName, setZeroTrustPolicyName] = useState("");
  const [zeroTrustWhitelistRaw, setZeroTrustWhitelistRaw] = useState("");
  const [zeroTrustSyncIntervalSec, setZeroTrustSyncIntervalSec] = useState("300");

  const [status, setStatus] = useState("正在加载 Cloudflare 配置...");
  const [isLoading, setIsLoading] = useState(false);

  useEffect(() => {
    void loadData();
  }, [props.controllerBaseUrl, props.sessionToken]);

  async function loadData() {
    setIsLoading(true);
    setStatus("正在加载 Cloudflare 配置...");
    try {
      const [api, ddnsRecords, zeroTrustState] = await Promise.all([
        fetchCloudflareAPIKey(props.controllerBaseUrl, props.sessionToken),
        fetchCloudflareDDNSRecords(props.controllerBaseUrl, props.sessionToken),
        fetchCloudflareZeroTrustWhitelist(props.controllerBaseUrl, props.sessionToken),
      ]);
      const fallbackZone = (ddnsRecords[0]?.zone_name || "").trim().toLowerCase();
      const zone = (api.zone_name || "").trim().toLowerCase() || fallbackZone;
      setAPIKeyInput(api.api_key || "");
      setAPIConfigured(api.configured === true);
      setZoneNameInput(zone);
      setRecords(ddnsRecords);

      setZeroTrust(zeroTrustState);
      setZeroTrustEnabled(zeroTrustState.enabled === true);
      setZeroTrustPolicyName(zeroTrustState.policy_name || "");
      setZeroTrustWhitelistRaw(zeroTrustState.whitelist_raw || "");
      setZeroTrustSyncIntervalSec(String(zeroTrustState.sync_interval_sec || 300));

      setStatus(api.configured ? `Cloudflare 配置已加载，已记录 DDNS ${ddnsRecords.length} 条` : "请先在基础设置中保存 API KEY");
    } catch (error) {
      const msg = error instanceof Error ? error.message : "unknown error";
      setStatus(`加载 Cloudflare 数据失败：${msg}`);
    } finally {
      setIsLoading(false);
    }
  }

  async function handleSaveAPIKey() {
    const apiKey = apiKeyInput.trim();
    if (!apiKey) {
      setStatus("API KEY 不能为空");
      return;
    }
    setIsLoading(true);
    setStatus("正在保存 Cloudflare API KEY...");
    try {
      const result = await setCloudflareAPIKey(props.controllerBaseUrl, props.sessionToken, apiKey);
      setAPIKeyInput(result.api_key || "");
      if ((result.zone_name || "").trim()) {
        setZoneNameInput((result.zone_name || "").trim().toLowerCase());
      }
      setAPIConfigured(result.configured === true);
      setStatus("Cloudflare API KEY 已保存");
    } catch (error) {
      const msg = error instanceof Error ? error.message : "unknown error";
      setStatus(`保存 API KEY 失败：${msg}`);
    } finally {
      setIsLoading(false);
    }
  }

  async function handleSaveZone() {
    const zoneName = zoneNameInput.trim().toLowerCase();
    if (!zoneName) {
      setStatus("Zone 域名不能为空");
      return;
    }
    setIsLoading(true);
    setStatus("正在保存 Zone 域名...");
    try {
      const saved = await setCloudflareZone(props.controllerBaseUrl, props.sessionToken, zoneName);
      setZoneNameInput((saved || zoneName).trim().toLowerCase());
      setStatus(`Zone 域名已保存：${saved || zoneName}`);
    } catch (error) {
      const msg = error instanceof Error ? error.message : "unknown error";
      setStatus(`保存 Zone 域名失败：${msg}`);
    } finally {
      setIsLoading(false);
    }
  }

  async function handleApplyDDNS() {
    const zoneName = zoneNameInput.trim().toLowerCase();
    if (!zoneName) {
      setStatus("请输入 Zone 域名（例如 example.com）");
      return;
    }
    setIsLoading(true);
    setStatus("正在为探针自动申请 DDNS...");
    try {
      const result = await applyCloudflareDDNS(props.controllerBaseUrl, props.sessionToken, zoneName);
      setZoneNameInput((result.zone_name || zoneName).trim().toLowerCase());
      setRecords(result.records);
      setStatus(`DDNS 已执行，当前有效记录 ${result.records.length} 条`);
    } catch (error) {
      const msg = error instanceof Error ? error.message : "unknown error";
      setStatus(`自动申请 DDNS 失败：${msg}`);
    } finally {
      setIsLoading(false);
    }
  }

  async function handleSaveZeroTrust() {
    const intervalRaw = Number.parseInt(zeroTrustSyncIntervalSec.trim(), 10);
    const interval = Number.isFinite(intervalRaw) ? Math.max(30, intervalRaw) : 300;
    setIsLoading(true);
    setStatus("正在保存 ZeroTrust 白名单配置...");
    try {
      const result = await setCloudflareZeroTrustWhitelist(props.controllerBaseUrl, props.sessionToken, {
        enabled: zeroTrustEnabled,
        policy_name: zeroTrustPolicyName.trim(),
        whitelist_raw: zeroTrustWhitelistRaw,
        sync_interval_sec: interval,
      });
      setZeroTrust(result);
      setZeroTrustEnabled(result.enabled === true);
      setZeroTrustPolicyName(result.policy_name || "");
      setZeroTrustWhitelistRaw(result.whitelist_raw || "");
      setZeroTrustSyncIntervalSec(String(result.sync_interval_sec || interval));
      setStatus("ZeroTrust 白名单配置已保存");
    } catch (error) {
      const msg = error instanceof Error ? error.message : "unknown error";
      setStatus(`保存 ZeroTrust 配置失败：${msg}`);
    } finally {
      setIsLoading(false);
    }
  }

  async function handleRunZeroTrustNow() {
    setIsLoading(true);
    setStatus("正在执行 ZeroTrust 白名单同步...");
    try {
      const result = await runCloudflareZeroTrustWhitelistSync(props.controllerBaseUrl, props.sessionToken, true);
      setZeroTrust(result);
      const summary = result.last_status || "success";
      const msg = result.last_message || "同步完成";
      setStatus(`ZeroTrust 同步完成：${summary} - ${msg}`);
    } catch (error) {
      const msg = error instanceof Error ? error.message : "unknown error";
      setStatus(`执行 ZeroTrust 同步失败：${msg}`);
    } finally {
      setIsLoading(false);
    }
  }

  return (
    <div className="content-block">
      <h2 style={{ marginBottom: 12 }}>Cloudflare助手</h2>

      <div className="subtab-list" style={{ marginBottom: 12 }}>
        <button className={`subtab-btn ${subTab === "settings" ? "active" : ""}`} onClick={() => setSubTab("settings")} disabled={isLoading}>
          基础设置
        </button>
        <button className={`subtab-btn ${subTab === "ddns" ? "active" : ""}`} onClick={() => setSubTab("ddns")} disabled={isLoading}>
          DDNS
        </button>
        <button className={`subtab-btn ${subTab === "zerotrust" ? "active" : ""}`} onClick={() => setSubTab("zerotrust")} disabled={isLoading}>
          ZeroTrust 白名单
        </button>
      </div>

      {subTab === "settings" ? (
        <div className="identity-card">
          <div className="row">
            <label>Cloudflare API KEY</label>
            <input
              className="input"
              type="password"
              value={apiKeyInput}
              onChange={(event) => setAPIKeyInput(event.target.value)}
              placeholder="输入 Cloudflare API Token"
              disabled={isLoading}
            />
          </div>
          <div className="status-inline">当前状态：{apiConfigured ? "已配置" : "未配置"}</div>
          <div className="content-actions">
            <button className="btn" onClick={() => void handleSaveAPIKey()} disabled={isLoading}>
              保存 API KEY
            </button>
          </div>
        </div>
      ) : subTab === "ddns" ? (
        <div className="identity-card">
          <div className="row">
            <label>Zone 域名</label>
            <input
              className="input"
              value={zoneNameInput}
              onChange={(event) => setZoneNameInput(event.target.value)}
              placeholder="例如：example.com"
              disabled={isLoading || !apiConfigured}
            />
          </div>
          <div className="content-actions">
            <button className="btn" onClick={() => void handleSaveZone()} disabled={isLoading || !apiConfigured}>
              保存 Zone
            </button>
            <button className="btn" onClick={() => void handleApplyDDNS()} disabled={isLoading || !apiConfigured}>
              自动为探针申请 DDNS
            </button>
            <button className="btn" onClick={() => void loadData()} disabled={isLoading}>
              刷新记录
            </button>
          </div>

          {records.length > 0 ? (
            <div className="probe-table-wrap">
              <table className="probe-table" style={{ minWidth: 880 }}>
                <thead>
                  <tr>
                    <th>探针</th>
                    <th>记录名</th>
                    <th>类型</th>
                    <th>IP</th>
                    <th>更新时间</th>
                  </tr>
                </thead>
                <tbody>
                  {records.map((item) => (
                    <tr key={`cf-record-${item.node_id}-${item.record_id || item.record_name}`}>
                      <td>{item.node_name || item.node_id}</td>
                      <td>{item.record_name || "-"}</td>
                      <td>{item.record_type || "-"}</td>
                      <td>{item.content_ip || "-"}</td>
                      <td>{formatDateTime(item.updated_at)}</td>
                    </tr>
                  ))}
                </tbody>
              </table>
            </div>
          ) : (
            <div className="status">暂无 DDNS 记录。</div>
          )}
        </div>
      ) : (
        <div className="identity-card">
          <label className="probe-direct-toggle" style={{ marginBottom: 8 }}>
            <input
              type="checkbox"
              checked={zeroTrustEnabled}
              onChange={(event) => setZeroTrustEnabled(event.target.checked)}
              disabled={isLoading || !apiConfigured}
            />
            <span>启用自动同步（Bypass 策略）</span>
          </label>

          <div className="row">
            <label>策略名称</label>
            <input
              className="input"
              value={zeroTrustPolicyName}
              onChange={(event) => setZeroTrustPolicyName(event.target.value)}
              placeholder="例如：office-whitelist"
              disabled={isLoading || !apiConfigured}
            />
          </div>

          <div className="row">
            <label>白名单（IP / CIDR / 域名，一行一个）</label>
            <textarea
              className="input"
              value={zeroTrustWhitelistRaw}
              onChange={(event) => setZeroTrustWhitelistRaw(event.target.value)}
              placeholder={`1.1.1.1\n203.0.113.0/24\nexample.com`}
              style={{ minHeight: 160 }}
              disabled={isLoading || !apiConfigured}
            />
          </div>

          <div className="row">
            <label>同步间隔（秒，最小30）</label>
            <input
              className="input"
              type="number"
              min={30}
              value={zeroTrustSyncIntervalSec}
              onChange={(event) => setZeroTrustSyncIntervalSec(event.target.value)}
              disabled={isLoading || !apiConfigured}
            />
          </div>

          <div className="content-actions">
            <button className="btn" onClick={() => void handleSaveZeroTrust()} disabled={isLoading || !apiConfigured}>
              保存 ZeroTrust 配置
            </button>
            <button className="btn" onClick={() => void handleRunZeroTrustNow()} disabled={isLoading || !apiConfigured || zeroTrust.running}>
              立即执行同步
            </button>
            <button className="btn" onClick={() => void loadData()} disabled={isLoading}>
              刷新状态
            </button>
          </div>

          <div className="status-inline" style={{ marginTop: 8 }}>
            运行中：{zeroTrust.running ? "是" : "否"}；最近状态：{zeroTrust.last_status || "-"}
          </div>
          <div className="status-inline">最近执行：{formatDateTime(zeroTrust.last_run_at)}</div>
          <div className="status-inline">最近成功：{formatDateTime(zeroTrust.last_success_at)}</div>
          <div className="status-inline">策略：{zeroTrust.last_policy_name || zeroTrust.policy_name || "-"}</div>
          <div className="status-inline">来源行数：{zeroTrust.last_source_lines || 0}</div>
          <div className="status-inline">最近消息：{zeroTrust.last_message || "-"}</div>
          <div className="status-inline">已应用IP：{zeroTrust.last_applied_ips.length > 0 ? zeroTrust.last_applied_ips.join(", ") : "-"}</div>
        </div>
      )}

      <div className="status">{status}</div>
    </div>
  );
}

function formatDateTime(raw: string): string {
  const value = raw.trim();
  if (!value) {
    return "-";
  }
  const dt = new Date(value);
  if (Number.isNaN(dt.getTime())) {
    return value;
  }
  return dt.toLocaleString();
}
