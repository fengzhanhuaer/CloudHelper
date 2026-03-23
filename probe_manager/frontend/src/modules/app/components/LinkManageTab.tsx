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
  fetchProbeLinkUserPublicKey,
  fetchProbeLinkUsers,
  fetchProbeNodeStatus,
  fetchProbeNodes,
  startProbeLinkTestOnController,
  stopProbeLinkTestOnController,
  upsertProbeLinkChain,
  type ProbeLinkChainItem,
  type ProbeLinkUserItem,
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

type ProbeLinkHopFormItem = {
  nodeNo: number;
  servicePort: number;
  externalPort: number;
  linkLayer: ProbeLinkLayer;
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
  hopConfigs: ProbeLinkHopFormItem[];
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
  const [chainUsers, setChainUsers] = useState<ProbeLinkUserItem[]>([]);
  const [isLoadingChainUsers, setIsLoadingChainUsers] = useState(false);
  const [loadingPublicKeyUser, setLoadingPublicKeyUser] = useState("");
  const [chainUserPublicKeys, setChainUserPublicKeys] = useState<Record<string, string>>({});
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
  const loadChainUserPublicKeySeqRef = useRef(0);
  const isOperatingChain = isLoadingChains || isSavingChain || deletingChainID !== "";

  useEffect(() => {
    if (!props.sessionToken.trim()) {
      stopLocalContinuousTestLoop();
      void StopProbeLinkSession();
      setNodes([]);
      setSelectedNodeID("");
      setStatus("未登录，无法加载探针列表");
      setChains([]);
      setChainUsers([]);
      setChainUserPublicKeys({});
      setLoadingPublicKeyUser("");
      loadChainUserPublicKeySeqRef.current += 1;
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
    void loadChainUsers();
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
  const chainUserOptions = useMemo(() => {
    const out: ProbeLinkUserItem[] = [];
    const seen = new Set<string>();
    for (const item of chainUsers) {
      const username = normalizeChainUsername(item.username);
      if (!username || seen.has(username)) {
        continue;
      }
      seen.add(username);
      out.push({
        username,
        user_role: item.user_role || "",
        cert_type: item.cert_type || "",
      });
    }
    const current = normalizeChainUsername(chainForm.userID);
    if (current && !seen.has(current)) {
      out.unshift({
        username: current,
        user_role: "legacy",
        cert_type: "legacy",
      });
    }
    return out;
  }, [chainForm.userID, chainUsers]);
  const chainRouteNodeNos = useMemo(
    () => buildProbeChainRouteNodeNos(chainForm.cascadeNodeIDsText, chainForm.exitNodeID),
    [chainForm.cascadeNodeIDsText, chainForm.exitNodeID],
  );

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

  useEffect(() => {
    setChainForm((prev) => syncProbeLinkHopConfigsWithRoute(prev));
  }, [chainForm.cascadeNodeIDsText, chainForm.exitNodeID]);

  function updateHopConfig(nodeNo: number, patch: Partial<ProbeLinkHopFormItem>) {
    if (!Number.isFinite(nodeNo) || nodeNo <= 0) {
      return;
    }
    const safeNodeNo = Math.trunc(nodeNo);
    setChainForm((prev) => {
      const current = normalizeProbeLinkHopFormItems(prev.hopConfigs);
      const index = current.findIndex((item) => item.nodeNo === safeNodeNo);
      const existing: ProbeLinkHopFormItem = index >= 0
        ? current[index]
        : {
          nodeNo: safeNodeNo,
          servicePort: prev.listenPort,
          externalPort: 0,
          linkLayer: prev.linkLayer,
        };
      const nextItem: ProbeLinkHopFormItem = {
        nodeNo: safeNodeNo,
        servicePort: patch.servicePort === undefined ? existing.servicePort : normalizePort(patch.servicePort),
        externalPort: patch.externalPort === undefined ? existing.externalPort : normalizePort(patch.externalPort),
        linkLayer: patch.linkLayer === undefined ? existing.linkLayer : normalizeProbeLinkLayer(patch.linkLayer),
      };
      const next = [...current];
      if (index >= 0) {
        next[index] = nextItem;
      } else {
        next.push(nextItem);
      }
      next.sort((left, right) => left.nodeNo - right.nodeNo);
      return {
        ...prev,
        hopConfigs: next,
      };
    });
  }

  async function loadChainUsers() {
    if (!props.sessionToken.trim()) {
      setChainUsers([]);
      return;
    }
    setIsLoadingChainUsers(true);
    try {
      const users = await fetchProbeLinkUsers(props.controllerBaseUrl, props.sessionToken);
      const normalized = normalizeProbeLinkUsers(users);
      setChainUsers(normalized);

      const current = normalizeChainUsername(chainForm.userID);
      const existsCurrent = current && normalized.some((item) => normalizeChainUsername(item.username) === current);
      const nextUser = existsCurrent ? current : (normalized[0] ? normalizeChainUsername(normalized[0].username) : "");
      if (!nextUser) {
        setChainForm((prev) => ({
          ...prev,
          userID: "",
          userPublicKey: "",
        }));
        return;
      }

      setChainForm((prev) => ({
        ...prev,
        userID: nextUser,
        userPublicKey: chainUserPublicKeys[nextUser] || (existsCurrent ? prev.userPublicKey : ""),
      }));
      if (!chainUserPublicKeys[nextUser]) {
        void loadChainUserPublicKey(nextUser, { silentStatus: true });
      }
    } catch (error) {
      const msg = errorToMessage(error);
      setChainStatus(`拉取用户列表失败：${msg}`);
    } finally {
      setIsLoadingChainUsers(false);
    }
  }

  async function loadChainUserPublicKey(
    userIDRaw: string,
    options?: { silentStatus?: boolean },
  ) {
    const userID = normalizeChainUsername(userIDRaw);
    if (!userID || !props.sessionToken.trim()) {
      return;
    }
    if (chainUserPublicKeys[userID]) {
      setChainForm((prev) => {
        if (normalizeChainUsername(prev.userID) !== userID) {
          return prev;
        }
        return {
          ...prev,
          userPublicKey: chainUserPublicKeys[userID],
        };
      });
      return;
    }

    const requestSeq = loadChainUserPublicKeySeqRef.current + 1;
    loadChainUserPublicKeySeqRef.current = requestSeq;
    setLoadingPublicKeyUser(userID);
    if (!options?.silentStatus) {
      setChainStatus(`正在从服务器拉取用户 ${userID} 的公钥...`);
    }

    try {
      const payload = await fetchProbeLinkUserPublicKey(props.controllerBaseUrl, props.sessionToken, userID);
      if (requestSeq !== loadChainUserPublicKeySeqRef.current) {
        return;
      }
      const resolvedUserID = normalizeChainUsername(payload.username) || userID;
      const publicKey = String(payload.public_key || "").trim();
      if (!publicKey) {
        throw new Error("服务器返回空公钥");
      }
      setChainUserPublicKeys((prev) => ({
        ...prev,
        [userID]: publicKey,
        [resolvedUserID]: publicKey,
      }));
      setChainForm((prev) => {
        const current = normalizeChainUsername(prev.userID);
        if (current !== userID && current !== resolvedUserID) {
          return prev;
        }
        return {
          ...prev,
          userID: resolvedUserID,
          userPublicKey: publicKey,
        };
      });
      if (!options?.silentStatus) {
        setChainStatus(`已获取用户 ${resolvedUserID} 的公钥`);
      }
    } catch (error) {
      if (requestSeq !== loadChainUserPublicKeySeqRef.current) {
        return;
      }
      const msg = errorToMessage(error);
      if (!options?.silentStatus) {
        setChainStatus(`拉取用户公钥失败：${msg}`);
      }
    } finally {
      if (requestSeq === loadChainUserPublicKeySeqRef.current) {
        setLoadingPublicKeyUser("");
      }
    }
  }

  async function handleChainUserChange(nextUserIDRaw: string) {
    const nextUserID = normalizeChainUsername(nextUserIDRaw);
    setChainForm((prev) => ({
      ...prev,
      userID: nextUserID,
      userPublicKey: nextUserID ? (chainUserPublicKeys[nextUserID] || "") : "",
    }));
    if (!nextUserID) {
      return;
    }
    if (chainUserPublicKeys[nextUserID]) {
      setChainStatus(`已选择用户：${nextUserID}`);
      return;
    }
    await loadChainUserPublicKey(nextUserID);
  }

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
    const defaultUserID = chainUsers[0] ? normalizeChainUsername(chainUsers[0].username) : "";
    setChainForm(createEmptyProbeLinkChainForm(
      selectedNodeID || (nodes[0] ? String(nodes[0].node_no) : ""),
      defaultUserID,
      chainUserPublicKeys[defaultUserID] || "",
    ));
    if (defaultUserID && !chainUserPublicKeys[defaultUserID]) {
      void loadChainUserPublicKey(defaultUserID, { silentStatus: true });
    }
  }

  function beginEditChain(item: ProbeLinkChainItem) {
    setEditingChainID(item.chain_id);
    const normalizedUserID = normalizeChainUsername(item.user_id || "");
    setChainForm({
      chainID: item.chain_id,
      name: item.name || "",
      userID: normalizedUserID,
      userPublicKey: chainUserPublicKeys[normalizedUserID] || item.user_public_key || "",
      secret: item.secret || "",
      exitNodeID: normalizeNodeIDText(item.exit_node_id || ""),
      cascadeNodeIDsText: (Array.isArray(item.cascade_node_ids) ? item.cascade_node_ids : []).join(","),
      listenHost: item.listen_host || defaultLinkChainListenHost,
      listenPort: normalizePort(item.listen_port || 0) || defaultLinkChainListenPort,
      linkLayer: normalizeProbeLinkLayer(item.link_layer),
      hopConfigs: normalizeProbeLinkHopFormItemsFromChain(
        item.hop_configs,
        normalizePort(item.listen_port || 0) || defaultLinkChainListenPort,
        normalizeProbeLinkLayer(item.link_layer),
      ),
    });
    if (normalizedUserID && !chainUserPublicKeys[normalizedUserID]) {
      void loadChainUserPublicKey(normalizedUserID, { silentStatus: true });
    }
    setChainStatus(`正在编辑链路：${item.name || item.chain_id}`);
  }

  async function handleSaveChain() {
    if (!props.sessionToken.trim()) {
      setChainStatus("未登录，无法保存链路");
      return;
    }
    const name = chainForm.name.trim();
    const userID = normalizeChainUsername(chainForm.userID);
    const userPublicKey = chainForm.userPublicKey.trim();
    const exitNodeID = normalizeNodeIDText(chainForm.exitNodeID);
    const listenPort = normalizePort(chainForm.listenPort);
    if (!name) {
      setChainStatus("链路名称不能为空");
      return;
    }
    if (!userID) {
      setChainStatus("用户 ID 不能为空");
      return;
    }
    if (loadingPublicKeyUser && normalizeChainUsername(loadingPublicKeyUser) === userID) {
      setChainStatus(`用户 ${userID} 的公钥拉取中，请稍后再试`);
      return;
    }
    if (!userPublicKey) {
      setChainStatus(`用户 ${userID} 的公钥未就绪，正在重新拉取...`);
      void loadChainUserPublicKey(userID);
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
    const cascades = parseNodeIDListInput(chainForm.cascadeNodeIDsText);
    const hopConfigsResult = buildProbeLinkHopConfigsPayload(chainForm);
    if (hopConfigsResult.error) {
      setChainStatus(hopConfigsResult.error);
      return;
    }

    setIsSavingChain(true);
    setChainStatus(editingChainID ? "正在更新链路..." : "正在新增链路...");
    try {
      const response = await upsertProbeLinkChain(props.controllerBaseUrl, props.sessionToken, {
        chain_id: editingChainID || undefined,
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
        egress_host: defaultLinkChainEgressHost,
        egress_port: defaultLinkChainEgressPort,
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
                value={editingChainID ? chainForm.chainID : ""}
                placeholder="自动生成（保存后创建）"
                readOnly
                disabled
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
              <select
                className="input"
                value={chainForm.userID}
                onChange={(event) => { void handleChainUserChange(event.target.value); }}
                disabled={isOperatingChain || isLoadingChainUsers}
              >
                <option value="">{isLoadingChainUsers ? "用户列表加载中..." : "请选择用户"}</option>
                {chainUserOptions.map((item) => (
                  <option key={item.username} value={item.username}>
                    {item.username}
                    {item.user_role ? ` (${item.user_role})` : ""}
                  </option>
                ))}
              </select>
            </div>
            <div className="row">
              <label>用户公钥</label>
              <textarea
                className="input"
                style={{ minHeight: 90, resize: "vertical" }}
                value={chainForm.userPublicKey}
                placeholder={loadingPublicKeyUser ? `正在拉取 ${loadingPublicKeyUser} 的公钥...` : "自动从服务器拉取"}
                readOnly
                disabled
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
              <label>默认服务端口</label>
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
              <label>默认链路协议</label>
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
              <label>逐探针链路配置</label>
              <div style={{ width: "100%" }}>
                {chainRouteNodeNos.length > 0 ? (
                  <div className="probe-table-wrap" style={{ marginTop: 4 }}>
                    <table className="probe-table" style={{ minWidth: 720 }}>
                      <thead>
                        <tr>
                          <th>探针</th>
                          <th>服务端口</th>
                          <th>外部端口</th>
                          <th>链路协议</th>
                        </tr>
                      </thead>
                      <tbody>
                        {chainRouteNodeNos.map((nodeNo) => {
                          const cfg = findProbeLinkHopFormItem(chainForm.hopConfigs, nodeNo);
                          const node = nodes.find((item) => item.node_no === nodeNo);
                          return (
                            <tr key={`hop-${nodeNo}`}>
                              <td>#{nodeNo} {node?.node_name || ""}</td>
                              <td>
                                <input
                                  className="input"
                                  type="number"
                                  min={1}
                                  max={65535}
                                  value={cfg.servicePort > 0 ? cfg.servicePort : ""}
                                  placeholder={String(chainForm.listenPort || defaultLinkChainListenPort)}
                                  onChange={(event) => updateHopConfig(nodeNo, { servicePort: Number(event.target.value) || 0 })}
                                  disabled={isOperatingChain}
                                />
                              </td>
                              <td>
                                <input
                                  className="input"
                                  type="number"
                                  min={1}
                                  max={65535}
                                  value={cfg.externalPort > 0 ? cfg.externalPort : ""}
                                  placeholder="默认使用探针公网端口"
                                  onChange={(event) => updateHopConfig(nodeNo, { externalPort: Number(event.target.value) || 0 })}
                                  disabled={isOperatingChain}
                                />
                              </td>
                              <td>
                                <select
                                  className="input"
                                  value={cfg.linkLayer}
                                  onChange={(event) => updateHopConfig(nodeNo, { linkLayer: event.target.value as ProbeLinkLayer })}
                                  disabled={isOperatingChain}
                                >
                                  <option value="http">http</option>
                                  <option value="http2">http2</option>
                                  <option value="http3">http3</option>
                                </select>
                              </td>
                            </tr>
                          );
                        })}
                      </tbody>
                    </table>
                  </div>
                ) : (
                  <div className="status">请先设置级联探针/出口探针，再逐个配置探针端口与协议。</div>
                )}
              </div>
            </div>
          </div>

          <div className="content-actions">
            <button
              className="btn"
              onClick={() => {
                void loadChainUsers();
                void loadChains();
              }}
              disabled={isOperatingChain || isLoadingChainUsers}
            >
              {isLoadingChains || isLoadingChainUsers ? "刷新中..." : "刷新列表"}
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
                          hop: {item.hop_configs.map((cfg) => {
                            const servicePort = normalizePort(Number(cfg.service_port || 0));
                            const externalPort = normalizePort(Number(cfg.external_port || cfg.listen_port || 0));
                            return `#${cfg.node_no}(svc:${servicePort || "-"}, ext:${externalPort || "-"}, ${normalizeProbeLinkLayer(cfg.link_layer)})`;
                          }).join(" | ")}
                        </div>
                      ) : null}
                    </td>
                    <td>{item.listen_host || defaultLinkChainListenHost}:{normalizePort(item.listen_port || 0)}</td>
                    <td>{normalizeProbeLinkLayer(item.link_layer)}</td>
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
                    <td colSpan={7}>
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

function createEmptyProbeLinkChainForm(
  defaultExitNodeID = "",
  defaultUserID = "",
  defaultUserPublicKey = "",
): ProbeLinkChainFormState {
  return {
    chainID: "",
    name: "",
    userID: normalizeChainUsername(defaultUserID),
    userPublicKey: String(defaultUserPublicKey || "").trim(),
    secret: "",
    exitNodeID: normalizeNodeIDText(defaultExitNodeID),
    cascadeNodeIDsText: "",
    listenHost: defaultLinkChainListenHost,
    listenPort: defaultLinkChainListenPort,
    linkLayer: defaultLinkChainLayer,
    hopConfigs: [],
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

function normalizeChainUsername(raw: unknown): string {
  return String(raw ?? "").trim();
}

function normalizeProbeLinkUsers(items: ProbeLinkUserItem[]): ProbeLinkUserItem[] {
  if (!Array.isArray(items) || items.length === 0) {
    return [];
  }
  const out: ProbeLinkUserItem[] = [];
  const seen = new Set<string>();
  for (const item of items) {
    const username = normalizeChainUsername(item.username);
    if (!username || seen.has(username)) {
      continue;
    }
    seen.add(username);
    out.push({
      username,
      user_role: String(item.user_role || "").trim(),
      cert_type: String(item.cert_type || "").trim(),
    });
  }
  return out;
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

function buildProbeChainRouteNodeNos(cascadeNodeIDsText: string, exitNodeIDRaw: string): number[] {
  const routeNodeIDs = [
    ...parseNodeIDListInput(cascadeNodeIDsText),
    normalizeNodeIDText(exitNodeIDRaw),
  ];
  const out: number[] = [];
  const seen = new Set<number>();
  for (const nodeID of routeNodeIDs) {
    const nodeNo = Number(nodeID);
    if (!Number.isFinite(nodeNo) || nodeNo <= 0) {
      continue;
    }
    const normalized = Math.trunc(nodeNo);
    if (seen.has(normalized)) {
      continue;
    }
    seen.add(normalized);
    out.push(normalized);
  }
  return out;
}

function normalizeProbeLinkHopFormItems(values?: ProbeLinkHopFormItem[]): ProbeLinkHopFormItem[] {
  if (!Array.isArray(values) || values.length === 0) {
    return [];
  }
  const out: ProbeLinkHopFormItem[] = [];
  const seen = new Set<number>();
  for (const item of values) {
    const nodeNo = Math.trunc(Number(item.nodeNo || 0));
    if (!Number.isFinite(nodeNo) || nodeNo <= 0 || seen.has(nodeNo)) {
      continue;
    }
    seen.add(nodeNo);
    out.push({
      nodeNo,
      servicePort: normalizePort(Number(item.servicePort || 0)),
      externalPort: normalizePort(Number(item.externalPort || 0)),
      linkLayer: normalizeProbeLinkLayer(item.linkLayer),
    });
  }
  out.sort((left, right) => left.nodeNo - right.nodeNo);
  return out;
}

function normalizeProbeLinkHopFormItemsFromChain(
  values: Array<{ node_no: number; service_port?: number; external_port?: number; listen_port?: number; link_layer?: "http" | "http2" | "http3" | "" }> | undefined,
  defaultServicePort: number,
  defaultLinkLayer: ProbeLinkLayer,
): ProbeLinkHopFormItem[] {
  if (!Array.isArray(values) || values.length === 0) {
    return [];
  }
  const safeDefaultServicePort = normalizePort(defaultServicePort) || defaultLinkChainListenPort;
  const safeDefaultLayer = normalizeProbeLinkLayer(defaultLinkLayer);
  const out: ProbeLinkHopFormItem[] = [];
  const seen = new Set<number>();
  for (const item of values) {
    const nodeNo = Math.trunc(Number(item.node_no || 0));
    if (!Number.isFinite(nodeNo) || nodeNo <= 0 || seen.has(nodeNo)) {
      continue;
    }
    seen.add(nodeNo);
    const servicePort = normalizePort(Number(item.service_port || 0)) || safeDefaultServicePort;
    const externalPort = normalizePort(Number(item.external_port || item.listen_port || 0));
    out.push({
      nodeNo,
      servicePort,
      externalPort,
      linkLayer: normalizeProbeLinkLayer(item.link_layer || safeDefaultLayer),
    });
  }
  out.sort((left, right) => left.nodeNo - right.nodeNo);
  return out;
}

function findProbeLinkHopFormItem(values: ProbeLinkHopFormItem[], nodeNo: number): ProbeLinkHopFormItem {
  const targetNodeNo = Math.trunc(Number(nodeNo || 0));
  if (!Number.isFinite(targetNodeNo) || targetNodeNo <= 0) {
    return {
      nodeNo: 0,
      servicePort: 0,
      externalPort: 0,
      linkLayer: defaultLinkChainLayer,
    };
  }
  for (const item of normalizeProbeLinkHopFormItems(values)) {
    if (item.nodeNo === targetNodeNo) {
      return item;
    }
  }
  return {
    nodeNo: targetNodeNo,
    servicePort: 0,
    externalPort: 0,
    linkLayer: defaultLinkChainLayer,
  };
}

function areProbeLinkHopFormItemsEqual(left: ProbeLinkHopFormItem[], right: ProbeLinkHopFormItem[]): boolean {
  if (left.length !== right.length) {
    return false;
  }
  for (let i = 0; i < left.length; i += 1) {
    const l = left[i];
    const r = right[i];
    if (l.nodeNo !== r.nodeNo || l.servicePort !== r.servicePort || l.externalPort !== r.externalPort || l.linkLayer !== r.linkLayer) {
      return false;
    }
  }
  return true;
}

function syncProbeLinkHopConfigsWithRoute(form: ProbeLinkChainFormState): ProbeLinkChainFormState {
  const routeNodeNos = buildProbeChainRouteNodeNos(form.cascadeNodeIDsText, form.exitNodeID);
  const current = normalizeProbeLinkHopFormItems(form.hopConfigs);
  const map = new Map<number, ProbeLinkHopFormItem>();
  for (const item of current) {
    map.set(item.nodeNo, item);
  }
  const safeDefaultServicePort = normalizePort(form.listenPort) || defaultLinkChainListenPort;
  const safeDefaultLayer = normalizeProbeLinkLayer(form.linkLayer);
  const next = routeNodeNos.map((nodeNo) => {
    const found = map.get(nodeNo);
    if (found) {
      return found;
    }
    return {
      nodeNo,
      servicePort: safeDefaultServicePort,
      externalPort: 0,
      linkLayer: safeDefaultLayer,
    };
  });
  if (areProbeLinkHopFormItemsEqual(current, next)) {
    return form;
  }
  return {
    ...form,
    hopConfigs: next,
  };
}

function buildProbeLinkHopConfigsPayload(form: ProbeLinkChainFormState): {
  items: Array<{ node_no: number; service_port?: number; external_port?: number; link_layer?: ProbeLinkLayer }>;
  error: string;
} {
  const routeNodeNos = buildProbeChainRouteNodeNos(form.cascadeNodeIDsText, form.exitNodeID);
  if (routeNodeNos.length === 0) {
    return { items: [], error: "" };
  }
  const hopMap = new Map<number, ProbeLinkHopFormItem>();
  for (const item of normalizeProbeLinkHopFormItems(form.hopConfigs)) {
    hopMap.set(item.nodeNo, item);
  }
  const fallbackServicePort = normalizePort(form.listenPort);
  const fallbackLayer = normalizeProbeLinkLayer(form.linkLayer);
  const items: Array<{ node_no: number; service_port?: number; external_port?: number; link_layer?: ProbeLinkLayer }> = [];
  for (const nodeNo of routeNodeNos) {
    const cfg = hopMap.get(nodeNo);
    const servicePort = normalizePort(cfg?.servicePort || fallbackServicePort);
    const externalPort = normalizePort(cfg?.externalPort || 0);
    const layer = normalizeProbeLinkLayer(cfg?.linkLayer || fallbackLayer);
    if (servicePort <= 0) {
      return { items: [], error: `探针 #${nodeNo} 的服务端口必须在 1-65535 范围内` };
    }
    items.push({
      node_no: nodeNo,
      service_port: servicePort,
      external_port: externalPort > 0 ? externalPort : undefined,
      link_layer: layer,
    });
  }
  return { items, error: "" };
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
