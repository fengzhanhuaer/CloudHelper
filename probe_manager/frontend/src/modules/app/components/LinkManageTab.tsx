import { useEffect, useMemo, useRef, useState } from "react";
import {
  PingProbeLinkSession,
  StartProbeLinkSession,
  StopProbeLinkSession,
} from "../../../../wailsjs/go/main/App";
import {
  deleteProbeLinkChain,
  fetchCloudflareDDNSRecords,
  fetchProbeLinkChains,
  fetchProbeNodeStatus,
  fetchProbeNodes,
  startProbeLinkTestOnController,
  stopProbeLinkTestOnController,
  upsertProbeLinkChain,
  type ProbeLinkChainItem,
  type ProbeNodeStatusItem,
  type ProbeNodeSyncItem,
} from "../services/controller-api";
import type { CloudflareDDNSRecord } from "../types";

type LinkManageTabProps = {
  controllerBaseUrl: string;
  sessionToken: string;
};

type ProbeLinkTestProtocol = "http" | "https" | "http3";

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

type ProbeTestTarget = {
  host: string;
  isAPI: boolean;
  source: string;
};

const defaultInternalPort = 16031;
const linkChainCacheStorageKey = "cloudhelper_probe_link_chains_cache_v1";

export function LinkManageTab(props: LinkManageTabProps) {
  const [subTab, setSubTab] = useState<"test">("test");
  const [nodes, setNodes] = useState<ProbeNodeSyncItem[]>([]);
  const [nodeRuntimes, setNodeRuntimes] = useState<Record<number, ProbeNodeStatusItem["runtime"]>>({});
  const [nodeAPIHosts, setNodeAPIHosts] = useState<Record<number, string>>({});
  const [selectedNodeID, setSelectedNodeID] = useState("");
  const [protocol, setProtocol] = useState<ProbeLinkTestProtocol>("http");
  const [internalPort, setInternalPort] = useState(defaultInternalPort);
  const [externalPort, setExternalPort] = useState(defaultInternalPort);
  const [isLoadingNodes, setIsLoadingNodes] = useState(false);
  const [isOperating, setIsOperating] = useState(false);
  const [isTesting, setIsTesting] = useState(false);
  const [status, setStatus] = useState("未执行测试");
  const [latencyMS, setLatencyMS] = useState<number | null>(null);
  const [resultSummary, setResultSummary] = useState("");
  const continuousTestSeqRef = useRef(0);
  const continuousTestingRef = useRef(false);

  useEffect(() => {
    if (!props.sessionToken.trim()) {
      stopLocalContinuousTestLoop();
      void StopProbeLinkSession();
      setNodes([]);
      setSelectedNodeID("");
      setStatus("未登录，无法加载探针列表");
      return;
    }
    void loadNodes();
  }, [props.controllerBaseUrl, props.sessionToken]);

  useEffect(() => {
    continuousTestingRef.current = isTesting;
  }, [isTesting]);

  useEffect(() => {
    return () => {
      continuousTestingRef.current = false;
      continuousTestSeqRef.current += 1;
      void StopProbeLinkSession();
    };
  }, []);

  const selectedNode = useMemo(
    () => nodes.find((item) => String(item.node_no) === selectedNodeID),
    [nodes, selectedNodeID],
  );
  const selectedRuntime = useMemo(() => {
    if (!selectedNode) {
      return undefined;
    }
    return nodeRuntimes[selectedNode.node_no];
  }, [nodeRuntimes, selectedNode]);
  const selectedAPIHost = useMemo(() => {
    if (!selectedNode) {
      return "";
    }
    return nodeAPIHosts[selectedNode.node_no] || "";
  }, [nodeAPIHosts, selectedNode]);
  const testTargets = useMemo(
    () => resolveNodeTestTargets(selectedNode, selectedRuntime, selectedAPIHost),
    [selectedAPIHost, selectedNode, selectedRuntime],
  );
  const testTarget = testTargets.length > 0 ? testTargets[0] : { host: "", isAPI: false, source: "" };

  useEffect(() => {
    if (!selectedNode) {
      return;
    }
    const preferredPort = normalizePort(Number(selectedNode.public_port || selectedNode.service_port || 0));
    const portToUse = preferredPort > 0 ? preferredPort : defaultInternalPort;
    setInternalPort(portToUse);
    setExternalPort(portToUse);
  }, [selectedNodeID]);

  async function loadNodes() {
    setIsLoadingNodes(true);
    try {
      const [data, statusItems] = await Promise.all([
        fetchProbeNodes(props.controllerBaseUrl, props.sessionToken),
        fetchProbeNodeStatus(props.controllerBaseUrl, props.sessionToken),
      ]);
      let cloudflareAPIHosts: Record<number, string> = {};
      try {
        const ddnsRecords = await fetchCloudflareDDNSRecords(props.controllerBaseUrl, props.sessionToken);
        cloudflareAPIHosts = buildNodeAPIHostsFromCloudflare(ddnsRecords);
      } catch {
        // ignore cloudflare record fetch failure and fallback to probe node fields/runtime ip
      }
      const sorted = [...data].sort((left, right) => left.node_no - right.node_no);
      const runtimeMap: Record<number, ProbeNodeStatusItem["runtime"]> = {};
      for (const item of statusItems) {
        runtimeMap[item.node_no] = item.runtime;
      }
      setNodes(sorted);
      setNodeRuntimes(runtimeMap);
      setNodeAPIHosts(cloudflareAPIHosts);
      if (!sorted.length) {
        stopLocalContinuousTestLoop();
        await closeLocalProbeLinkSessionSilently();
        setSelectedNodeID("");
        setNodeRuntimes({});
        setNodeAPIHosts({});
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
      const msg = errorToMessage(error);
      setStatus(`加载探针失败：${msg}`);
    } finally {
      setIsLoadingNodes(false);
    }
  }

  function stopLocalContinuousTestLoop() {
    continuousTestingRef.current = false;
    setIsTesting(false);
    continuousTestSeqRef.current += 1;
  }

  async function closeLocalProbeLinkSessionSilently() {
    try {
      await StopProbeLinkSession();
    } catch {
      // ignore close failure when switching/stopping
    }
  }

  async function handleSelectedNodeChange(nextNodeIDRaw: string) {
    const nextNodeID = nextNodeIDRaw.trim();
    const prevNodeID = selectedNodeID.trim();
    if (nextNodeID === prevNodeID) {
      return;
    }

    const wasTesting = continuousTestingRef.current || isTesting;
    setSelectedNodeID(nextNodeIDRaw);
    setLatencyMS(null);
    setResultSummary("");

    if (!wasTesting) {
      await closeLocalProbeLinkSessionSilently();
      return;
    }

    setIsOperating(true);
    stopLocalContinuousTestLoop();
    await closeLocalProbeLinkSessionSilently();
    try {
      if (prevNodeID) {
        await stopProbeLinkTestOnController(props.controllerBaseUrl, props.sessionToken, prevNodeID);
      }
      setStatus(nextNodeID
        ? `已切换到探针 #${nextNodeID}，旧测试连接已自动关闭`
        : "已切换探针，旧测试连接已自动关闭");
    } catch (error) {
      const msg = errorToMessage(error);
      setStatus(`切换探针时关闭旧测试失败：${msg}`);
    } finally {
      setIsOperating(false);
    }
  }

  async function runContinuousTestLoop(loopSeq: number) {
    let round = 0;
    while (continuousTestingRef.current && loopSeq === continuousTestSeqRef.current) {
      round += 1;
      try {
        const result = (await PingProbeLinkSession()) as ProbeLinkConnectResult;
        if (!continuousTestingRef.current || loopSeq !== continuousTestSeqRef.current) {
          return;
        }
        const latency = typeof result.duration_ms === "number" ? result.duration_ms : null;
        setLatencyMS(latency);
        setResultSummary(buildResultSummary(result));
        setStatus(`持续测试中：第 ${round} 次，延迟 ${latency === null ? "-" : `${latency}ms`}`);
      } catch (error) {
        if (!continuousTestingRef.current || loopSeq !== continuousTestSeqRef.current) {
          return;
        }
        const msg = errorToMessage(error);
        setResultSummary(`error=${msg}`);
        setStatus(`持续测试异常：${msg}（3秒后重试）`);
      }
      await sleep(3000);
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
    if (testTargets.length === 0) {
      setStatus("未找到可用测试地址，请先在探针管理里配置公网地址，或确认 Cloudflare 已生成 api 域名");
      return;
    }
    if (isTesting) {
      setStatus("链路测试已在持续运行中，请先关闭测试");
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
      stopLocalContinuousTestLoop();
      await closeLocalProbeLinkSessionSilently();

      const startResp = await startProbeLinkTestOnController(props.controllerBaseUrl, props.sessionToken, {
        node_id: nodeID,
        protocol,
        internal_port: safeInternalPort,
      });
      const startMessage = startResp.message || "探针已启动测试服务";
      setStatus(`${startMessage}，正在连接 ${testTarget.host}:${safeExternalPort} ...`);

      const maxConnectAttemptsPerTarget = 4;
      let first: ProbeLinkConnectResult | null = null;
      let connectedTarget: ProbeTestTarget | null = null;
      const connectErrors: string[] = [];
      for (let targetIndex = 0; targetIndex < testTargets.length && !first; targetIndex += 1) {
        const target = testTargets[targetIndex];
        for (let attempt = 1; attempt <= maxConnectAttemptsPerTarget; attempt += 1) {
          try {
            setStatus(`测试服务已启动，正在连接 ${target.host}:${safeExternalPort}（${target.source}，第 ${attempt}/${maxConnectAttemptsPerTarget} 次）...`);
            first = (await StartProbeLinkSession(
              nodeID,
              protocol,
              target.host,
              safeExternalPort,
            )) as ProbeLinkConnectResult;
            connectedTarget = target;
            break;
          } catch (error) {
            const lastConnectErr = errorToMessage(error);
            if (attempt < maxConnectAttemptsPerTarget) {
              setStatus(`等待链路就绪：${target.host}:${safeExternalPort}（${target.source}）失败：${lastConnectErr}`);
              await sleep(1200);
              continue;
            }
            connectErrors.push(`${target.host}(${target.source}): ${lastConnectErr}`);
          }
        }
      }
      if (!first) {
        throw new Error(connectErrors.length > 0 ? `全部目标连接失败：${connectErrors.join(" | ")}` : "failed to establish probe link session");
      }
      const firstLatency = typeof first.duration_ms === "number" ? first.duration_ms : null;
      setLatencyMS(firstLatency);
      setResultSummary(buildResultSummary(first));

      continuousTestSeqRef.current += 1;
      const currentSeq = continuousTestSeqRef.current;
      continuousTestingRef.current = true;
      setIsTesting(true);
      if (connectedTarget) {
        setStatus(`测试已启动，连接已建立，持续检测中：${connectedTarget.host}:${safeExternalPort}（${connectedTarget.source}）`);
      } else {
        setStatus(`测试已启动，连接已建立，持续检测中：${safeExternalPort}`);
      }
      void runContinuousTestLoop(currentSeq);
    } catch (error) {
      const msg = errorToMessage(error);
      setStatus(`测试失败：${msg}（探针测试服务保持开启，便于排查；如需关闭请点击“关闭测试”）`);
      stopLocalContinuousTestLoop();
      await closeLocalProbeLinkSessionSilently();
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
    stopLocalContinuousTestLoop();
    setIsOperating(true);
    let localCloseErr = "";
    try {
      try {
        await StopProbeLinkSession();
      } catch (error) {
        localCloseErr = errorToMessage(error);
      }
      const stopResp = await stopProbeLinkTestOnController(props.controllerBaseUrl, props.sessionToken, nodeID);
      const baseMessage = stopResp.message || "已关闭测试，探针测试服务已停止";
      if (localCloseErr) {
        setStatus(`${baseMessage}（本地连接关闭异常：${localCloseErr}）`);
      } else {
        setStatus(baseMessage);
      }
    } catch (error) {
      const msg = errorToMessage(error);
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
                onChange={(event) => { void handleSelectedNodeChange(event.target.value); }}
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
                disabled={isOperating || isTesting}
              >
                <option value="http">http</option>
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
                disabled={isOperating || isTesting}
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
                disabled={isOperating || isTesting}
              />
            </div>
          </div>

          <div className="content-actions">
            <button className="btn" onClick={() => void loadNodes()} disabled={isLoadingNodes || isOperating || isTesting}>
              {isLoadingNodes ? "刷新中..." : "刷新探针"}
            </button>
            <button className="btn" onClick={() => void handleStartTest()} disabled={isOperating || isTesting || !selectedNodeID}>
              {isTesting ? "测试中..." : isOperating ? "处理中..." : "开始测试"}
            </button>
            <button className="btn" onClick={() => void handleStopTest()} disabled={isOperating || !selectedNodeID}>
              {isOperating ? "关闭中..." : "关闭测试"}
            </button>
          </div>

          <div className="status">{status}</div>
          <div className="status">测试目标：{testTarget.host || "-"} {testTarget.host ? `(${testTarget.source})` : ""}</div>
          <div className="status">候选目标：{testTargets.length > 0 ? testTargets.map((item) => `${item.host}(${item.source})`).join(" | ") : "-"}</div>
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

function resolveNodeTestTargets(
  node?: ProbeNodeSyncItem,
  runtime?: ProbeNodeStatusItem["runtime"],
  cloudflareAPIHost?: string,
): ProbeTestTarget[] {
  void runtime;
  if (!node) {
    return [];
  }
  const candidates: ProbeTestTarget[] = [];
  const seen = new Set<string>();
  const pushCandidate = (rawHost: unknown, source: string) => {
    const host = normalizeHost(rawHost);
    if (!isUsableTargetHost(host)) {
      return;
    }
    if (!isLikelyAPIDomainHost(host)) {
      return;
    }
    const key = host.toLowerCase();
    if (seen.has(key)) {
      return;
    }
    seen.add(key);
    candidates.push({
      host,
      isAPI: isLikelyAPIDomainHost(host),
      source,
    });
  };

  pushCandidate(cloudflareAPIHost, "cloudflare_business");
  pushCandidate(node.public_host, "public_host");
  pushCandidate(node.ddns, "ddns");
  pushCandidate(node.service_host, "service_host");
  return candidates;
}

function buildNodeAPIHostsFromCloudflare(records: CloudflareDDNSRecord[]): Record<number, string> {
  const bestByNodeNo: Record<number, { host: string; score: number }> = {};
  for (const item of records) {
    const nodeNo = Number(item.node_no);
    if (!Number.isFinite(nodeNo) || nodeNo <= 0) {
      continue;
    }
    const host = normalizeHost(item.record_name);
    if (!isUsableTargetHost(host)) {
      continue;
    }
    const recordClass = String(item.record_class || "").trim().toLowerCase();
    let score = 0;
    if (recordClass === "business") {
      score += 100;
    }
    if (isLikelyAPIDomainHost(host)) {
      score += 50;
    }
    const sequence = Number(item.sequence || 0);
    if (Number.isFinite(sequence) && sequence === 1) {
      score += 10;
    }

    const current = bestByNodeNo[nodeNo];
    if (!current || score > current.score) {
      bestByNodeNo[nodeNo] = { host, score };
    }
  }

  const out: Record<number, string> = {};
  for (const [key, value] of Object.entries(bestByNodeNo)) {
    const nodeNo = Number(key);
    if (!Number.isFinite(nodeNo) || nodeNo <= 0) {
      continue;
    }
    out[nodeNo] = value.host;
  }
  return out;
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

function isLikelyAPIDomainHost(host: string): boolean {
  const value = normalizeHost(host).toLowerCase();
  if (!value || !isDomainHost(value)) {
    return false;
  }
  if (value.startsWith("api.")) {
    return true;
  }
  return value.includes(".api.");
}

function isUsableTargetHost(host: string): boolean {
  const value = normalizeHost(host);
  if (!value) {
    return false;
  }
  if (isIPv4Host(value) || isIPv6Host(value)) {
    return true;
  }
  if (value.toLowerCase() === "localhost") {
    return true;
  }
  return value.includes(".");
}

function isDomainHost(host: string): boolean {
  const value = normalizeHost(host);
  if (!value) {
    return false;
  }
  if (isIPv4Host(value) || isIPv6Host(value)) {
    return false;
  }
  return value.includes(".");
}

function isIPv4Host(host: string): boolean {
  return /^(?:\d{1,3}\.){3}\d{1,3}$/.test(host);
}

function isIPv6Host(host: string): boolean {
  if (!host.includes(":")) {
    return false;
  }
  if (host.includes(".")) {
    return false;
  }
  return /^[0-9a-fA-F:]+$/.test(host);
}

function sleep(ms: number): Promise<void> {
  const safeMs = Number.isFinite(ms) ? Math.max(0, Math.trunc(ms)) : 0;
  return new Promise((resolve) => {
    window.setTimeout(() => resolve(), safeMs);
  });
}

function errorToMessage(error: unknown): string {
  if (error instanceof Error) {
    const msg = error.message.trim();
    if (msg) {
      return msg;
    }
  }
  if (typeof error === "string") {
    const msg = error.trim();
    if (msg) {
      return msg;
    }
  }
  if (error && typeof error === "object") {
    const record = error as Record<string, unknown>;
    const messageCandidates = [record.message, record.error, record.reason];
    for (const candidate of messageCandidates) {
      if (typeof candidate === "string" && candidate.trim()) {
        return candidate.trim();
      }
    }
    try {
      const serialized = JSON.stringify(record);
      if (serialized && serialized !== "{}") {
        return serialized;
      }
    } catch {
      // ignore serialization failure
    }
  }
  return "unknown error";
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
