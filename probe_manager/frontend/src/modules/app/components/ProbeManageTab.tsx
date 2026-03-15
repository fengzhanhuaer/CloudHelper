import { useEffect, useState } from "react";
import { fetchProbeNodes, syncProbeNodes, upgradeAllProbeNodes, upgradeProbeNode, type ProbeNodeSyncItem } from "../services/controller-api";

type ProbeManageTabProps = {
  controllerBaseUrl: string;
  sessionToken: string;
};

type ProbeSubTab = "create" | "list";
type ProbeTargetSystem = "linux" | "windows";

type ProbeNodeItem = {
  node_no: number;
  node_name: string;
  node_secret: string;
  target_system: ProbeTargetSystem;
  direct_connect: boolean;
  created_at: string;
  updated_at: string;
};

export function ProbeManageTab(props: ProbeManageTabProps) {
  const [subTab, setSubTab] = useState<ProbeSubTab>("create");
  const [nodeNameInput, setNodeNameInput] = useState("");
  const [controllerAddress, setControllerAddress] = useState(props.controllerBaseUrl || "");
  const [nodes, setNodes] = useState<ProbeNodeItem[]>([]);
  const [isLoading, setIsLoading] = useState(false);
  const [isUpgradingAll, setIsUpgradingAll] = useState(false);
  const [upgradingNodeNos, setUpgradingNodeNos] = useState<number[]>([]);
  const [status, setStatus] = useState("正在加载探针列表...");

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
      const syncedLocal = await replaceLocalProbeNodes(remoteNodes);
      setNodes(sortNodes(syncedLocal));
      setStatus(syncedLocal.length ? "已从主控同步探针列表" : "主控暂无探针，请先创建");
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
      const local = await replaceLocalProbeNodes(synced);
      setNodes(sortNodes(local));
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

  async function updateNode(nodeNo: number, patch: Partial<Pick<ProbeNodeItem, "target_system" | "direct_connect">>) {
    const current = nodes.find((item) => item.node_no === nodeNo);
    if (!current) {
      return;
    }

    const nextTargetSystem = patch.target_system ?? current.target_system;
    const nextDirectConnect = patch.direct_connect ?? current.direct_connect;

    setIsLoading(true);
    try {
      await syncFromControllerToLocal(controllerAddress, props.sessionToken);
      const updated = await updateProbeNode(nodeNo, nextTargetSystem, nextDirectConnect);
      const refreshed = await getProbeNodes();
      const synced = await syncProbeNodesToController(controllerAddress, props.sessionToken, refreshed);
      const local = await replaceLocalProbeNodes(synced);
      setNodes(sortNodes(local));
      setStatus(`节点已更新并同步到主控：${updated.node_name}`);
    } catch (error) {
      const msg = error instanceof Error ? error.message : "unknown error";
      setStatus(`更新节点失败：${msg}`);
    } finally {
      setIsLoading(false);
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
      ) : (
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
                  <div className="probe-node-meta">节点号：{node.node_no}</div>
                  <div className="probe-node-meta">节点 Secret：{node.node_secret}</div>
                  <div className="probe-node-meta">创建时间：{formatTime(node.created_at)}</div>
                  <div className="probe-node-meta">更新时间：{formatTime(node.updated_at)}</div>

                  <div className="row" style={{ marginTop: 10, marginBottom: 10 }}>
                    <label>目标系统</label>
                    <select
                      className="input"
                      value={node.target_system}
                      onChange={(event) => void updateNode(node.node_no, { target_system: event.target.value as ProbeTargetSystem })}
                      disabled={isLoading}
                    >
                      <option value="linux">Linux</option>
                      <option value="windows">Windows</option>
                    </select>
                  </div>

                  <label className="probe-direct-toggle">
                    <input
                      type="checkbox"
                      checked={node.direct_connect}
                      onChange={(event) => void updateNode(node.node_no, { direct_connect: event.target.checked })}
                      disabled={isLoading}
                    />
                    是否直连（关闭后通过主控下载/安装/升级，并携带 Secret）
                  </label>

                  <div className="content-actions">
                    <button className="btn" onClick={() => void copyInstallCommand(node)} disabled={isLoading}>复制安装命令</button>
                    <button className="btn" onClick={() => void upgradeOne(node)} disabled={isLoading || isUpgradingAll || upgradingNodeNos.includes(node.node_no)}>
                      {upgradingNodeNos.includes(node.node_no) ? "下发中..." : "升级该探针"}
                    </button>
                  </div>
                </div>
              ))}
            </div>
          )}
        </div>
      )}

      <div className="status">{status}</div>
    </div>
  );
}

function sortNodes(nodes: ProbeNodeItem[]): ProbeNodeItem[] {
  return [...nodes].sort((a, b) => a.node_no - b.node_no);
}

function formatTime(isoTime: string): string {
  const dt = new Date(isoTime);
  if (Number.isNaN(dt.getTime())) {
    return "-";
  }
  return dt.toLocaleString();
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

  if (node.direct_connect) {
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

    return "curl -fsSL https://raw.githubusercontent.com/fengzhanhuaer/CloudHelper/main/scripts/install_probe_node_service.sh | sudo PROBE_NODE_ID='" + String(node.node_no) + "' PROBE_NODE_SECRET='" + node.node_secret + "' PROBE_CONTROLLER_URL='" + base + "' bash";
  }

  const params = new URLSearchParams({
    node_id: String(node.node_no),
    node_name: node.node_name,
    secret: node.node_secret,
    target: node.target_system,
  });

  if (node.target_system === "windows") {
    return "powershell -NoProfile -ExecutionPolicy Bypass -Command \"irm '" + base + "/api/admin/proxy/probe-node/install-script?" + params.toString() + "' | iex\"";
  }

  return "curl -fsSL '" + base + "/api/admin/proxy/probe-node/install-script?" + params.toString() + "' | sudo bash";
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

async function updateProbeNode(nodeNo: number, targetSystem: ProbeTargetSystem, directConnect: boolean): Promise<ProbeNodeItem> {
  const api = getWailsAppApi();
  return (await api.UpdateProbeNode(nodeNo, targetSystem, directConnect)) as ProbeNodeItem;
}

function getWailsAppApi(): {
  GetProbeNodes: () => Promise<unknown>;
  CreateProbeNode: (nodeName: string) => Promise<unknown>;
  UpdateProbeNode: (nodeNo: number, targetSystem: string, directConnect: boolean) => Promise<unknown>;
  ReplaceProbeNodes?: (nodes: ProbeNodeItem[]) => Promise<unknown>;
} {
  const api = (window as unknown as { go?: { main?: { App?: unknown } } }).go?.main?.App as {
    GetProbeNodes?: () => Promise<unknown>;
    CreateProbeNode?: (nodeName: string) => Promise<unknown>;
    UpdateProbeNode?: (nodeNo: number, targetSystem: string, directConnect: boolean) => Promise<unknown>;
    ReplaceProbeNodes?: (nodes: ProbeNodeItem[]) => Promise<unknown>;
  } | undefined;

  if (!api?.GetProbeNodes || !api?.CreateProbeNode || !api?.UpdateProbeNode) {
    throw new Error("wails probe node api unavailable");
  }
  return {
    GetProbeNodes: api.GetProbeNodes,
    CreateProbeNode: api.CreateProbeNode,
    UpdateProbeNode: api.UpdateProbeNode,
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

async function syncFromControllerToLocal(controllerBaseUrl: string, sessionToken: string): Promise<void> {
  const remoteNodes = await fetchProbeNodesFromController(controllerBaseUrl, sessionToken);
  await replaceLocalProbeNodes(remoteNodes);
}
