import { useEffect, useState } from "react";

/** R5-PENDING: Cloudflare 域代理端点尚未实现，返回语义明确的错误 */
function r5PendingError(feature: string): never {
  throw new Error(`[R5-PENDING] ${feature}功能需要主控代理端点，请等待 R5 后端实施完成`);
}

// ── R5 局部类型 ───────────────────────────────────────────────────────────────
type CloudflareAPIKeyState = { api_key?: string; zone_name?: string; configured?: boolean };
type CloudflareDDNSRecord = {
  record_name?: string;
  record_class?: string;
  record_type?: string;
  ip?: string;
  content_ip?: string;
  zone_name?: string;
  node_no?: number;
  node_id?: string;
  node_name?: string;
  record_id?: string;
  updated_at?: string;
  sequence?: number;
};
type CloudflareZeroTrustWhitelistState = {
  enabled: boolean;
  policy_name: string;
  whitelist_raw: string;
  sync_interval_sec: number;
  running?: boolean;
  last_run_at?: string;
  last_success_at?: string;
  last_status?: string;
  last_message?: string;
  last_policy_id?: string;
  last_policy_name?: string;
  last_applied_ips?: string[];
  last_source_lines?: number;
};

function fetchCloudflareAPIKey(_base: string, _token: string): Promise<CloudflareAPIKeyState> { r5PendingError("Cloudflare API Key 读取"); }
function setCloudflareAPIKey(_base: string, _token: string, _apiKey: string): Promise<CloudflareAPIKeyState> { r5PendingError("Cloudflare API Key 设置"); }
function setCloudflareZone(_base: string, _token: string, _zone: string): Promise<string> { r5PendingError("Cloudflare Zone 设置"); }
function fetchCloudflareDDNSRecords(_base: string, _token: string): Promise<CloudflareDDNSRecord[]> { r5PendingError("Cloudflare DDNS 记录查询"); }
function applyCloudflareDDNS(_base: string, _token: string, _zone: string): Promise<{ zone_name?: string; applied?: number; skipped?: number; items?: unknown[]; records?: CloudflareDDNSRecord[] }> { r5PendingError("Cloudflare DDNS 应用"); }
function fetchCloudflareZeroTrustWhitelist(_base: string, _token: string): Promise<CloudflareZeroTrustWhitelistState> { r5PendingError("Cloudflare ZeroTrust 白名单读取"); }
function setCloudflareZeroTrustWhitelist(_base: string, _token: string, _enabled: boolean, _policyName: string, _whitelistRaw: string, _syncIntervalSec: number): Promise<CloudflareZeroTrustWhitelistState> { r5PendingError("Cloudflare ZeroTrust 白名单设置"); }
function runCloudflareZeroTrustWhitelistSync(_base: string, _token: string): Promise<CloudflareZeroTrustWhitelistState> { r5PendingError("Cloudflare ZeroTrust 同步"); }

import { fetchJson } from "../api";

type CloudflareAssistantTabProps = {
  controllerBaseUrl: string;
  sessionToken: string;
};

type CloudflareSubTab = "settings" | "ddns" | "zerotrust" | "ip-speedtest";

// IP 优选结果类型
type IPTestResult = {
  ip: string;
  latency_ms: number;
};

// IP 优选面板组件（调用 Go 后端做真实 TCP 拨号测速）
function IPSpeedTestPanel() {
  const [scanMode, setScanMode] = useState<"fast" | "normal" | "deep">("normal");
  const [timeoutMs, setTimeoutMs] = useState("2000");
  const [isTesting, setIsTesting] = useState(false);
  const [results, setResults] = useState<IPTestResult[]>([]);
  const [status, setStatus] = useState("");
  const [copiedIP, setCopiedIP] = useState("");

  async function handleTest() {
    const modeMap: Record<"fast" | "normal" | "deep", number> = { fast: 50, normal: 120, deep: 300 };
    const count = modeMap[scanMode];
    const timeout = Math.max(500, Math.min(15000, Number.parseInt(timeoutMs, 10) || 2000));

    setIsTesting(true);
    setResults([]);
    setStatus(`正在测速（${scanMode === "fast" ? "快速" : scanMode === "normal" ? "标准" : "深度"}模式，共 ${count} 个 Cloudflare IP），请稍候...`);

    try {
      const resp = await fetchJson<any>('/cloudflare/speedtest', {
        method: 'POST',
        body: JSON.stringify({
          sample_count: count,
          timeout_ms: timeout,
          top_n: 20,
        })
      });
      const items: IPTestResult[] = (resp.results ?? []).map((r: { ip: string; latency_ms: number }) => ({
        ip: r.ip,
        latency_ms: r.latency_ms,
      }));
      setResults(items);
      setStatus(resp.message || `测速完成，有效 IP ${resp.valid_count} 个`);
    } catch (err) {
      const msg = err instanceof Error ? err.message : String(err);
      setStatus(`测速失败：${msg}`);
    } finally {
      setIsTesting(false);
    }
  }

  async function handleCopy(ip: string) {
    try {
      await navigator.clipboard.writeText(ip);
      setCopiedIP(ip);
      setTimeout(() => setCopiedIP(""), 1800);
    } catch {
      setCopiedIP("");
    }
  }

  // 延迟色阶
  function latencyColor(ms: number): string {
    if (ms <= 80)  return "#52e892";
    if (ms <= 180) return "#a8e852";
    if (ms <= 350) return "#f0c04a";
    return "#f07a4a";
  }

  return (
    <div style={{ display: "grid", gap: 12 }}>
      {/* 说明 */}
      <div className="status-inline" style={{ fontSize: 13, color: "#8badd4" }}>
        通过 Go 后端直接向 Cloudflare IP 的 443 端口发起 TCP 连接，测量真实握手延迟，结果比浏览器 fetch 更准确。
      </div>

      {/* 配置区 */}
      <div className="identity-card">
        <div className="row">
          <label>扫描规模</label>
          <div style={{ display: "flex", gap: 8 }}>
            {(["fast", "normal", "deep"] as const).map((mode) => (
              <button
                key={mode}
                onClick={() => setScanMode(mode)}
                disabled={isTesting}
                style={{
                  flex: 1,
                  height: 34,
                  borderRadius: 6,
                  border: `1px solid ${scanMode === mode ? "rgba(129,184,255,0.72)" : "rgba(255,255,255,0.2)"}`,
                  background: scanMode === mode ? "rgba(65,108,166,0.72)" : "rgba(22,30,44,0.9)",
                  color: "#fff",
                  cursor: isTesting ? "not-allowed" : "pointer",
                  fontSize: 13,
                  opacity: isTesting ? 0.6 : 1,
                }}
              >
                {mode === "fast" ? "⚡ 快速" : mode === "normal" ? "🔍 标准" : "🔬 深度"}
              </button>
            ))}
          </div>
        </div>
        <div className="status-inline" style={{ fontSize: 12, color: "#6a8fc8", marginTop: -4 }}>
          {scanMode === "fast" ? "测试约 50 个 IP，速度快（约 2s）" : scanMode === "normal" ? "测试约 120 个 IP，覆盖面广（约 3s）" : "测试约 300 个 IP，结果更全面（约 5s）"}
        </div>
        <div className="row">
          <label>超时时间 (ms)</label>
          <input
            className="input"
            type="number"
            min={500}
            max={15000}
            step={500}
            value={timeoutMs}
            onChange={(e) => setTimeoutMs(e.target.value)}
            disabled={isTesting}
          />
        </div>

        <div className="content-actions">
          <button className="btn" onClick={() => void handleTest()} disabled={isTesting}>
            {isTesting ? "⏳ 测速中..." : "🚀 开始测速"}
          </button>
        </div>

        {/* 进度条（不定进度，仅测试时展示） */}
        {isTesting && (
          <div className="progress-bar" style={{ marginTop: 4 }}>
            <div
              className="progress-bar-fill"
              style={{
                width: "100%",
                animation: "cf-indeterminate 1.4s ease-in-out infinite",
              }}
            />
          </div>
        )}
      </div>

      {/* 结果区 */}
      {results.length > 0 && (
        <div className="identity-card" style={{ padding: 0, overflow: "hidden" }}>
          <div style={{ padding: "10px 14px 6px", color: "#a8c8ff", fontSize: 13, fontWeight: 600 }}>
            🏆 延迟最低前 {results.length} 个 IP（TCP 443 实测）— 点击一键复制
          </div>
          <div style={{ display: "grid", gridTemplateColumns: "repeat(auto-fill, minmax(220px, 1fr))", gap: 0 }}>
            {results.map((r, idx) => (
              <button
                key={r.ip}
                onClick={() => void handleCopy(r.ip)}
                title={`点击复制 ${r.ip}`}
                style={{
                  display: "flex",
                  alignItems: "center",
                  gap: 8,
                  padding: "9px 14px",
                  border: "none",
                  borderBottom: "1px solid rgba(255,255,255,0.07)",
                  borderRight: "1px solid rgba(255,255,255,0.07)",
                  background: copiedIP === r.ip ? "rgba(80,200,130,0.18)" : "transparent",
                  color: copiedIP === r.ip ? "#52e892" : "#e4f0ff",
                  cursor: "pointer",
                  textAlign: "left",
                  transition: "background 0.18s",
                  fontSize: 13,
                }}
              >
                <span style={{ minWidth: 22, color: "#6a8fc8", fontSize: 12, fontWeight: 700 }}>
                  #{idx + 1}
                </span>
                <span style={{ flex: 1, fontFamily: "monospace", letterSpacing: "0.02em" }}>
                  {r.ip}
                </span>
                <span style={{
                  fontSize: 12,
                  fontWeight: 700,
                  color: latencyColor(r.latency_ms),
                  minWidth: 56,
                  textAlign: "right",
                }}>
                  {r.latency_ms} ms
                </span>
                {copiedIP === r.ip && (
                  <span style={{ fontSize: 11, color: "#52e892", marginLeft: 2 }}>✓ 已复制</span>
                )}
              </button>
            ))}
          </div>
        </div>
      )}

      {status && <div className="status">{status}</div>}
    </div>
  );
}

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
      setRecords(result.records ?? []);
      setStatus(`DDNS 已执行，当前有效记录 ${(result.records ?? []).length} 条`);
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
      const result = await setCloudflareZeroTrustWhitelist(
        props.controllerBaseUrl,
        props.sessionToken,
        zeroTrustEnabled,
        zeroTrustPolicyName.trim(),
        zeroTrustWhitelistRaw,
        interval,
      );
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
      const result = await runCloudflareZeroTrustWhitelistSync(props.controllerBaseUrl, props.sessionToken);
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
        <button className={`subtab-btn ${subTab === "ip-speedtest" ? "active" : ""}`} onClick={() => setSubTab("ip-speedtest")}>
          🚀 IP 优选
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
                      <td>{formatDateTime(item.updated_at || "")}</td>
                    </tr>
                  ))}
                </tbody>
              </table>
            </div>
          ) : (
            <div className="status">暂无 DDNS 记录。</div>
          )}
        </div>
      ) : subTab === "ip-speedtest" ? (
        <IPSpeedTestPanel />
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
          <div className="status-inline">最近执行：{formatDateTime(zeroTrust.last_run_at || "")}</div>
          <div className="status-inline">最近成功：{formatDateTime(zeroTrust.last_success_at || "")}</div>
          <div className="status-inline">策略：{zeroTrust.last_policy_name || zeroTrust.policy_name || "-"}</div>
          <div className="status-inline">来源行数：{zeroTrust.last_source_lines || 0}</div>
          <div className="status-inline">最近消息：{zeroTrust.last_message || "-"}</div>
          <div className="status-inline">已应用IP：{(zeroTrust.last_applied_ips ?? []).length > 0 ? (zeroTrust.last_applied_ips ?? []).join(", ") : "-"}</div>
        </div>
      )}

      {subTab !== "ip-speedtest" && <div className="status">{status}</div>}
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
