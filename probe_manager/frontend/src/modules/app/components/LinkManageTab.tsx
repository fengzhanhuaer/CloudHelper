import { useEffect, useMemo, useState } from "react";
import { TestProbeLink } from "../../../../wailsjs/go/main/App";
import {
  fetchProbeNodes,
  startProbeLinkTestOnController,
  stopProbeLinkTestOnController,
  type ProbeNodeSyncItem,
} from "../services/controller-api";

type LinkManageTabProps = {
  controllerBaseUrl: string;
  sessionToken: string;
};

type ProbeLinkTestProtocol = "tcp" | "https" | "http3";

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

const defaultInternalPort = 16031;

export function LinkManageTab(props: LinkManageTabProps) {
  const [subTab, setSubTab] = useState<"test">("test");
  const [nodes, setNodes] = useState<ProbeNodeSyncItem[]>([]);
  const [selectedNodeID, setSelectedNodeID] = useState("");
  const [protocol, setProtocol] = useState<ProbeLinkTestProtocol>("tcp");
  const [internalPort, setInternalPort] = useState(defaultInternalPort);
  const [externalPort, setExternalPort] = useState(defaultInternalPort);
  const [isLoadingNodes, setIsLoadingNodes] = useState(false);
  const [isOperating, setIsOperating] = useState(false);
  const [status, setStatus] = useState("未执行测试");
  const [latencyMS, setLatencyMS] = useState<number | null>(null);
  const [resultSummary, setResultSummary] = useState("");

  useEffect(() => {
    if (!props.sessionToken.trim()) {
      setNodes([]);
      setSelectedNodeID("");
      setStatus("未登录，无法加载探针列表");
      return;
    }
    void loadNodes();
  }, [props.controllerBaseUrl, props.sessionToken]);

  const selectedNode = useMemo(
    () => nodes.find((item) => String(item.node_no) === selectedNodeID),
    [nodes, selectedNodeID],
  );
  const apiDomain = useMemo(() => resolveNodeAPIDomain(selectedNode), [selectedNode]);

  async function loadNodes() {
    setIsLoadingNodes(true);
    try {
      const data = await fetchProbeNodes(props.controllerBaseUrl, props.sessionToken);
      const sorted = [...data].sort((left, right) => left.node_no - right.node_no);
      setNodes(sorted);
      if (!sorted.length) {
        setSelectedNodeID("");
        setStatus("暂无探针，请先在探针管理中创建节点");
        return;
      }
      setSelectedNodeID((prev) => {
        if (prev && sorted.some((item) => String(item.node_no) === prev)) {
          return prev;
        }
        return String(sorted[0].node_no);
      });
      setStatus(`已加载 ${sorted.length} 个探针`);
    } catch (error) {
      const msg = error instanceof Error ? error.message : "unknown error";
      setStatus(`加载探针失败：${msg}`);
    } finally {
      setIsLoadingNodes(false);
    }
  }

  async function handleStartTest() {
    if (!props.sessionToken.trim()) {
      setStatus("未登录，无法开始测试");
      return;
    }
    const nodeID = selectedNodeID.trim();
    if (!nodeID) {
      setStatus("请选择探针");
      return;
    }
    if (!apiDomain) {
      setStatus("未找到探针 API 域名，请先在探针管理里配置公网地址或 DDNS（建议以 api. 开头）");
      return;
    }

    const safeInternalPort = normalizePort(internalPort);
    const safeExternalPort = normalizePort(externalPort);
    if (safeInternalPort <= 0 || safeExternalPort <= 0) {
      setStatus("内部端口与外部端口都必须在 1-65535 范围内");
      return;
    }

    setIsOperating(true);
    setLatencyMS(null);
    setResultSummary("");
    setStatus("正在下发开测命令...");
    try {
      const startResp = await startProbeLinkTestOnController(props.controllerBaseUrl, props.sessionToken, {
        node_id: nodeID,
        protocol,
        internal_port: safeInternalPort,
      });
      const startMessage = startResp.message || "探针已启动测试服务";
      setStatus(`${startMessage}，正在连接 ${apiDomain}:${safeExternalPort} ...`);

      const result = (await TestProbeLink(nodeID, protocol, protocol, apiDomain, safeExternalPort)) as ProbeLinkConnectResult;
      const latency = typeof result.duration_ms === "number" ? result.duration_ms : null;
      setLatencyMS(latency);
      setResultSummary(buildResultSummary(result));
      setStatus(`测试成功：延迟 ${latency === null ? "-" : `${latency}ms`}`);
    } catch (error) {
      const msg = error instanceof Error ? error.message : "unknown error";
      setStatus(`测试失败：${msg}`);
    } finally {
      setIsOperating(false);
    }
  }

  async function handleStopTest() {
    const nodeID = selectedNodeID.trim();
    if (!nodeID) {
      setStatus("请选择探针");
      return;
    }
    setIsOperating(true);
    try {
      const stopResp = await stopProbeLinkTestOnController(props.controllerBaseUrl, props.sessionToken, nodeID);
      setStatus(stopResp.message || "已关闭测试，探针测试服务已停止");
    } catch (error) {
      const msg = error instanceof Error ? error.message : "unknown error";
      setStatus(`关闭测试失败：${msg}`);
    } finally {
      setIsOperating(false);
    }
  }

  return (
    <div className="content-block">
      <h2>链路管理</h2>

      <div className="subtab-list" style={{ marginBottom: 12 }}>
        <button className={`subtab-btn ${subTab === "test" ? "active" : ""}`} onClick={() => setSubTab("test")}>测试</button>
      </div>

      {subTab === "test" ? (
        <>
          <div className="identity-card">
            <div className="row">
              <label>探针</label>
              <select
                className="input"
                value={selectedNodeID}
                onChange={(event) => setSelectedNodeID(event.target.value)}
                disabled={isOperating || isLoadingNodes}
              >
                {nodes.map((item) => (
                  <option key={item.node_no} value={String(item.node_no)}>
                    #{item.node_no} {item.node_name}
                  </option>
                ))}
              </select>
            </div>
            <div className="row">
              <label>协议</label>
              <select
                className="input"
                value={protocol}
                onChange={(event) => setProtocol(event.target.value as ProbeLinkTestProtocol)}
                disabled={isOperating}
              >
                <option value="tcp">tcp</option>
                <option value="https">https</option>
                <option value="http3">http3</option>
              </select>
            </div>
            <div className="row">
              <label>探针服务IP</label>
              <input className="input" value="0.0.0.0" disabled />
            </div>
            <div className="row">
              <label>内部端口</label>
              <input
                className="input"
                type="number"
                min={1}
                max={65535}
                value={internalPort}
                onChange={(event) => setInternalPort(Number(event.target.value) || 0)}
                disabled={isOperating}
              />
            </div>
            <div className="row">
              <label>外部端口</label>
              <input
                className="input"
                type="number"
                min={1}
                max={65535}
                value={externalPort}
                onChange={(event) => setExternalPort(Number(event.target.value) || 0)}
                disabled={isOperating}
              />
            </div>
          </div>

          <div className="content-actions">
            <button className="btn" onClick={() => void loadNodes()} disabled={isLoadingNodes || isOperating}>
              {isLoadingNodes ? "刷新中..." : "刷新探针"}
            </button>
            <button className="btn" onClick={() => void handleStartTest()} disabled={isOperating || !selectedNodeID}>
              {isOperating ? "处理中..." : "开始测试"}
            </button>
            <button className="btn" onClick={() => void handleStopTest()} disabled={isOperating || !selectedNodeID}>
              关闭测试
            </button>
          </div>

          <div className="status">{status}</div>
          <div className="status">API 域名：{apiDomain || "-"}</div>
          <div className="status">链路延迟：{latencyMS === null ? "-" : `${latencyMS} ms`}</div>
          <div className="status">{resultSummary || "暂无测试结果详情"}</div>
        </>
      ) : null}
    </div>
  );
}

function normalizePort(value: number): number {
  if (!Number.isFinite(value)) {
    return 0;
  }
  const port = Math.trunc(value);
  if (port <= 0 || port > 65535) {
    return 0;
  }
  return port;
}

function resolveNodeAPIDomain(node?: ProbeNodeSyncItem): string {
  if (!node) {
    return "";
  }
  const candidates = [
    normalizeHost(node.public_host),
    normalizeHost(node.ddns),
    normalizeHost(node.service_host),
  ].filter((item) => item !== "");
  if (!candidates.length) {
    return "";
  }
  const apiFirst = candidates.find((item) => item.toLowerCase().startsWith("api."));
  return apiFirst || "";
}

function normalizeHost(raw: unknown): string {
  let value = String(raw ?? "").trim();
  if (!value) {
    return "";
  }

  if (value.includes("://")) {
    try {
      const parsed = new URL(value);
      value = parsed.host;
    } catch {
      return "";
    }
  }

  value = value.split("/")[0].trim();
  if (!value) {
    return "";
  }

  if (value.startsWith("[") && value.endsWith("]")) {
    return value.slice(1, -1).trim();
  }

  const lastColon = value.lastIndexOf(":");
  if (lastColon > 0 && value.indexOf(":") === lastColon) {
    const maybePort = value.slice(lastColon + 1);
    if (/^\d+$/.test(maybePort)) {
      return value.slice(0, lastColon).trim();
    }
  }

  return value;
}

function buildResultSummary(result: ProbeLinkConnectResult): string {
  const parts = [
    result.message || "",
    result.url ? `URL=${result.url}` : "",
    result.status_code ? `HTTP=${result.status_code}` : "",
    result.node_id ? `node_id=${result.node_id}` : "",
    result.endpoint_type ? `protocol=${result.endpoint_type}` : "",
  ].filter((item) => item !== "");
  return parts.join(" | ");
}
