import { useEffect, useRef, useState } from "react";
import {
  fetchProbeNodeStatus,
  fetchProbeNodes,
  fetchProbeReportIntervalSettings,
  setProbeReportInterval,
  syncProbeNodes,
  upgradeAllProbeNodes,
  upgradeProbeNode,
  type ProbeNodeStatusItem,
  type ProbeNodeSyncItem,
  type ProbeReportIntervalSettings,
} from "../services/controller-api";

type ProbeManageTabProps = {
  controllerBaseUrl: string;
  sessionToken: string;
};

type ProbeSubTab = "create" | "list" | "status";
type ProbeTargetSystem = "linux" | "windows";

type ProbeNodeItem = {
  node_no: number;
  node_name: string;
  remark?: string;
  node_secret: string;
  target_system: ProbeTargetSystem;
  direct_connect: boolean;
  payment_cycle?: string;
  cost?: string;
  expire_at?: string;
  vendor_name?: string;
  vendor_url?: string;
  created_at: string;
  updated_at: string;
  runtime?: {
    node_id?: string;
    online?: boolean;
    last_seen?: string;
    version?: string;
    ipv4?: string[];
    ipv6?: string[];
    system?: {
      cpu_percent?: number;
      memory_used_percent?: number;
      swap_used_percent?: number;
      disk_used_percent?: number;
    };
  };
};

type ProbeNodeSettingsDraft = {
  node_no: number;
  node_name: string;
  remark: string;
  target_system: ProbeTargetSystem;
  direct_connect: boolean;
  payment_cycle: string;
  cost: string;
  expire_at: string;
  vendor_name: string;
  vendor_url: string;
};

export function ProbeManageTab(props: ProbeManageTabProps) {
  const [subTab, setSubTab] = useState<ProbeSubTab>("create");
  const [nodeNameInput, setNodeNameInput] = useState("");
  const [controllerAddress, setControllerAddress] = useState(props.controllerBaseUrl || "");
  const [nodes, setNodes] = useState<ProbeNodeItem[]>([]);
  const [nodeStatusItems, setNodeStatusItems] = useState<ProbeNodeStatusItem[]>([]);
  const [reportIntervalInput, setReportIntervalInput] = useState("60");
  const [reportIntervalSettings, setReportIntervalSettings] = useState<ProbeReportIntervalSettings | null>(null);
  const pollIndexRef = useRef(0);
  const [isLoading, setIsLoading] = useState(false);
  const [isUpgradingAll, setIsUpgradingAll] = useState(false);
  const [upgradingNodeNos, setUpgradingNodeNos] = useState<number[]>([]);
  const [status, setStatus] = useState("正在加载探针列表...");
  const [settingsDraft, setSettingsDraft] = useState<ProbeNodeSettingsDraft | null>(null);

  useEffect(() => {
    if (!controllerAddress.trim() && props.controllerBaseUrl.trim()) {
      setControllerAddress(props.controllerBaseUrl.trim());
    }
  }, [controllerAddress, props.controllerBaseUrl]);

  useEffect(() => {
    void loadNodes();
  }, []);

  async function loadNodes() {
    setIsLoading(true);
    try {
      const remoteNodes = await fetchProbeNodesFromController(controllerAddress, props.sessionToken);
      let mergedNodes = sortNodes(remoteNodes as ProbeNodeItem[]);
      try {
        const items = await fetchProbeStatusFromController(controllerAddress, props.sessionToken);
        const sortedItems = sortStatusItems(items);
        setNodeStatusItems(sortedItems);
        mergedNodes = mergeNodesWithStatus(remoteNodes as ProbeNodeItem[], sortedItems);
      } catch {
        // ignore status refresh failure and keep list available
      }
      await replaceLocalProbeNodes(remoteNodes);
      setNodes(mergedNodes);
      setStatus(remoteNodes.length ? "已从主控同步探针列表" : "主控暂无探针，请先创建");
    } catch (error) {
      try {
        const localNodes = await getProbeNodes();
        setNodes(sortNodes(localNodes));
        setStatus("主控同步失败，已加载本地探针列表");
      } catch (fallbackErr) {
        const msg = fallbackErr instanceof Error ? fallbackErr.message : "unknown error";
        setStatus(`加载探针列表失败：${msg}`);
      }
    } finally {
      setIsLoading(false);
    }
  }

  async function loadNodeStatus() {
    setIsLoading(true);
    try {
      const [items, settings] = await Promise.all([
        fetchProbeStatusFromController(controllerAddress, props.sessionToken),
        fetchProbeReportIntervalFromController(controllerAddress, props.sessionToken),
      ]);
      const sortedItems = sortStatusItems(items);
      setNodeStatusItems(sortedItems);
      setNodes((prev) => mergeNodesWithStatus(prev, sortedItems));
      setReportIntervalSettings(settings);
      setReportIntervalInput(String(settings.current_sec || settings.default_sec || 60));
      setStatus(items.length ? "已从主控同步探针状态" : "暂无探针状态数据");
    } catch (error) {
      const msg = error instanceof Error ? error.message : "unknown error";
      setStatus(`加载探针状态失败：${msg}`);
    } finally {
      setIsLoading(false);
    }
  }

  async function applyReportInterval() {
    const sec = Number.parseInt(reportIntervalInput.trim(), 10);
    if (!Number.isFinite(sec) || sec <= 0) {
      setStatus("上送周期必须是正整数（秒）");
      return;
    }

    setIsLoading(true);
    try {
      const settings = await setProbeReportIntervalOnController(controllerAddress, props.sessionToken, sec);
      setReportIntervalSettings(settings);
      setReportIntervalInput(String(settings.current_sec || sec));
      setStatus(`已设置探针上送周期为 ${settings.current_sec || sec}s（5分钟后或管理端断开后回退默认值）`);
    } catch (error) {
      const msg = error instanceof Error ? error.message : "unknown error";
      setStatus(`设置上送周期失败：${msg}`);
    } finally {
      setIsLoading(false);
    }
  }

  async function pollNodeStatusByNodeID(nodeNo: number) {
    try {
      const items = await fetchProbeStatusFromController(controllerAddress, props.sessionToken, nodeNo);
      if (!items.length) {
        return;
      }
      setNodeStatusItems((prev) => mergeStatusItems(prev, items));
      setNodes((prev) => mergeNodesWithStatus(prev, items));
    } catch {
      // ignore intermittent poll failure
    }
  }

  useEffect(() => {
    if (subTab !== "status") {
      return;
    }
    const nodeNos = nodes.map((item) => item.node_no).filter((v) => v > 0);
    if (!nodeNos.length) {
      return;
    }

    const timer = window.setInterval(() => {
      const idx = pollIndexRef.current % nodeNos.length;
      pollIndexRef.current += 1;
      void pollNodeStatusByNodeID(nodeNos[idx]);
    }, 2000);

    return () => window.clearInterval(timer);
  }, [subTab, nodes, controllerAddress, props.sessionToken]);

  async function createNode() {
    const cleanName = nodeNameInput.trim();
    if (!cleanName) {
      setStatus("请输入节点名称");
      return;
    }

    setIsLoading(true);
    try {
      await syncFromControllerToLocal(controllerAddress, props.sessionToken);
      const created = await createProbeNode(cleanName);
      const refreshed = await getProbeNodes();
      const synced = await syncProbeNodesToController(controllerAddress, props.sessionToken, refreshed);
      await replaceLocalProbeNodes(synced);
      setNodes(sortNodes(synced));
      await loadNodeStatus();
      setNodeNameInput("");
      setSubTab("list");
      setStatus(`节点已创建并全量同步到主控：${created.node_name}（节点号 ${created.node_no}）`);
    } catch (error) {
      const msg = error instanceof Error ? error.message : "unknown error";
      setStatus(`创建节点失败：${msg}`);
    } finally {
      setIsLoading(false);
    }
  }

  async function updateNode(
    nodeNo: number,
    patch: Partial<Pick<ProbeNodeItem, "node_name" | "remark" | "target_system" | "direct_connect" | "payment_cycle" | "cost" | "expire_at" | "vendor_name" | "vendor_url">>,
  ): Promise<boolean> {
    const current = nodes.find((item) => item.node_no === nodeNo);
    if (!current) {
      return false;
    }

    const nextNodeName = (patch.node_name ?? current.node_name).trim();
    if (!nextNodeName) {
      setStatus("节点名称不能为空");
      return false;
    }

    const nextRemark = (patch.remark ?? current.remark ?? "").trim();
    const nextTargetSystem = patch.target_system ?? current.target_system;
    const nextDirectConnect = patch.direct_connect ?? current.direct_connect;
    const nextPaymentCycle = (patch.payment_cycle ?? current.payment_cycle ?? "").trim();
    const nextCost = (patch.cost ?? current.cost ?? "").trim();
    const nextExpireAt = (patch.expire_at ?? current.expire_at ?? "").trim();
    const nextVendorName = (patch.vendor_name ?? current.vendor_name ?? "").trim();
    const nextVendorURL = (patch.vendor_url ?? current.vendor_url ?? "").trim();

    setIsLoading(true);
    try {
      await syncFromControllerToLocal(controllerAddress, props.sessionToken);
      const updated = await updateProbeNodeSettings({
        node_no: nodeNo,
        node_name: nextNodeName,
        remark: nextRemark,
        target_system: nextTargetSystem,
        direct_connect: nextDirectConnect,
        payment_cycle: nextPaymentCycle,
        cost: nextCost,
        expire_at: nextExpireAt,
        vendor_name: nextVendorName,
        vendor_url: nextVendorURL,
      });
      const refreshed = await getProbeNodes();
      const synced = await syncProbeNodesToController(controllerAddress, props.sessionToken, refreshed);
      await replaceLocalProbeNodes(synced);
      setNodes(sortNodes(synced));
      await loadNodeStatus();
      setStatus(`节点已更新并同步到主控：${updated.node_name}`);
      return true;
    } catch (error) {
      const msg = error instanceof Error ? error.message : "unknown error";
      setStatus(`更新节点失败：${msg}`);
      return false;
    } finally {
      setIsLoading(false);
    }
  }

  function openSettings(node: ProbeNodeItem) {
    setSettingsDraft({
      node_no: node.node_no,
      node_name: node.node_name,
      remark: node.remark || "",
      target_system: node.target_system,
      direct_connect: node.direct_connect,
      payment_cycle: node.payment_cycle || "",
      cost: node.cost || "",
      expire_at: node.expire_at || "",
      vendor_name: node.vendor_name || "",
      vendor_url: node.vendor_url || "",
    });
  }

  function closeSettings() {
    setSettingsDraft(null);
  }

  async function saveSettings() {
    if (!settingsDraft) {
      return;
    }
    const ok = await updateNode(settingsDraft.node_no, settingsDraft);
    if (ok) {
      setSettingsDraft(null);
    }
  }

  async function copyInstallCommand(node: ProbeNodeItem) {
    const command = buildInstallCommand(node, controllerAddress);
    try {
      await copyText(command);
      setStatus(`已复制安装命令：${node.node_name}（${node.target_system} / ${node.direct_connect ? "直连" : "主控转发"}）`);
    } catch (error) {
      const msg = error instanceof Error ? error.message : "unknown error";
      setStatus(`复制安装命令失败：${msg}`);
    }
  }

  async function upgradeOne(node: ProbeNodeItem) {
    const base = sanitizeControllerAddress(controllerAddress);
    const token = props.sessionToken.trim();
    if (!token) {
      setStatus("未登录，无法下发升级命令");
      return;
    }

    setUpgradingNodeNos((prev) => [...prev, node.node_no]);
    try {
      await upgradeProbeNode(base, token, node.node_no);
      setStatus(`已下发升级命令：${node.node_name}（${node.direct_connect ? "直连升级" : "主控代理升级"}）`);
    } catch (error) {
      const msg = error instanceof Error ? error.message : "unknown error";
      setStatus(`下发升级失败：${node.node_name}，${msg}`);
    } finally {
      setUpgradingNodeNos((prev) => prev.filter((v) => v !== node.node_no));
    }
  }

  async function upgradeAll() {
    const base = sanitizeControllerAddress(controllerAddress);
    const token = props.sessionToken.trim();
    if (!token) {
      setStatus("未登录，无法下发升级命令");
      return;
    }

    setIsUpgradingAll(true);
    try {
      const result = await upgradeAllProbeNodes(base, token);
      if (result.failures.length > 0) {
        setStatus(`一键升级已下发：成功 ${result.success}/${result.total}，失败 ${result.failures.length}`);
      } else {
        setStatus(`一键升级已下发：成功 ${result.success}/${result.total}`);
      }
    } catch (error) {
      const msg = error instanceof Error ? error.message : "unknown error";
      setStatus(`一键升级下发失败：${msg}`);
    } finally {
      setIsUpgradingAll(false);
    }
  }

  return (
    <div className="content-block">
      <h2>探针管理</h2>

      <div className="subtab-list">
        <button className={`subtab-btn ${subTab === "create" ? "active" : ""}`} onClick={() => setSubTab("create")}>新建探针</button>
        <button className={`subtab-btn ${subTab === "list" ? "active" : ""}`} onClick={() => setSubTab("list")}>探针列表</button>
        <button className={`subtab-btn ${subTab === "status" ? "active" : ""}`} onClick={() => { setSubTab("status"); void loadNodeStatus(); }}>探针状态</button>
      </div>

      {subTab === "create" ? (
        <div className="identity-card" style={{ marginTop: 12 }}>
          <div>节点名称</div>
          <input
            className="input"
            value={nodeNameInput}
            placeholder="例如：华东-生产-01"
            onChange={(event) => setNodeNameInput(event.target.value)}
            maxLength={64}
            disabled={isLoading}
          />
          <div className="content-actions">
            <button className="btn" onClick={() => void createNode()} disabled={isLoading}>新建节点</button>
          </div>
          <div>创建后会自动生成数字节点号与节点 Secret（保存到管理端 data/probe_nodes.json）。</div>
        </div>
      ) : subTab === "list" ? (
        <div style={{ marginTop: 12 }}>
          <div className="identity-card" style={{ marginBottom: 12 }}>
            <div>主控地址（用于“非直连”安装命令）</div>
            <input
              className="input"
              value={controllerAddress}
              placeholder="例如：https://controller.example.com"
              onChange={(event) => setControllerAddress(event.target.value)}
              disabled={isLoading}
            />
            <div className="content-actions">
              <button className="btn" onClick={() => void loadNodes()} disabled={isLoading}>刷新列表</button>
              <button className="btn" onClick={() => void upgradeAll()} disabled={isLoading || isUpgradingAll || nodes.length === 0}>一键升级（全部探针）</button>
            </div>
            <div>升级命令通过主控下发；直连节点直连 GitHub，非直连节点走主控代理升级。</div>
          </div>

          {nodes.length === 0 ? (
            <div className="status">暂无探针，请先在“新建探针”中创建节点。</div>
          ) : (
            <div className="probe-node-list">
              {nodes.map((node) => (
                <div className="probe-node-card" key={node.node_no}>
                  <div className="probe-node-title">{node.node_name}</div>
                  <div className="probe-node-meta single-line">节点号：{node.node_no}　版本：{node.runtime?.version || "-"}　厂家：
                    {node.vendor_name ? (
                      <button className="vendor-copy-link" type="button" title={node.vendor_url || "点击复制厂家URL"} onClick={() => void copyVendorURL(node, setStatus)}>
                        {node.vendor_name}
                      </button>
                    ) : "-"}　付款周期：{node.payment_cycle || "-"}　费用：{node.cost || "-"}　到期：{formatTime(node.expire_at || "")}</div>
                  {node.remark ? <div className="probe-node-meta compact">备注：{node.remark}</div> : null}

                  <div className="probe-node-controls-row">
                   <div className="content-actions inline">
                     <button className="btn" onClick={() => openSettings(node)} disabled={isLoading}>设置</button>
                     <button className="btn" onClick={() => void copyInstallCommand(node)} disabled={isLoading}>安装</button>
                     <button className="btn" onClick={() => void upgradeOne(node)} disabled={isLoading || isUpgradingAll || upgradingNodeNos.includes(node.node_no)}>
                       {upgradingNodeNos.includes(node.node_no) ? "下发中..." : "升级"}
                    </button>
                  </div>
                  </div>
                </div>
              ))}
            </div>
          )}
        </div>
      ) : (
        <div style={{ marginTop: 12 }}>
          <div className="identity-card" style={{ marginBottom: 12 }}>
            <div>探针实时状态（来自主控汇总）</div>
            <div className="row" style={{ marginBottom: 0 }}>
              <label>上送周期(秒)</label>
              <input className="input" value={reportIntervalInput} onChange={(e) => setReportIntervalInput(e.target.value)} disabled={isLoading} />
            </div>
            <div className="content-actions">
              <button className="btn" onClick={() => void applyReportInterval()} disabled={isLoading}>设置周期</button>
              <button className="btn" onClick={() => void loadNodeStatus()} disabled={isLoading}>刷新状态</button>
            </div>
            <div>
              默认：{reportIntervalSettings?.default_sec ?? 60}s，当前：{reportIntervalSettings?.current_sec ?? 60}s，
              过期：{formatTime(reportIntervalSettings?.override_expires_at || "")}
            </div>
          </div>

          {nodeStatusItems.length === 0 ? (
            <div className="status">暂无探针，请先在“新建探针”中创建节点。</div>
          ) : (
            <div className="probe-node-list">
              {nodeStatusItems.map((item) => (
                <div className="probe-node-card" key={`status-${item.node_no}`}>
                  <div className="probe-node-title">{item.node_name}</div>
                  <div className="probe-node-meta compact">节点号：{item.node_no > 0 ? item.node_no : (item.runtime?.node_id || "-")}　|　状态：{item.runtime?.online ? "在线" : "离线"}　|　版本：{item.runtime?.version || "-"}　|　最后上报：{formatTime(item.runtime?.last_seen || "")}</div>
                  <div className="probe-node-meta compact">CPU：{item.runtime?.online ? formatPercent(item.runtime?.system?.cpu_percent) : "-"}　RAM：{item.runtime?.online ? formatPercent(item.runtime?.system?.memory_used_percent) : "-"}　SWAP：{item.runtime?.online ? formatPercent(item.runtime?.system?.swap_used_percent) : "-"}　硬盘：{item.runtime?.online ? formatPercent(item.runtime?.system?.disk_used_percent) : "-"}</div>
                  <div className="probe-node-meta compact">
                    IP：
                    {collectIPs(item).length === 0 ? "-" : collectIPs(item).map((ip) => (
                      <button
                        key={`${item.node_no}-${ip}`}
                        className="ip-copy-chip"
                        onClick={() => void copyStatusIP(ip, setStatus)}
                        title="点击复制IP"
                        type="button"
                      >
                        {ip}
                      </button>
                    ))}
                  </div>
                </div>
              ))}
            </div>
          )}
        </div>
      )}

      {settingsDraft ? (
        <div className="probe-settings-modal-mask" onClick={closeSettings}>
          <div className="probe-settings-modal" onClick={(event) => event.stopPropagation()}>
            <h3 style={{ marginTop: 0 }}>探针设置 - 节点 {settingsDraft.node_no}</h3>
            <div className="row">
              <label>名称</label>
              <input className="input" value={settingsDraft.node_name} onChange={(event) => setSettingsDraft((prev) => prev ? { ...prev, node_name: event.target.value } : prev)} disabled={isLoading} />
            </div>
            <div className="row">
              <label>备注</label>
              <input className="input" value={settingsDraft.remark} onChange={(event) => setSettingsDraft((prev) => prev ? { ...prev, remark: event.target.value } : prev)} disabled={isLoading} />
            </div>
            <div className="row">
              <label>操作系统</label>
              <select className="input" value={settingsDraft.target_system} onChange={(event) => setSettingsDraft((prev) => prev ? { ...prev, target_system: event.target.value as ProbeTargetSystem } : prev)} disabled={isLoading}>
                <option value="linux">Linux</option>
                <option value="windows">Windows</option>
              </select>
            </div>
            <div className="row">
              <label>安装方式</label>
              <label className="probe-direct-toggle" style={{ marginTop: 0 }}>
                <input type="checkbox" checked={settingsDraft.direct_connect} onChange={(event) => setSettingsDraft((prev) => prev ? { ...prev, direct_connect: event.target.checked } : prev)} disabled={isLoading} />
                {settingsDraft.direct_connect ? "直连" : "主控代理"}
              </label>
            </div>
            <div className="row">
              <label>付款周期</label>
              <input className="input" value={settingsDraft.payment_cycle} onChange={(event) => setSettingsDraft((prev) => prev ? { ...prev, payment_cycle: event.target.value } : prev)} disabled={isLoading} />
            </div>
            <div className="row">
              <label>费用</label>
              <input className="input" value={settingsDraft.cost} onChange={(event) => setSettingsDraft((prev) => prev ? { ...prev, cost: event.target.value } : prev)} disabled={isLoading} />
            </div>
            <div className="row">
              <label>到期时间</label>
              <input
                className="input"
                type="datetime-local"
                value={toDateTimeLocalInputValue(settingsDraft.expire_at)}
                onChange={(event) => setSettingsDraft((prev) => prev ? { ...prev, expire_at: fromDateTimeLocalInputValue(event.target.value) } : prev)}
                disabled={isLoading}
              />
            </div>
            <div className="row">
              <label>厂家</label>
              <input className="input" value={settingsDraft.vendor_name} onChange={(event) => setSettingsDraft((prev) => prev ? { ...prev, vendor_name: event.target.value } : prev)} disabled={isLoading} />
            </div>
            <div className="row">
              <label>厂家URL</label>
              <input className="input" value={settingsDraft.vendor_url} onChange={(event) => setSettingsDraft((prev) => prev ? { ...prev, vendor_url: event.target.value } : prev)} disabled={isLoading} />
            </div>
            <div className="content-actions">
              <button className="btn" onClick={() => void saveSettings()} disabled={isLoading}>保存</button>
              <button className="btn" onClick={closeSettings} disabled={isLoading}>取消</button>
            </div>
          </div>
        </div>
      ) : null}

      <div className="status">{status}</div>
    </div>
  );
}

function sortNodes(nodes: ProbeNodeItem[]): ProbeNodeItem[] {
  return [...nodes].sort((a, b) => a.node_no - b.node_no);
}

function sortStatusItems(items: ProbeNodeStatusItem[]): ProbeNodeStatusItem[] {
  return [...items].sort((a, b) => a.node_no - b.node_no);
}

function mergeStatusItems(current: ProbeNodeStatusItem[], incoming: ProbeNodeStatusItem[]): ProbeNodeStatusItem[] {
  const map = new Map<number, ProbeNodeStatusItem>();
  for (const item of current) {
    map.set(item.node_no, item);
  }
  for (const item of incoming) {
    map.set(item.node_no, item);
  }
  return sortStatusItems(Array.from(map.values()));
}

function mergeNodesWithStatus(nodes: ProbeNodeItem[], statusItems: ProbeNodeStatusItem[]): ProbeNodeItem[] {
  const runtimeByNodeNo = new Map<number, ProbeNodeStatusItem["runtime"]>();
  for (const item of statusItems) {
    runtimeByNodeNo.set(item.node_no, item.runtime || {});
  }

  return sortNodes(nodes.map((node) => {
    const runtime = runtimeByNodeNo.get(node.node_no);
    if (!runtime) {
      return node;
    }
    return {
      ...node,
      runtime: {
        ...(node.runtime || {}),
        ...runtime,
      },
    };
  }));
}

function collectIPs(item: ProbeNodeStatusItem): string[] {
  const v4 = Array.isArray(item.runtime?.ipv4) ? item.runtime?.ipv4 ?? [] : [];
  const v6 = Array.isArray(item.runtime?.ipv6) ? item.runtime?.ipv6 ?? [] : [];
  const merged = [...v4, ...v6].map((v) => String(v).trim()).filter((v) => v !== "");
  return Array.from(new Set(merged));
}

async function copyStatusIP(ip: string, setStatus: (value: string) => void): Promise<void> {
  try {
    await copyText(ip);
    setStatus(`已复制IP：${ip}`);
  } catch (error) {
    const msg = error instanceof Error ? error.message : "unknown error";
    setStatus(`复制IP失败：${msg}`);
  }
}

async function copyVendorURL(node: ProbeNodeItem, setStatus: (value: string) => void): Promise<void> {
  const vendorURL = (node.vendor_url || "").trim();
  if (!vendorURL) {
    setStatus(`节点 ${node.node_name} 未设置厂家URL`);
    return;
  }
  try {
    await copyText(vendorURL);
    setStatus(`已复制厂家URL：${vendorURL}`);
  } catch (error) {
    const msg = error instanceof Error ? error.message : "unknown error";
    setStatus(`复制厂家URL失败：${msg}`);
  }
}

function formatTime(isoTime: string): string {
  const dt = new Date(isoTime);
  if (Number.isNaN(dt.getTime())) {
    return "-";
  }
  return dt.toLocaleString();
}

function formatPercent(value: number | undefined): string {
  if (typeof value !== "number" || Number.isNaN(value)) {
    return "-";
  }
  return `${value.toFixed(1)}%`;
}

function toDateTimeLocalInputValue(value: string): string {
  const raw = (value || "").trim();
  if (!raw) {
    return "";
  }
  const dt = new Date(raw);
  if (Number.isNaN(dt.getTime())) {
    return "";
  }
  const y = dt.getFullYear();
  const m = String(dt.getMonth() + 1).padStart(2, "0");
  const d = String(dt.getDate()).padStart(2, "0");
  const hh = String(dt.getHours()).padStart(2, "0");
  const mm = String(dt.getMinutes()).padStart(2, "0");
  return `${y}-${m}-${d}T${hh}:${mm}`;
}

function fromDateTimeLocalInputValue(value: string): string {
  const raw = value.trim();
  if (!raw) {
    return "";
  }
  const dt = new Date(raw);
  if (Number.isNaN(dt.getTime())) {
    return raw;
  }
  return dt.toISOString();
}

function sanitizeControllerAddress(rawAddress: string): string {
  const value = rawAddress.trim().replace(/\/+$/, "");
  if (!value) {
    return "http://127.0.0.1:15030";
  }
  return value;
}

function buildInstallCommand(node: ProbeNodeItem, controllerAddress: string): string {
  const base = sanitizeControllerAddress(controllerAddress);
  const envArgs = "PROBE_NODE_ID='" + String(node.node_no) + "' PROBE_NODE_SECRET='" + node.node_secret + "' PROBE_CONTROLLER_URL='" + base + "'";
  const params = new URLSearchParams({
    node_id: String(node.node_no),
    secret: node.node_secret,
  });

  if (node.target_system === "windows") {
    return [
      "$repo = \"fengzhanhuaer/CloudHelper\"",
      "$nodeId = \"" + String(node.node_no) + "\"",
      "$secret = \"" + node.node_secret + "\"",
      "$controller = \"" + base + "\"",
      "$dir = \"C:\\\\cloudhelper\\\\probe_node\"",
      "New-Item -ItemType Directory -Force -Path $dir | Out-Null",
      "$url = \"https://github.com/$repo/releases/latest/download/cloudhelper-probe-node-windows-amd64.exe\"",
      "Invoke-WebRequest -Uri $url -OutFile \"$dir\\\\probe_node.exe\"",
      "Write-Host \"Downloaded probe_node.exe to $dir (nodeId=$nodeId, secret=$secret, controller=$controller)\"",
    ].join("; ");
  }

  if (!node.direct_connect) {
    return buildLinuxInstallCommand(base + "/api/probe/proxy/probe-node/install-script?" + params.toString(), envArgs);
  }

  return buildLinuxInstallCommand("https://raw.githubusercontent.com/fengzhanhuaer/CloudHelper/main/scripts/install_probe_node_service.sh", envArgs);
}

function buildLinuxInstallCommand(scriptURL: string, envArgs: string): string {
  return "curl -fsSL '" + scriptURL + "' | env " + envArgs + " bash";
}

async function getProbeNodes(): Promise<ProbeNodeItem[]> {
  const api = getWailsAppApi();
  return (await api.GetProbeNodes()) as ProbeNodeItem[];
}

async function replaceLocalProbeNodes(nodes: ProbeNodeItem[]): Promise<ProbeNodeItem[]> {
  const api = getWailsAppApi();
  if (!api.ReplaceProbeNodes) {
    return nodes;
  }
  return (await api.ReplaceProbeNodes(nodes)) as ProbeNodeItem[];
}

async function createProbeNode(nodeName: string): Promise<ProbeNodeItem> {
  const api = getWailsAppApi();
  return (await api.CreateProbeNode(nodeName)) as ProbeNodeItem;
}

async function updateProbeNodeSettings(payload: {
  node_no: number;
  node_name: string;
  remark: string;
  target_system: ProbeTargetSystem;
  direct_connect: boolean;
  payment_cycle: string;
  cost: string;
  expire_at: string;
  vendor_name: string;
  vendor_url: string;
}): Promise<ProbeNodeItem> {
  const api = getWailsAppApi();
  if (!api.UpdateProbeNodeSettings) {
    if (!api.ReplaceProbeNodes) {
      return (await api.UpdateProbeNode(payload.node_no, payload.target_system, payload.direct_connect)) as ProbeNodeItem;
    }
    const currentNodes = (await api.GetProbeNodes()) as ProbeNodeItem[];
    const nextNodes = currentNodes.map((node) => {
      if (node.node_no !== payload.node_no) {
        return node;
      }
      return {
        ...node,
        node_name: payload.node_name,
        remark: payload.remark,
        target_system: payload.target_system,
        direct_connect: payload.direct_connect,
        payment_cycle: payload.payment_cycle,
        cost: payload.cost,
        expire_at: payload.expire_at,
        vendor_name: payload.vendor_name,
        vendor_url: payload.vendor_url,
      };
    });
    const replaced = (await api.ReplaceProbeNodes(nextNodes)) as ProbeNodeItem[];
    const updated = replaced.find((node) => node.node_no === payload.node_no);
    if (!updated) {
      throw new Error("updated node not found after local replace");
    }
    return updated;
  }
  return (await api.UpdateProbeNodeSettings(
    payload.node_no,
    payload.node_name,
    payload.remark,
    payload.target_system,
    payload.direct_connect,
    payload.payment_cycle,
    payload.cost,
    payload.expire_at,
    payload.vendor_name,
    payload.vendor_url,
  )) as ProbeNodeItem;
}

function getWailsAppApi(): {
  GetProbeNodes: () => Promise<unknown>;
  CreateProbeNode: (nodeName: string) => Promise<unknown>;
  UpdateProbeNode: (nodeNo: number, targetSystem: string, directConnect: boolean) => Promise<unknown>;
  UpdateProbeNodeSettings?: (
    nodeNo: number,
    nodeName: string,
    remark: string,
    targetSystem: string,
    directConnect: boolean,
    paymentCycle: string,
    cost: string,
    expireAt: string,
    vendorName: string,
    vendorURL: string,
  ) => Promise<unknown>;
  ReplaceProbeNodes?: (nodes: ProbeNodeItem[]) => Promise<unknown>;
} {
  const api = (window as unknown as { go?: { main?: { App?: unknown } } }).go?.main?.App as {
    GetProbeNodes?: () => Promise<unknown>;
    CreateProbeNode?: (nodeName: string) => Promise<unknown>;
    UpdateProbeNode?: (nodeNo: number, targetSystem: string, directConnect: boolean) => Promise<unknown>;
    UpdateProbeNodeSettings?: (
      nodeNo: number,
      nodeName: string,
      remark: string,
      targetSystem: string,
      directConnect: boolean,
      paymentCycle: string,
      cost: string,
      expireAt: string,
      vendorName: string,
      vendorURL: string,
    ) => Promise<unknown>;
    ReplaceProbeNodes?: (nodes: ProbeNodeItem[]) => Promise<unknown>;
  } | undefined;

  if (!api?.GetProbeNodes || !api?.CreateProbeNode || !api?.UpdateProbeNode) {
    throw new Error("wails probe node api unavailable");
  }
  return {
    GetProbeNodes: api.GetProbeNodes,
    CreateProbeNode: api.CreateProbeNode,
    UpdateProbeNode: api.UpdateProbeNode,
    UpdateProbeNodeSettings: api.UpdateProbeNodeSettings,
    ReplaceProbeNodes: api.ReplaceProbeNodes,
  };
}

async function copyText(text: string): Promise<void> {
  if (typeof navigator !== "undefined" && navigator.clipboard?.writeText) {
    await navigator.clipboard.writeText(text);
    return;
  }

  if (typeof document !== "undefined") {
    const textarea = document.createElement("textarea");
    textarea.value = text;
    textarea.style.position = "fixed";
    textarea.style.opacity = "0";
    document.body.appendChild(textarea);
    textarea.focus();
    textarea.select();
    document.execCommand("copy");
    document.body.removeChild(textarea);
    return;
  }

  throw new Error("clipboard api unavailable");
}

async function fetchProbeNodesFromController(controllerBaseUrl: string, sessionToken: string): Promise<ProbeNodeItem[]> {
  const base = sanitizeControllerAddress(controllerBaseUrl);
  const token = sessionToken.trim();
  if (!token) {
    throw new Error("session token is empty, cannot fetch nodes from controller");
  }
  return (await fetchProbeNodes(base, token)) as ProbeNodeItem[];
}

async function syncProbeNodesToController(controllerBaseUrl: string, sessionToken: string, nodes: ProbeNodeItem[]): Promise<ProbeNodeItem[]> {
  const base = sanitizeControllerAddress(controllerBaseUrl);
  const token = sessionToken.trim();
  if (!token) {
    throw new Error("session token is empty, cannot sync nodes to controller");
  }
  const synced = await syncProbeNodes(base, token, nodes as ProbeNodeSyncItem[]);
  return synced as ProbeNodeItem[];
}

async function fetchProbeStatusFromController(controllerBaseUrl: string, sessionToken: string, nodeID?: number): Promise<ProbeNodeStatusItem[]> {
  const base = sanitizeControllerAddress(controllerBaseUrl);
  const token = sessionToken.trim();
  if (!token) {
    throw new Error("session token is empty, cannot fetch status from controller");
  }
  return await fetchProbeNodeStatus(base, token, nodeID);
}

async function fetchProbeReportIntervalFromController(controllerBaseUrl: string, sessionToken: string): Promise<ProbeReportIntervalSettings> {
  const base = sanitizeControllerAddress(controllerBaseUrl);
  const token = sessionToken.trim();
  if (!token) {
    throw new Error("session token is empty, cannot fetch report interval settings");
  }
  return await fetchProbeReportIntervalSettings(base, token);
}

async function setProbeReportIntervalOnController(controllerBaseUrl: string, sessionToken: string, intervalSec: number): Promise<ProbeReportIntervalSettings> {
  const base = sanitizeControllerAddress(controllerBaseUrl);
  const token = sessionToken.trim();
  if (!token) {
    throw new Error("session token is empty, cannot set report interval");
  }
  return await setProbeReportInterval(base, token, intervalSec);
}

async function syncFromControllerToLocal(controllerBaseUrl: string, sessionToken: string): Promise<void> {
  const remoteNodes = await fetchProbeNodesFromController(controllerBaseUrl, sessionToken);
  await replaceLocalProbeNodes(remoteNodes);
}
