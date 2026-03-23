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

type LinkManageSubTab = "list" | "test";
type ProbeLinkTestProtocol = "http" | "https" | "http3";
type ProbeLinkLayer = "http" | "http2" | "http3";

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

type ProbeLinkChainFormState = {
  chainID: string;
  name: string;
  userID: string;
  userPublicKey: string;
  secret: string;
  exitNodeID: string;
  cascadeNodeIDsText: string;
  listenHost: string;
  listenPort: number;
  linkLayer: ProbeLinkLayer;
  hopConfigsText: string;
  egressHost: string;
  egressPort: number;
};

const defaultInternalPort = 16031;
const defaultLinkChainListenHost = "0.0.0.0";
const defaultLinkChainListenPort = 16030;
const defaultLinkChainLayer: ProbeLinkLayer = "http";
const defaultLinkChainEgressHost = "127.0.0.1";
const defaultLinkChainEgressPort = 1080;
const linkChainCacheStorageKey = "cloudhelper_probe_link_chains_cache_v1";

export function LinkManageTab(props: LinkManageTabProps) {
  const [subTab, setSubTab] = useState<LinkManageSubTab>("list");
  const [nodes, setNodes] = useState<ProbeNodeSyncItem[]>([]);
  const [nodeRuntimes, setNodeRuntimes] = useState<Record<number, ProbeNodeStatusItem["runtime"]>>({});
  const [nodeAPIHosts, setNodeAPIHosts] = useState<Record<number, string>>({});
  const [chains, setChains] = useState<ProbeLinkChainItem[]>([]);
  const [isLoadingChains, setIsLoadingChains] = useState(false);
  const [isSavingChain, setIsSavingChain] = useState(false);
  const [deletingChainID, setDeletingChainID] = useState("");
  const [editingChainID, setEditingChainID] = useState("");
  const [chainStatus, setChainStatus] = useState("未加载链路列表");
  const [chainForm, setChainForm] = useState<ProbeLinkChainFormState>(() => createEmptyProbeLinkChainForm());
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
  const isOperatingChain = isLoadingChains || isSavingChain || deletingChainID !== "";

  useEffect(() => {
    if (!props.sessionToken.trim()) {
      stopLocalContinuousTestLoop();
      void StopProbeLinkSession();
      setNodes([]);
      setSelectedNodeID("");
      setStatus("未登录，无法加载探针列表");
      setChains([]);
      setEditingChainID("");
      setChainForm(createEmptyProbeLinkChainForm());
      setChainStatus("未登录，无法加载链路列表");
      return;
    }
    const cachedChains = readProbeLinkChainCache();
    if (cachedChains.length > 0) {
      setChains(sortProbeLinkChains(cachedChains));
      setChainStatus(`已加载本地缓存链路（${cachedChains.length} 条），正在同步最新数据...`);
    }
    void loadNodes();
    void loadChains();
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

  useEffect(() => {
    if (chainForm.exitNodeID.trim() || nodes.length === 0) {
      return;
    }
    setChainForm((prev) => ({
      ...prev,
      exitNodeID: String(nodes[0].node_no),
    }));
  }, [chainForm.exitNodeID, nodes]);

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

  async function loadChains() {
    if (!props.sessionToken.trim()) {
      setChains([]);
      setChainStatus("未登录，无法加载链路列表");
      return;
    }
    setIsLoadingChains(true);
    try {
      const items = await fetchProbeLinkChains(props.controllerBaseUrl, props.sessionToken);
      const sorted = sortProbeLinkChains(items);
      setChains(sorted);
      writeProbeLinkChainCache(sorted);
      setChainStatus(`已加载链路列表（${sorted.length} 条）`);
    } catch (error) {
      const msg = errorToMessage(error);
      setChainStatus(`加载链路列表失败：${msg}`);
    } finally {
      setIsLoadingChains(false);
    }
  }

  function resetChainForm() {
    setEditingChainID("");
    setChainForm(createEmptyProbeLinkChainForm(selectedNodeID || (nodes[0] ? String(nodes[0].node_no) : "")));
  }

  function beginEditChain(item: ProbeLinkChainItem) {
    setEditingChainID(item.chain_id);
    setChainForm({
      chainID: item.chain_id,
      name: item.name || "",
      userID: item.user_id || "",
      userPublicKey: item.user_public_key || "",
      secret: item.secret || "",
      exitNodeID: normalizeNodeIDText(item.exit_node_id || ""),
      cascadeNodeIDsText: (Array.isArray(item.cascade_node_ids) ? item.cascade_node_ids : []).join(","),
      listenHost: item.listen_host || defaultLinkChainListenHost,
      listenPort: normalizePort(item.listen_port || 0) || defaultLinkChainListenPort,
      linkLayer: normalizeProbeLinkLayer(item.link_layer),
      hopConfigsText: serializeHopConfigs(item.hop_configs),
      egressHost: item.egress_host || defaultLinkChainEgressHost,
      egressPort: normalizePort(item.egress_port || 0) || defaultLinkChainEgressPort,
    });
    setChainStatus(`正在编辑链路：${item.name || item.chain_id}`);
  }

  async function handleSaveChain() {
    if (!props.sessionToken.trim()) {
      setChainStatus("未登录，无法保存链路");
      return;
    }
    const name = chainForm.name.trim();
    const userID = chainForm.userID.trim();
    const userPublicKey = chainForm.userPublicKey.trim();
    const exitNodeID = normalizeNodeIDText(chainForm.exitNodeID);
    const listenPort = normalizePort(chainForm.listenPort);
    const egressHost = chainForm.egressHost.trim();
    const egressPort = normalizePort(chainForm.egressPort);
    if (!name) {
      setChainStatus("链路名称不能为空");
      return;
    }
    if (!userID) {
      setChainStatus("用户 ID 不能为空");
      return;
    }
    if (!userPublicKey) {
      setChainStatus("用户公钥不能为空");
      return;
    }
    if (!exitNodeID) {
      setChainStatus("请选择出口探针");
      return;
    }
    if (listenPort <= 0) {
      setChainStatus("监听端口必须在 1-65535 范围内");
      return;
    }
    if (!egressHost) {
      setChainStatus("出口地址不能为空");
      return;
    }
    if (egressPort <= 0) {
      setChainStatus("出口端口必须在 1-65535 范围内");
      return;
    }

    const cascades = parseNodeIDListInput(chainForm.cascadeNodeIDsText);
    const hopConfigsResult = parseHopConfigsInput(chainForm.hopConfigsText);
    if (hopConfigsResult.error) {
      setChainStatus(hopConfigsResult.error);
      return;
    }

    setIsSavingChain(true);
    setChainStatus(editingChainID ? "正在更新链路..." : "正在新增链路...");
    try {
      const response = await upsertProbeLinkChain(props.controllerBaseUrl, props.sessionToken, {
        chain_id: editingChainID || chainForm.chainID.trim() || undefined,
        name,
        user_id: userID,
        user_public_key: userPublicKey,
        secret: chainForm.secret.trim() || undefined,
        entry_node_id: "",
        exit_node_id: exitNodeID,
        cascade_node_ids: cascades,
        listen_host: chainForm.listenHost.trim() || defaultLinkChainListenHost,
        listen_port: listenPort,
        link_layer: normalizeProbeLinkLayer(chainForm.linkLayer),
        hop_configs: hopConfigsResult.items,
        egress_host: egressHost,
        egress_port: egressPort,
      });
      const nextItems = sortProbeLinkChains(
        response.items.length > 0
          ? response.items
          : response.item
            ? [response.item]
            : chains,
      );
      setChains(nextItems);
      writeProbeLinkChainCache(nextItems);
      if (response.item) {
        beginEditChain(response.item);
      }
      if (response.apply_ok === false && response.apply_error) {
        setChainStatus(`保存成功，但下发探针异常：${response.apply_error}`);
      } else {
        setChainStatus(editingChainID ? "链路更新成功" : "链路新增成功");
      }
      if (response.items.length === 0) {
        void loadChains();
      }
    } catch (error) {
      const msg = errorToMessage(error);
      setChainStatus(`保存链路失败：${msg}`);
    } finally {
      setIsSavingChain(false);
    }
  }

  async function handleDeleteChain(chainID: string) {
    const target = String(chainID).trim();
    if (!target) {
      setChainStatus("chain_id 不能为空");
      return;
    }
    const found = chains.find((item) => item.chain_id === target);
    const displayName = found?.name || target;
    if (!window.confirm(`确认删除链路“${displayName}”？`)) {
      return;
    }

    setDeletingChainID(target);
    setChainStatus(`正在删除链路：${displayName}`);
    try {
      const response = await deleteProbeLinkChain(props.controllerBaseUrl, props.sessionToken, target);
      const nextItems = sortProbeLinkChains(response.items);
      setChains(nextItems);
      writeProbeLinkChainCache(nextItems);
      if (editingChainID === target) {
        resetChainForm();
      }
      if (response.apply_ok === false && response.apply_error) {
        setChainStatus(`删除成功，但探针清理异常：${response.apply_error}`);
      } else {
        setChainStatus(`链路已删除：${displayName}`);
      }
    } catch (error) {
      const msg = errorToMessage(error);
      setChainStatus(`删除链路失败：${msg}`);
    } finally {
      setDeletingChainID("");
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
        <button className={`subtab-btn ${subTab === "list" ? "active" : ""}`} onClick={() => setSubTab("list")}>链路列表</button>
        <button className={`subtab-btn ${subTab === "test" ? "active" : ""}`} onClick={() => setSubTab("test")}>测试</button>
      </div>

      {subTab === "list" ? (
        <>
          <div className="identity-card">
            <div className="row">
              <label>链路ID</label>
              <input
                className="input"
                value={chainForm.chainID}
                onChange={(event) => setChainForm((prev) => ({ ...prev, chainID: event.target.value }))}
                placeholder="新建时留空自动生成"
                disabled={isOperatingChain || editingChainID.trim() !== ""}
              />
            </div>
            <div className="row">
              <label>链路名称</label>
              <input
                className="input"
                value={chainForm.name}
                onChange={(event) => setChainForm((prev) => ({ ...prev, name: event.target.value }))}
                placeholder="例如：CN-Relay-01"
                disabled={isOperatingChain}
              />
            </div>
            <div className="row">
              <label>用户ID</label>
              <input
                className="input"
                value={chainForm.userID}
                onChange={(event) => setChainForm((prev) => ({ ...prev, userID: event.target.value }))}
                placeholder="链路绑定用户ID"
                disabled={isOperatingChain}
              />
            </div>
            <div className="row">
              <label>用户公钥</label>
              <textarea
                className="input"
                style={{ minHeight: 90, resize: "vertical" }}
                value={chainForm.userPublicKey}
                onChange={(event) => setChainForm((prev) => ({ ...prev, userPublicKey: event.target.value }))}
                placeholder="粘贴用户公钥内容"
                disabled={isOperatingChain}
              />
            </div>
            <div className="row">
              <label>链路Secret</label>
              <input
                className="input"
                value={chainForm.secret}
                onChange={(event) => setChainForm((prev) => ({ ...prev, secret: event.target.value }))}
                placeholder="留空自动生成"
                disabled={isOperatingChain}
              />
            </div>
            <div className="row">
              <label>出口探针</label>
              <select
                className="input"
                value={chainForm.exitNodeID}
                onChange={(event) => setChainForm((prev) => ({ ...prev, exitNodeID: event.target.value }))}
                disabled={isOperatingChain || nodes.length === 0}
              >
                <option value="">请选择出口探针</option>
                {nodes.map((item) => (
                  <option key={item.node_no} value={String(item.node_no)}>
                    #{item.node_no} {item.node_name}
                  </option>
                ))}
              </select>
            </div>
            <div className="row">
              <label>级联探针</label>
              <input
                className="input"
                value={chainForm.cascadeNodeIDsText}
                onChange={(event) => setChainForm((prev) => ({ ...prev, cascadeNodeIDsText: event.target.value }))}
                placeholder="例如：2,3,4"
                disabled={isOperatingChain}
              />
            </div>
            <div className="row">
              <label>监听地址</label>
              <input
                className="input"
                value={chainForm.listenHost}
                onChange={(event) => setChainForm((prev) => ({ ...prev, listenHost: event.target.value }))}
                placeholder={defaultLinkChainListenHost}
                disabled={isOperatingChain}
              />
            </div>
            <div className="row">
              <label>监听端口</label>
              <input
                className="input"
                type="number"
                min={1}
                max={65535}
                value={chainForm.listenPort}
                onChange={(event) => setChainForm((prev) => ({ ...prev, listenPort: Number(event.target.value) || 0 }))}
                disabled={isOperatingChain}
              />
            </div>
            <div className="row">
              <label>链路层协议</label>
              <select
                className="input"
                value={chainForm.linkLayer}
                onChange={(event) => setChainForm((prev) => ({ ...prev, linkLayer: normalizeProbeLinkLayer(event.target.value) }))}
                disabled={isOperatingChain}
              >
                <option value="http">http</option>
                <option value="http2">http2</option>
                <option value="http3">http3</option>
              </select>
            </div>
            <div className="row">
              <label>每跳配置</label>
              <textarea
                className="input"
                style={{ minHeight: 76, resize: "vertical" }}
                value={chainForm.hopConfigsText}
                onChange={(event) => setChainForm((prev) => ({ ...prev, hopConfigsText: event.target.value }))}
                placeholder="可选，格式：node_no:端口:协议，例如 2:16030:http2,3:16031:http3"
                disabled={isOperatingChain}
              />
            </div>
            <div className="row">
              <label>出口地址</label>
              <input
                className="input"
                value={chainForm.egressHost}
                onChange={(event) => setChainForm((prev) => ({ ...prev, egressHost: event.target.value }))}
                placeholder={defaultLinkChainEgressHost}
                disabled={isOperatingChain}
              />
            </div>
            <div className="row">
              <label>出口端口</label>
              <input
                className="input"
                type="number"
                min={1}
                max={65535}
                value={chainForm.egressPort}
                onChange={(event) => setChainForm((prev) => ({ ...prev, egressPort: Number(event.target.value) || 0 }))}
                disabled={isOperatingChain}
              />
            </div>
          </div>

          <div className="content-actions">
            <button className="btn" onClick={() => void loadChains()} disabled={isOperatingChain}>
              {isLoadingChains ? "刷新中..." : "刷新列表"}
            </button>
            <button className="btn" onClick={() => void handleSaveChain()} disabled={isOperatingChain}>
              {isSavingChain ? "保存中..." : editingChainID ? "保存修改" : "新增链路"}
            </button>
            <button className="btn" onClick={resetChainForm} disabled={isOperatingChain}>
              清空表单
            </button>
          </div>

          <div className="status">{chainStatus}</div>
          <div className="status">
            当前表单路由：管理端
            {parseNodeIDListInput(chainForm.cascadeNodeIDsText).map((item) => ` -> #${item}`).join("")}
            {chainForm.exitNodeID ? ` -> #${normalizeNodeIDText(chainForm.exitNodeID)}(出口)` : " -> (未选择出口)"}
          </div>

          <div className="probe-table-wrap" style={{ marginTop: 8 }}>
            <table className="probe-table" style={{ minWidth: 1280 }}>
              <thead>
                <tr>
                  <th>链路</th>
                  <th>用户</th>
                  <th>路由</th>
                  <th>监听</th>
                  <th>协议</th>
                  <th>出口代理</th>
                  <th>更新时间</th>
                  <th style={{ width: 190 }}>操作</th>
                </tr>
              </thead>
              <tbody>
                {chains.length > 0 ? chains.map((item) => (
                  <tr key={item.chain_id}>
                    <td>
                      <div className="probe-table-name">{item.name || "-"}</div>
                      <div className="probe-table-sub">{item.chain_id}</div>
                    </td>
                    <td>
                      <div>{item.user_id || "-"}</div>
                      <div className="probe-table-sub">pubkey: {item.user_public_key ? `${item.user_public_key.slice(0, 24)}...` : "-"}</div>
                    </td>
                    <td>
                      <div>{buildChainRouteSummary(item)}</div>
                      {item.hop_configs && item.hop_configs.length > 0 ? (
                        <div className="probe-table-sub">
                          hop: {item.hop_configs.map((cfg) => `#${cfg.node_no}:${cfg.listen_port}/${normalizeProbeLinkLayer(cfg.link_layer)}`).join(" | ")}
                        </div>
                      ) : null}
                    </td>
                    <td>{item.listen_host || defaultLinkChainListenHost}:{normalizePort(item.listen_port || 0)}</td>
                    <td>{normalizeProbeLinkLayer(item.link_layer)}</td>
                    <td>{item.egress_host || "-"}:{normalizePort(item.egress_port || 0)}</td>
                    <td>{item.updated_at || item.created_at || "-"}</td>
                    <td>
                      <div className="probe-table-actions">
                        <button
                          className="btn"
                          onClick={() => beginEditChain(item)}
                          disabled={isOperatingChain}
                        >
                          编辑
                        </button>
                        <button
                          className="btn"
                          onClick={() => void handleDeleteChain(item.chain_id)}
                          disabled={isOperatingChain}
                        >
                          {deletingChainID === item.chain_id ? "删除中..." : "删除"}
                        </button>
                      </div>
                    </td>
                  </tr>
                )) : (
                  <tr>
                    <td colSpan={8}>
                      <div className="empty">暂无链路</div>
                    </td>
                  </tr>
                )}
              </tbody>
            </table>
          </div>
        </>
      ) : null}

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

function createEmptyProbeLinkChainForm(defaultExitNodeID = ""): ProbeLinkChainFormState {
  return {
    chainID: "",
    name: "",
    userID: "",
    userPublicKey: "",
    secret: "",
    exitNodeID: normalizeNodeIDText(defaultExitNodeID),
    cascadeNodeIDsText: "",
    listenHost: defaultLinkChainListenHost,
    listenPort: defaultLinkChainListenPort,
    linkLayer: defaultLinkChainLayer,
    hopConfigsText: "",
    egressHost: defaultLinkChainEgressHost,
    egressPort: defaultLinkChainEgressPort,
  };
}

function sortProbeLinkChains(items: ProbeLinkChainItem[]): ProbeLinkChainItem[] {
  if (!Array.isArray(items) || items.length === 0) {
    return [];
  }
  const out = [...items];
  out.sort((left, right) => {
    const leftKey = String(left.updated_at || left.created_at || "").trim();
    const rightKey = String(right.updated_at || right.created_at || "").trim();
    if (leftKey === rightKey) {
      return String(left.chain_id || "").localeCompare(String(right.chain_id || ""));
    }
    return rightKey.localeCompare(leftKey);
  });
  return out;
}

function readProbeLinkChainCache(): ProbeLinkChainItem[] {
  try {
    const raw = window.localStorage.getItem(linkChainCacheStorageKey);
    if (!raw) {
      return [];
    }
    const parsed = JSON.parse(raw);
    if (!Array.isArray(parsed)) {
      return [];
    }
    const items: ProbeLinkChainItem[] = [];
    for (const item of parsed) {
      if (!item || typeof item !== "object") {
        continue;
      }
      const record = item as ProbeLinkChainItem;
      if (!String(record.chain_id || "").trim()) {
        continue;
      }
      items.push(record);
    }
    return items;
  } catch {
    return [];
  }
}

function writeProbeLinkChainCache(items: ProbeLinkChainItem[]): void {
  try {
    window.localStorage.setItem(linkChainCacheStorageKey, JSON.stringify(sortProbeLinkChains(items)));
  } catch {
    // ignore local cache write failure
  }
}

function normalizeProbeLinkLayer(raw: unknown): ProbeLinkLayer {
  const value = String(raw || "").trim().toLowerCase();
  if (value === "http2" || value === "h2") {
    return "http2";
  }
  if (value === "http3" || value === "h3") {
    return "http3";
  }
  return "http";
}

function parseProbeLinkLayerStrict(raw: unknown): { ok: boolean; value: ProbeLinkLayer } {
  const value = String(raw || "").trim().toLowerCase();
  if (value === "http") {
    return { ok: true, value: "http" };
  }
  if (value === "http2" || value === "h2") {
    return { ok: true, value: "http2" };
  }
  if (value === "http3" || value === "h3") {
    return { ok: true, value: "http3" };
  }
  return { ok: false, value: defaultLinkChainLayer };
}

function normalizeNodeIDText(raw: unknown): string {
  const value = String(raw ?? "").trim();
  if (!value) {
    return "";
  }
  const lower = value.toLowerCase();
  if (lower.startsWith("node-") || lower.startsWith("node_")) {
    const suffix = lower.replace(/^node[-_]/, "").trim();
    if (/^\d+$/.test(suffix)) {
      const n = Number(suffix);
      if (Number.isFinite(n) && n > 0) {
        return String(Math.trunc(n));
      }
    }
    return suffix;
  }
  if (/^\d+$/.test(value)) {
    const n = Number(value);
    if (Number.isFinite(n) && n > 0) {
      return String(Math.trunc(n));
    }
  }
  return value;
}

function parseNodeIDListInput(raw: string): string[] {
  const values = String(raw || "")
    .split(/[\s,;|]+/)
    .map((item) => normalizeNodeIDText(item))
    .filter((item) => item !== "");
  if (values.length === 0) {
    return [];
  }
  const out: string[] = [];
  const seen = new Set<string>();
  for (const item of values) {
    if (seen.has(item)) {
      continue;
    }
    seen.add(item);
    out.push(item);
  }
  return out;
}

function parseHopConfigsInput(raw: string): {
  items: Array<{ node_no: number; listen_port?: number; link_layer?: ProbeLinkLayer }>;
  error: string;
} {
  const text = String(raw || "").trim();
  if (!text) {
    return { items: [], error: "" };
  }
  const segments = text
    .split(/[\n,;]+/)
    .map((item) => item.trim())
    .filter((item) => item !== "");
  if (segments.length === 0) {
    return { items: [], error: "" };
  }
  const out: Array<{ node_no: number; listen_port?: number; link_layer?: ProbeLinkLayer }> = [];
  const seenNodeNo = new Set<number>();
  for (const segment of segments) {
    const parts = segment.split(":").map((item) => item.trim()).filter((item) => item !== "");
    if (parts.length < 2 || parts.length > 3) {
      return {
        items: [],
        error: `每跳配置格式错误：${segment}（正确格式：node_no:端口:协议）`,
      };
    }
    const nodeNo = Number(parts[0]);
    if (!Number.isFinite(nodeNo) || nodeNo <= 0) {
      return {
        items: [],
        error: `每跳配置中的 node_no 非法：${segment}`,
      };
    }
    const listenPort = normalizePort(Number(parts[1]));
    if (listenPort <= 0) {
      return {
        items: [],
        error: `每跳配置中的端口非法：${segment}`,
      };
    }
    let layer = defaultLinkChainLayer;
    if (parts.length >= 3) {
      const parsedLayer = parseProbeLinkLayerStrict(parts[2]);
      if (!parsedLayer.ok) {
        return {
          items: [],
          error: `每跳配置中的协议非法：${segment}（仅支持 http/http2/http3）`,
        };
      }
      layer = parsedLayer.value;
    }
    const normalizedNodeNo = Math.trunc(nodeNo);
    if (seenNodeNo.has(normalizedNodeNo)) {
      continue;
    }
    seenNodeNo.add(normalizedNodeNo);
    out.push({
      node_no: normalizedNodeNo,
      listen_port: listenPort,
      link_layer: layer,
    });
  }
  return { items: out, error: "" };
}

function serializeHopConfigs(
  values?: Array<{ node_no: number; listen_port?: number; link_layer?: "http" | "http2" | "http3" | "" }>,
): string {
  if (!Array.isArray(values) || values.length === 0) {
    return "";
  }
  const parts: string[] = [];
  for (const item of values) {
    const nodeNo = Number(item.node_no || 0);
    const port = normalizePort(Number(item.listen_port || 0));
    if (!Number.isFinite(nodeNo) || nodeNo <= 0 || port <= 0) {
      continue;
    }
    parts.push(`${Math.trunc(nodeNo)}:${port}:${normalizeProbeLinkLayer(item.link_layer)}`);
  }
  return parts.join(",");
}

function buildChainRouteSummary(item: ProbeLinkChainItem): string {
  const route = ["管理端"];
  const cascades = Array.isArray(item.cascade_node_ids) ? item.cascade_node_ids : [];
  for (const nodeID of cascades) {
    const normalized = normalizeNodeIDText(nodeID);
    if (!normalized) {
      continue;
    }
    route.push(`#${normalized}`);
  }
  const exitNodeID = normalizeNodeIDText(item.exit_node_id || "");
  if (exitNodeID) {
    route.push(`#${exitNodeID}(出口)`);
  } else {
    route.push("(未配置出口)");
  }
  return route.join(" -> ");
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
