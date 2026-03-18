import { useEffect, useState } from "react";
import {
  applyCloudflareDDNS,
  fetchCloudflareAPIKey,
  fetchCloudflareDDNSRecords,
  setCloudflareAPIKey,
  setCloudflareZone,
} from "../services/controller-api";
import type { CloudflareDDNSApplyItem, CloudflareDDNSRecord } from "../types";

type CloudflareAssistantTabProps = {
  controllerBaseUrl: string;
  sessionToken: string;
};

type CloudflareSubTab = "settings" | "ddns";

export function CloudflareAssistantTab(props: CloudflareAssistantTabProps) {
  const [subTab, setSubTab] = useState<CloudflareSubTab>("settings");
  const [apiKeyInput, setAPIKeyInput] = useState("");
  const [apiConfigured, setAPIConfigured] = useState(false);
  const [zoneNameInput, setZoneNameInput] = useState("");
  const [records, setRecords] = useState<CloudflareDDNSRecord[]>([]);
  const [applyItems, setApplyItems] = useState<CloudflareDDNSApplyItem[]>([]);
  const [status, setStatus] = useState("正在加载 Cloudflare 配置...");
  const [isLoading, setIsLoading] = useState(false);

  useEffect(() => {
    void loadData();
  }, [props.controllerBaseUrl, props.sessionToken]);

  async function loadData() {
    setIsLoading(true);
    setStatus("正在加载 Cloudflare 配置...");
    try {
      const [api, ddnsRecords] = await Promise.all([
        fetchCloudflareAPIKey(props.controllerBaseUrl, props.sessionToken),
        fetchCloudflareDDNSRecords(props.controllerBaseUrl, props.sessionToken),
      ]);
      const fallbackZone = (ddnsRecords[0]?.zone_name || "").trim().toLowerCase();
      const zone = (api.zone_name || "").trim().toLowerCase() || fallbackZone;
      setAPIKeyInput(api.api_key || "");
      setAPIConfigured(api.configured === true);
      setZoneNameInput(zone);
      setRecords(ddnsRecords);
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
      setApplyItems(result.items);
      setRecords(result.records);
      setStatus(`DDNS 已执行：成功 ${result.applied}，跳过 ${result.skipped}`);
    } catch (error) {
      const msg = error instanceof Error ? error.message : "unknown error";
      setStatus(`自动申请 DDNS 失败：${msg}`);
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
      ) : (
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

          {applyItems.length > 0 ? (
            <div className="probe-table-wrap">
              <table className="probe-table" style={{ minWidth: 880 }}>
                <thead>
                  <tr>
                    <th>探针</th>
                    <th>域名</th>
                    <th>IP</th>
                    <th>状态</th>
                    <th>结果</th>
                  </tr>
                </thead>
                <tbody>
                  {applyItems.map((item) => (
                    <tr key={`cf-apply-${item.node_id}-${item.record_name}`}>
                      <td>{item.node_name || item.node_id}</td>
                      <td>{item.record_name || "-"}</td>
                      <td>{item.content_ip || "-"}</td>
                      <td>{item.status}</td>
                      <td>{item.message || "-"}</td>
                    </tr>
                  ))}
                </tbody>
              </table>
            </div>
          ) : null}

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
