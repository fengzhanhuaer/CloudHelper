import { useEffect, useMemo, useState } from "react";
import { TestProbeLink } from "../../../../wailsjs/go/main/App";
import {
  fetchProbeNodeStatus,
  fetchProbeNodes,
  updateProbeNodeLinkOnController,
  type ProbeNodeStatusItem,
  type ProbeNodeSyncItem,
} from "../services/controller-api";

type LinkManageTabProps = {
  controllerBaseUrl: string;
  sessionToken: string;
};

type ProbeEndpointType = "service" | "public";

type ProbeLinkConnectResult = {
  ok?: boolean;
  node_id?: string;
  endpoint_type?: string;
  url?: string;
  status_code?: number;
  service?: string;
  version?: string;
  message?: string;
  connected_at?: string;
  duration_ms?: number;
};

type LinkNodeItem = ProbeNodeSyncItem & {
  runtime?: ProbeNodeStatusItem["runtime"];
};

type NodeMessageMap = Record<number, string>;
type TestingKeyMap = Record<string, boolean>;

const defaultServiceScheme: "http" | "https" = "http";
const defaultPublicScheme: "http" | "https" = "http";
const defaultServicePort = 16030;

export function LinkManageTab(props: LinkManageTabProps) {
  const [nodes, setNodes] = useState<LinkNodeItem[]>([]);
  const [status, setStatus] = useState("正在加载链路配置...");
  const [isLoading, setIsLoading] = useState(false);
  const [savingNodes, setSavingNodes] = useState<NodeMessageMap>({});
  const [nodeMessages, setNodeMessages] = useState<NodeMessageMap>({});
  const [testingKeys, setTestingKeys] = useState<TestingKeyMap>({});

  useEffect(() => {
    if (!props.sessionToken.trim()) {
      setNodes([]);
      setStatus("未登录，无法加载链路配置");
      return;
    }
    void loadNodes();
  }, [props.controllerBaseUrl, props.sessionToken]);

  const onlineCount = useMemo(() => nodes.filter((item) => item.runtime?.online).length, [nodes]);

  async function loadNodes() {
    setIsLoading(true);
    try {
      const [rawNodes, statusItems] = await Promise.all([
        fetchProbeNodes(props.controllerBaseUrl, props.sessionToken),
        fetchProbeNodeStatus(props.controllerBaseUrl, props.sessionToken),
      ]);
      const merged = mergeNodes(rawNodes, statusItems);
      setNodes(merged);
      setNodeMessages({});
      setStatus(merged.length ? `已加载 ${merged.length} 条探针链路配置，在线 ${onlineCountOf(merged)} 条` : "暂无探针，请先在探针管理中创建节点");
    } catch (error) {
      const msg = error instanceof Error ? error.message : "unknown error";
      setStatus(`加载链路配置失败：${msg}`);
    } finally {
      setIsLoading(false);
    }
  }

  function updateNodeField(nodeNo: number, field: keyof LinkNodeItem, value: string | number) {
    setNodes((prev) => prev.map((item) => {
      if (item.node_no !== nodeNo) {
        return item;
      }
      return { ...item, [field]: value };
    }));
  }

  async function saveLink(node: LinkNodeItem) {
    const servicePort = normalizeServicePort(node.service_port);
    const publicPort = normalizePublicPort(node.public_port);
    if (!node.service_host?.trim()) {
      setNodeMessages((prev) => ({ ...prev, [node.node_no]: "服务地址不能为空" }));
      return;
    }

    setSavingNodes((prev) => ({ ...prev, [node.node_no]: "saving" }));
    try {
      const updated = await updateProbeNodeLinkOnController(props.controllerBaseUrl, props.sessionToken, {
        node_no: node.node_no,
        service_scheme: normalizeScheme(node.service_scheme, defaultServiceScheme),
        service_host: String(node.service_host ?? "").trim(),
        service_port: servicePort,
        public_scheme: normalizeScheme(node.public_scheme, defaultPublicScheme),
        public_host: String(node.public_host ?? "").trim(),
        public_port: publicPort,
      });

      setNodes((prev) => prev.map((item) => item.node_no === node.node_no ? { ...normalizeLinkNode(updated), runtime: item.runtime } : item));
      setNodeMessages((prev) => ({ ...prev, [node.node_no]: "链路配置已保存" }));
      setStatus(`已保存探针 #${node.node_no} ${node.node_name} 的链路配置`);
    } catch (error) {
      const msg = error instanceof Error ? error.message : "unknown error";
      setNodeMessages((prev) => ({ ...prev, [node.node_no]: `保存失败：${msg}` }));
    } finally {
      setSavingNodes((prev) => {
        const next = { ...prev };
        delete next[node.node_no];
        return next;
      });
    }
  }

  async function testLink(node: LinkNodeItem, endpointType: ProbeEndpointType) {
    const isPublic = endpointType === "public";
    const scheme = isPublic ? normalizeScheme(node.public_scheme, defaultPublicScheme) : normalizeScheme(node.service_scheme, defaultServiceScheme);
    const host = isPublic ? String(node.public_host ?? "").trim() : String(node.service_host ?? "").trim();
    const port = isPublic ? normalizePublicPort(node.public_port) : normalizeServicePort(node.service_port);
    const testingKey = `${node.node_no}:${endpointType}`;

    if (!host) {
      setNodeMessages((prev) => ({ ...prev, [node.node_no]: isPublic ? "公网地址未配置，无法测试公网连接" : "服务地址未配置，无法测试服务连接" }));
      return;
    }
    if (isPublic && port <= 0) {
      setNodeMessages((prev) => ({ ...prev, [node.node_no]: "公网端口未配置，无法测试公网连接" }));
      return;
    }

    setTestingKeys((prev) => ({ ...prev, [testingKey]: true }));
    setNodeMessages((prev) => ({ ...prev, [node.node_no]: `正在测试${isPublic ? "公网" : "服务"}连接...` }));
    try {
      const result = (await TestProbeLink(String(node.node_no), endpointType, scheme, host, port)) as ProbeLinkConnectResult;
      const summary = buildConnectSummary(result);
      setNodeMessages((prev) => ({ ...prev, [node.node_no]: summary }));
      setStatus(`探针 #${node.node_no} ${node.node_name} ${isPublic ? "公网" : "服务"}连接测试成功`);
    } catch (error) {
      const msg = error instanceof Error ? error.message : "unknown error";
      setNodeMessages((prev) => ({ ...prev, [node.node_no]: `${isPublic ? "公网" : "服务"}连接失败：${msg}` }));
    } finally {
      setTestingKeys((prev) => {
        const next = { ...prev };
        delete next[testingKey];
        return next;
      });
    }
  }

  return (
    <div className="content-block">
      <h2>链路管理</h2>

      <div className="content-actions">
        <button className="btn" onClick={() => void loadNodes()} disabled={isLoading}>
          {isLoading ? "刷新中..." : "刷新链路"}
        </button>
      </div>

      <div className="status">{status}</div>
      <div className="status">链路概览：总计 {nodes.length} 条，在线 {onlineCount} 条。第一步当前基于 probe 的 HTTP 服务接口 `/api/node/info` 建连测试。</div>

      {!nodes.length ? (
        <div className="status">暂无可管理的探针链路。请先在“探针管理”中创建并同步 probe 节点。</div>
      ) : nodes.map((node) => {
        const saving = Boolean(savingNodes[node.node_no]);
        const serviceTesting = Boolean(testingKeys[`${node.node_no}:service`]);
        const publicTesting = Boolean(testingKeys[`${node.node_no}:public`]);

        return (
          <section key={node.node_no} className="link-node-card">
            <div className="link-node-header">
              <div>
                <div className="link-node-title">#{node.node_no} {node.node_name}</div>
                <div className="link-node-meta">
                  运行状态：{node.runtime?.online ? "在线" : "离线"} | 最近上报：{node.runtime?.last_seen || "-"} | DDNS：{node.ddns || "-"}
                </div>
                <div className="link-node-meta">
                  Probe IP：{joinIPs(node.runtime)}
                </div>
              </div>
            </div>

            <div className="link-node-grid">
              <div className="link-endpoint-panel">
                <div className="link-endpoint-title">服务端点</div>
                <div className="row">
                  <label>协议</label>
                  <select
                    className="input"
                    value={normalizeScheme(node.service_scheme, defaultServiceScheme)}
                    onChange={(event) => updateNodeField(node.node_no, "service_scheme", event.target.value)}
                    disabled={saving}
                  >
                    <option value="http">http</option>
                    <option value="https">https</option>
                  </select>
                </div>
                <div className="row">
                  <label>服务地址</label>
                  <input
                    className="input"
                    value={String(node.service_host ?? "")}
                    onChange={(event) => updateNodeField(node.node_no, "service_host", event.target.value)}
                    disabled={saving}
                    placeholder="例如 10.0.0.8 或 probe.example.com"
                  />
                </div>
                <div className="row">
                  <label>服务端口</label>
                  <input
                    className="input"
                    type="number"
                    min={1}
                    max={65535}
                    value={normalizeServicePort(node.service_port)}
                    onChange={(event) => updateNodeField(node.node_no, "service_port", Number(event.target.value) || defaultServicePort)}
                    disabled={saving}
                  />
                </div>
                <div className="content-actions">
                  <button className="btn" onClick={() => void testLink(node, "service")} disabled={saving || serviceTesting}>
                    {serviceTesting ? "测试中..." : "测试服务连接"}
                  </button>
                </div>
              </div>

              <div className="link-endpoint-panel">
                <div className="link-endpoint-title">公网端点（NAT 手工映射）</div>
                <div className="row">
                  <label>协议</label>
                  <select
                    className="input"
                    value={normalizeScheme(node.public_scheme, defaultPublicScheme)}
                    onChange={(event) => updateNodeField(node.node_no, "public_scheme", event.target.value)}
                    disabled={saving}
                  >
                    <option value="http">http</option>
                    <option value="https">https</option>
                  </select>
                </div>
                <div className="row">
                  <label>公网地址</label>
                  <input
                    className="input"
                    value={String(node.public_host ?? "")}
                    onChange={(event) => updateNodeField(node.node_no, "public_host", event.target.value)}
                    disabled={saving}
                    placeholder="例如 203.0.113.8 或 nat.example.com"
                  />
                </div>
                <div className="row">
                  <label>公网端口</label>
                  <input
                    className="input"
                    type="number"
                    min={0}
                    max={65535}
                    value={normalizePublicPort(node.public_port)}
                    onChange={(event) => updateNodeField(node.node_no, "public_port", Number(event.target.value) || 0)}
                    disabled={saving}
                  />
                </div>
                <div className="content-actions">
                  <button className="btn" onClick={() => void testLink(node, "public")} disabled={saving || publicTesting}>
                    {publicTesting ? "测试中..." : "测试公网连接"}
                  </button>
                </div>
              </div>
            </div>

            <div className="content-actions">
              <button className="btn" onClick={() => void saveLink(node)} disabled={saving}>
                {saving ? "保存中..." : "保存链路配置"}
              </button>
            </div>

            <div className="status">{nodeMessages[node.node_no] || "尚未执行链路测试"}</div>
          </section>
        );
      })}
    </div>
  );
}

function mergeNodes(nodes: ProbeNodeSyncItem[], statusItems: ProbeNodeStatusItem[]): LinkNodeItem[] {
  const runtimeMap = new Map<number, ProbeNodeStatusItem["runtime"]>();
  for (const item of statusItems) {
    runtimeMap.set(item.node_no, item.runtime);
  }
  return nodes
    .map((item) => {
      const normalized = normalizeLinkNode(item);
      return {
        ...normalized,
        runtime: runtimeMap.get(item.node_no),
      };
    })
    .sort((left, right) => left.node_no - right.node_no);
}

function normalizeLinkNode(item: ProbeNodeSyncItem): ProbeNodeSyncItem {
  return {
    ...item,
    service_scheme: normalizeScheme(item.service_scheme, defaultServiceScheme),
    service_host: String(item.service_host ?? "").trim(),
    service_port: normalizeServicePort(item.service_port),
    public_scheme: normalizeScheme(item.public_scheme, defaultPublicScheme),
    public_host: String(item.public_host ?? "").trim(),
    public_port: normalizePublicPort(item.public_port),
  };
}

function normalizeScheme(value: unknown, fallback: "http" | "https"): "http" | "https" {
  return String(value ?? "").trim().toLowerCase() === "https" ? "https" : fallback;
}

function normalizeServicePort(value: unknown): number {
  const num = Number(value);
  if (!Number.isFinite(num)) {
    return defaultServicePort;
  }
  const normalized = Math.trunc(num);
  if (normalized <= 0 || normalized > 65535) {
    return defaultServicePort;
  }
  return normalized;
}

function normalizePublicPort(value: unknown): number {
  const num = Number(value);
  if (!Number.isFinite(num)) {
    return 0;
  }
  const normalized = Math.trunc(num);
  if (normalized <= 0 || normalized > 65535) {
    return 0;
  }
  return normalized;
}

function buildConnectSummary(result: ProbeLinkConnectResult): string {
  const parts = [
    result.message || "连接成功",
    result.url ? `URL=${result.url}` : "",
    result.node_id ? `node_id=${result.node_id}` : "",
    result.version ? `version=${result.version}` : "",
    typeof result.duration_ms === "number" ? `耗时=${result.duration_ms}ms` : "",
  ].filter(Boolean);
  return parts.join(" | ");
}

function joinIPs(nodeRuntime?: ProbeNodeStatusItem["runtime"]): string {
  if (!nodeRuntime) {
    return "-";
  }
  const values = [...(nodeRuntime.ipv4 || []), ...(nodeRuntime.ipv6 || [])].filter((item) => String(item).trim() !== "");
  return values.length ? values.join(", ") : "-";
}

function onlineCountOf(nodes: LinkNodeItem[]): number {
  return nodes.filter((item) => item.runtime?.online).length;
}
