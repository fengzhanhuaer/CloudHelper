import { useEffect, useMemo, useRef, useState } from "react";
import {
  ForceRefreshNetworkAssistantNodes,
  GetProbeLinkChainsCache,
  PingProbeLinkSession,
  PingProbeChain,
  StartProbeLinkSession,
  StopProbeLinkSession,
} from "../../../../wailsjs/go/main/App";
import {
  deleteProbeLinkChain,
  fetchCloudflareDDNSRecords,
  fetchProbeLinkUserPublicKey,
  fetchProbeLinkUsers,
  fetchProbeNodeStatus,
  fetchProbeNodes,
  forceRefreshProbeDNSCache,
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
  initialSubTab?: LinkManageSubTab;
};

type LinkManageSubTab = "list" | "forward" | "test";
type ProbeLinkTestProtocol = "http" | "https" | "http3";
type ProbeLinkLayer = "http" | "http2" | "http3";
type ProbeLinkDialMode = "forward" | "reverse";
type ProbeLinkPFNetwork = "tcp" | "udp" | "both";

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
  listenHost: string;
  servicePort: number;
  externalPort: number;
  linkLayer: ProbeLinkLayer;
  dialMode: ProbeLinkDialMode;
};

type ProbeLinkPFEntrySide = "chain_entry" | "chain_exit";

type ProbeLinkPortForwardFormItem = {
  id: string;
  name: string;
  entrySide: ProbeLinkPFEntrySide;
  listenHost: string;
  listenPort: number;
  targetHost: string;
  targetPort: number;
  network: ProbeLinkPFNetwork;
  enabled: boolean;
};

type ProbeLinkChainFormState = {
  chainID: string;
  name: string;
  userID: string;
  userPublicKey: string;
  secret: string;
  hopConfigs: ProbeLinkHopFormItem[];
  portForwards: ProbeLinkPortForwardFormItem[];
};

const defaultInternalPort = 16031;
const defaultLinkChainListenHost = "0.0.0.0";
const defaultLinkChainListenPort = 16030;
const defaultLinkChainLayer: ProbeLinkLayer = "http";
const defaultLinkChainDialMode: ProbeLinkDialMode = "forward";
const defaultLinkChainEgressHost = "127.0.0.1";
const defaultLinkChainEgressPort = 1080;
const defaultPortForwardListenHost = "0.0.0.0";
const defaultPortForwardNetwork: ProbeLinkPFNetwork = "tcp";
export function LinkManageTab(props: LinkManageTabProps) {
  const isForwardOnlyTab = props.initialSubTab === "forward";
  const [subTab, setSubTab] = useState<LinkManageSubTab>(props.initialSubTab || "list");
  const [nodes, setNodes] = useState<ProbeNodeSyncItem[]>([]);
  const [nodeRuntimes, setNodeRuntimes] = useState<Record<number, ProbeNodeStatusItem["runtime"]>>({});
  const [nodeAPIHosts, setNodeAPIHosts] = useState<Record<number, string>>({});
  const [chains, setChains] = useState<ProbeLinkChainItem[]>([]);
  const [isLoadingChains, setIsLoadingChains] = useState(false);
  const [isSavingChain, setIsSavingChain] = useState(false);
  const [deletingChainID, setDeletingChainID] = useState("");
  const [editingChainID, setEditingChainID] = useState("");
  const [chainStatus, setChainStatus] = useState("未加载链路列表");
  const [isRefreshingDNSCache, setIsRefreshingDNSCache] = useState(false);
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
  const [status, setStatus] = useState("未开始探针延迟检测");
  const [latencyMS, setLatencyMS] = useState<number | null>(null);
  const [resultSummary, setResultSummary] = useState("");
  const continuousTestSeqRef = useRef(0);
  const continuousTestingRef = useRef(false);
  const loadChainUserPublicKeySeqRef = useRef(0);
  const isOperatingChain = isLoadingChains || isSavingChain || deletingChainID !== "" || isRefreshingDNSCache;

  type ChainPingState = { ok: boolean | null; durationMS: number | null; message: string };
  const [chainPingStates, setChainPingStates] = useState<Record<string, ChainPingState>>({});
  const [chainPingingID, setChainPingingID] = useState("");

  useEffect(() => {
    if (!props.initialSubTab) {
      return;
    }
    setSubTab(props.initialSubTab);
  }, [props.initialSubTab]);

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
    void loadChainsFromBackendCache({
      emptyStatus: "未找到本地链路缓存，请点击“从主控获取链路”",
    });
  }, [props.controllerBaseUrl, props.sessionToken]);

  const handlePingChain = async (chainID: string) => {
    if (chainPingingID) return;
    setChainPingingID(chainID);
    setChainPingStates((prev) => ({ ...prev, [chainID]: { ok: null, durationMS: null, message: "测试中..." } }));
    try {
      const result = await PingProbeChain(chainID);
      setChainPingStates((prev) => ({
        ...prev,
        [chainID]: { ok: result.ok, durationMS: result.duration_ms ?? null, message: result.message ?? (result.ok ? "成功" : "失败") },
      }));
    } catch (err: unknown) {
      const msg = err instanceof Error ? err.message : String(err);
      setChainPingStates((prev) => ({ ...prev, [chainID]: { ok: false, durationMS: null, message: msg } }));
    } finally {
      setChainPingingID("");
    }
  };


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

  useEffect(() => {
    if (subTab !== "forward") {
      return;
    }
    const currentID = String(editingChainID || chainForm.chainID || "").trim();
    if (currentID) {
      return;
    }
    if (chains.length === 0) {
      return;
    }
    beginEditChain(chains[0]);
  }, [chainForm.chainID, chains, editingChainID, subTab]);

  const selectedNode = useMemo(
    () => nodes.find((item) => String(item.node_no) === selectedNodeID),
    [nodes, selectedNodeID],
  );
  const nodeNameByID = useMemo(() => {
    const out: Record<string, string> = {};
    for (const item of nodes) {
      const nodeID = normalizeNodeIDText(String(item.node_no || ""));
      if (!nodeID) {
        continue;
      }
      const nodeName = String(item.node_name || "").trim();
      if (!nodeName) {
        continue;
      }
      out[nodeID] = nodeName;
    }
    return out;
  }, [nodes]);
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
  const chainHopConfigs = useMemo(
    () => normalizeProbeLinkHopFormItems(chainForm.hopConfigs),
    [chainForm.hopConfigs],
  );
  const chainRouteNodeNos = useMemo(
    () => chainHopConfigs.map((item) => item.nodeNo),
    [chainHopConfigs],
  );
  const portForwardItems = useMemo(
    () => normalizeProbeLinkPortForwardFormItems(chainForm.portForwards),
    [chainForm.portForwards],
  );
  const selectedChainForPortForward = useMemo(() => {
    const chainID = String(editingChainID || chainForm.chainID || "").trim();
    if (!chainID) {
      return undefined;
    }
    return chains.find((item) => item.chain_id === chainID);
  }, [chainForm.chainID, chains, editingChainID]);

  useEffect(() => {
    if (!selectedNode) {
      return;
    }
    const preferredPort = normalizePort(Number(selectedNode.public_port || selectedNode.service_port || 0));
    const portToUse = preferredPort > 0 ? preferredPort : defaultInternalPort;
    setInternalPort(portToUse);
    setExternalPort(portToUse);
  }, [selectedNodeID]);

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
          listenHost: defaultLinkChainListenHost,
          servicePort: defaultLinkChainListenPort,
          externalPort: 0,
          linkLayer: defaultLinkChainLayer,
          dialMode: defaultLinkChainDialMode,
        };
      const nextItem: ProbeLinkHopFormItem = {
        nodeNo: safeNodeNo,
        listenHost: patch.listenHost === undefined ? existing.listenHost : normalizeProbeLinkHopListenHost(patch.listenHost),
        servicePort: patch.servicePort === undefined ? existing.servicePort : normalizePort(patch.servicePort),
        externalPort: patch.externalPort === undefined ? existing.externalPort : normalizePort(patch.externalPort),
        linkLayer: patch.linkLayer === undefined ? existing.linkLayer : normalizeProbeLinkLayer(patch.linkLayer),
        dialMode: patch.dialMode === undefined ? existing.dialMode : normalizeProbeLinkDialMode(patch.dialMode),
      };
      const next = [...current];
      if (index >= 0) {
        next[index] = nextItem;
      } else {
        next.push(nextItem);
      }
      return {
        ...prev,
        hopConfigs: next,
      };
    });
  }

  function addHopConfig() {
    setChainForm((prev) => {
      const current = normalizeProbeLinkHopFormItems(prev.hopConfigs);
      const used = new Set<number>();
      for (const item of current) {
        used.add(item.nodeNo);
      }

      let nextNodeNo = 0;
      for (const node of nodes) {
        const candidate = Math.trunc(Number(node.node_no || 0));
        if (!Number.isFinite(candidate) || candidate <= 0) {
          continue;
        }
        if (!used.has(candidate)) {
          nextNodeNo = candidate;
          break;
        }
      }
      if (nextNodeNo <= 0) {
        for (const item of current) {
          if (item.nodeNo > nextNodeNo) {
            nextNodeNo = item.nodeNo;
          }
        }
        nextNodeNo += 1;
      }
      if (!Number.isFinite(nextNodeNo) || nextNodeNo <= 0) {
        return prev;
      }

      return {
        ...prev,
        hopConfigs: [
          ...current,
          {
            nodeNo: nextNodeNo,
            listenHost: defaultLinkChainListenHost,
            servicePort: defaultLinkChainListenPort,
            externalPort: 0,
            linkLayer: defaultLinkChainLayer,
            dialMode: defaultLinkChainDialMode,
          },
        ],
      };
    });
  }

  function updateHopConfigNodeNo(nodeNo: number, nextNodeNoRaw: string) {
    const nextNodeNo = Math.trunc(Number(nextNodeNoRaw));
    if (!Number.isFinite(nextNodeNo) || nextNodeNo <= 0) {
      setChainStatus("探针编号必须为正整数");
      return;
    }
    if (nextNodeNo !== nodeNo && chainRouteNodeNos.includes(nextNodeNo)) {
      setChainStatus(`探针 #${nextNodeNo} 已在链路中，请勿重复添加`);
      return;
    }
    setChainForm((prev) => {
      const current = normalizeProbeLinkHopFormItems(prev.hopConfigs);
      const index = current.findIndex((item) => item.nodeNo === nodeNo);
      if (index < 0) {
        return prev;
      }
      const next = [...current];
      next[index] = {
        ...next[index],
        nodeNo: nextNodeNo,
      };
      return {
        ...prev,
        hopConfigs: next,
      };
    });
  }

  function removeHopConfig(nodeNo: number) {
    setChainForm((prev) => {
      const current = normalizeProbeLinkHopFormItems(prev.hopConfigs);
      const next = current.filter((item) => item.nodeNo !== nodeNo);
      if (next.length === current.length) {
        return prev;
      }
      return {
        ...prev,
        hopConfigs: next,
      };
    });
  }

  function moveHopConfig(nodeNo: number, direction: -1 | 1) {
    setChainForm((prev) => {
      const current = normalizeProbeLinkHopFormItems(prev.hopConfigs);
      const index = current.findIndex((item) => item.nodeNo === nodeNo);
      if (index < 0) {
        return prev;
      }
      const target = index + direction;
      if (target < 0 || target >= current.length) {
        return prev;
      }
      const next = [...current];
      const tmp = next[index];
      next[index] = next[target];
      next[target] = tmp;
      return {
        ...prev,
        hopConfigs: next,
      };
    });
  }

  function updatePortForward(id: string, patch: Partial<ProbeLinkPortForwardFormItem>) {
    const targetID = String(id || "").trim();
    if (!targetID) {
      return;
    }
    setChainForm((prev) => {
      const current = normalizeProbeLinkPortForwardFormItems(prev.portForwards);
      const index = current.findIndex((item) => item.id === targetID);
      if (index < 0) {
        return prev;
      }
      const existing = current[index];
      const next = [...current];
      next[index] = {
        id: existing.id,
        name: patch.name === undefined ? existing.name : String(patch.name || ""),
        entrySide: patch.entrySide === undefined ? existing.entrySide : normalizeProbeLinkPFEntrySide(patch.entrySide),
        listenHost: patch.listenHost === undefined ? existing.listenHost : normalizePortForwardHost(patch.listenHost),
        listenPort: patch.listenPort === undefined ? existing.listenPort : normalizePort(Number(patch.listenPort || 0)),
        targetHost: patch.targetHost === undefined ? existing.targetHost : normalizePortForwardTargetHost(patch.targetHost),
        targetPort: patch.targetPort === undefined ? existing.targetPort : normalizePort(Number(patch.targetPort || 0)),
        network: patch.network === undefined ? existing.network : normalizeProbeLinkPFNetwork(patch.network),
        enabled: patch.enabled === undefined ? existing.enabled : patch.enabled === true,
      };
      return {
        ...prev,
        portForwards: next,
      };
    });
  }

  function addPortForward() {
    setChainForm((prev) => {
      const current = normalizeProbeLinkPortForwardFormItems(prev.portForwards);
      return {
        ...prev,
        portForwards: [
          ...current,
          {
            id: buildPortForwardFormID(),
            name: "",
            entrySide: "chain_entry",
            listenHost: defaultPortForwardListenHost,
            listenPort: 0,
            targetHost: "",
            targetPort: 0,
            network: defaultPortForwardNetwork,
            enabled: true,
          },
        ],
      };
    });
  }

  function removePortForward(id: string) {
    const targetID = String(id || "").trim();
    if (!targetID) {
      return;
    }
    setChainForm((prev) => {
      const current = normalizeProbeLinkPortForwardFormItems(prev.portForwards);
      const next = current.filter((item) => item.id !== targetID);
      if (next.length === current.length) {
        return prev;
      }
      return {
        ...prev,
        portForwards: next,
      };
    });
  }

  function handleSelectChainForPortForward(chainIDRaw: string) {
    const chainID = String(chainIDRaw || "").trim();
    if (!chainID) {
      return;
    }
    const found = chains.find((item) => item.chain_id === chainID);
    if (!found) {
      setChainStatus(`链路不存在：${chainID}`);
      return;
    }
    beginEditChain(found);
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

  async function loadChainsFromBackendCache(options?: { emptyStatus?: string; silentStatus?: boolean }): Promise<number> {
    try {
      const items = await GetProbeLinkChainsCache();
      const sorted = sortProbeLinkChains((Array.isArray(items) ? items : []) as ProbeLinkChainItem[]);
      setChains(sorted);
      if (!options?.silentStatus) {
        if (sorted.length > 0) {
          setChainStatus(`已加载本地链路缓存（${sorted.length} 条）`);
        } else {
          setChainStatus(options?.emptyStatus || "本地链路缓存为空，请点击“从主控获取链路”");
        }
      }
      return sorted.length;
    } catch (error) {
      const msg = errorToMessage(error);
      setChainStatus(`读取本地链路缓存失败：${msg}`);
      return 0;
    }
  }

  async function loadChainsFromController() {
    if (!props.sessionToken.trim()) {
      setChains([]);
      setChainStatus("未登录，无法加载链路列表");
      return;
    }
    setIsLoadingChains(true);
    setChainStatus("正在从主控刷新链路...");
    try {
      await ForceRefreshNetworkAssistantNodes(props.controllerBaseUrl, props.sessionToken);
      const total = await loadChainsFromBackendCache({ silentStatus: true });
      setChainStatus(`已从主控刷新并加载链路（${total} 条）`);
      void loadChainUsers();
      void loadNodes();
    } catch (error) {
      const msg = errorToMessage(error);
      setChainStatus(`从主控刷新链路失败：${msg}`);
    } finally {
      setIsLoadingChains(false);
    }
  }

  async function handleForceRefreshProbeDNSCache() {
    if (!props.sessionToken.trim()) {
      setChainStatus("未登录，无法强制刷新 DNS 缓存");
      return;
    }
    if (isRefreshingDNSCache) {
      return;
    }

    setIsRefreshingDNSCache(true);
    setChainStatus("正在强制刷新 DNS 缓存...");
    try {
      const message = await forceRefreshProbeDNSCache(props.controllerBaseUrl, props.sessionToken);
      const text = String(message || "").trim();
      setChainStatus(text || "DNS 缓存已刷新（有效期 24 小时）");
    } catch (error) {
      const msg = errorToMessage(error);
      setChainStatus(`强制刷新 DNS 缓存失败：${msg}`);
    } finally {
      setIsRefreshingDNSCache(false);
    }
  }

  function resetChainForm() {
    setEditingChainID("");
    const defaultUserID = chainUsers[0] ? normalizeChainUsername(chainUsers[0].username) : "";
    setChainForm(createEmptyProbeLinkChainForm(
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
    const routeNodeIDs = normalizeProbeChainRouteNodeIDTextsFromChain(item);
    setChainForm({
      chainID: item.chain_id,
      name: item.name || "",
      userID: normalizedUserID,
      userPublicKey: chainUserPublicKeys[normalizedUserID] || item.user_public_key || "",
      secret: item.secret || "",
      hopConfigs: normalizeProbeLinkHopFormItemsFromChain(
        item.hop_configs,
        item.listen_host || defaultLinkChainListenHost,
        normalizePort(item.listen_port || 0) || defaultLinkChainListenPort,
        normalizeProbeLinkLayer(item.link_layer),
        routeNodeIDs,
      ),
      portForwards: normalizeProbeLinkPortForwardFormItemsFromChain(item.port_forwards),
    });
    if (normalizedUserID && !chainUserPublicKeys[normalizedUserID]) {
      void loadChainUserPublicKey(normalizedUserID, { silentStatus: true });
    }
    setChainStatus(`正在编辑链路：${item.name || "未命名链路"}`);
  }

  async function handleSaveChain() {
    if (!props.sessionToken.trim()) {
      setChainStatus("未登录，无法保存链路");
      return;
    }
    const name = chainForm.name.trim();
    const userID = normalizeChainUsername(chainForm.userID);
    const userPublicKey = chainForm.userPublicKey.trim();
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
    const routeNodeIDs = chainRouteNodeNos.map((nodeNo) => String(nodeNo));
    if (routeNodeIDs.length === 0) {
      setChainStatus("请至少添加一个探针，最后一个探针将自动作为出口");
      return;
    }
    const exitNodeID = routeNodeIDs[routeNodeIDs.length - 1];
    const entryNodeID = routeNodeIDs[0] || "";
    const cascades = routeNodeIDs.slice(1, -1);
    const hopConfigsResult = buildProbeLinkHopConfigsPayload(chainForm);
    if (hopConfigsResult.error) {
      setChainStatus(hopConfigsResult.error);
      return;
    }
    if (hopConfigsResult.items.length === 0) {
      setChainStatus("请至少添加一个探针，最后一个探针将自动作为出口");
      return;
    }
    const portForwardsResult = buildProbeLinkPortForwardsPayload(chainForm);
    if (portForwardsResult.error) {
      setChainStatus(portForwardsResult.error);
      return;
    }
    const firstHop = hopConfigsResult.items[0];
    const fallbackListenHost = normalizeProbeLinkHopListenHost(firstHop.listen_host) || defaultLinkChainListenHost;
    const fallbackListenPort = normalizePort(Number(firstHop.listen_port || 0)) || defaultLinkChainListenPort;
    const fallbackLayer = normalizeProbeLinkLayer(firstHop.link_layer || defaultLinkChainLayer);

    setIsSavingChain(true);
    setChainStatus(editingChainID ? "正在更新链路..." : "正在新增链路...");
    try {
      const response = await upsertProbeLinkChain(props.controllerBaseUrl, props.sessionToken, {
        chain_id: editingChainID || undefined,
        name,
        user_id: userID,
        user_public_key: userPublicKey,
        secret: chainForm.secret.trim() || undefined,
        entry_node_id: entryNodeID,
        exit_node_id: exitNodeID,
        cascade_node_ids: cascades,
        listen_host: fallbackListenHost,
        listen_port: fallbackListenPort,
        link_layer: fallbackLayer,
        hop_configs: hopConfigsResult.items,
        port_forwards: portForwardsResult.items,
        egress_host: defaultLinkChainEgressHost,
        egress_port: defaultLinkChainEgressPort,
      });
      if (response.item) {
        beginEditChain(response.item);
      }
      const saveStatus = response.apply_ok === false && response.apply_error
        ? `保存成功，但下发探针异常：${response.apply_error}`
        : (editingChainID ? "链路更新成功" : "链路新增成功");
      await ForceRefreshNetworkAssistantNodes(props.controllerBaseUrl, props.sessionToken);
      const total = await loadChainsFromBackendCache({ silentStatus: true });
      setChainStatus(`${saveStatus}，缓存已更新（${total} 条）`);
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
      setChainStatus("链路标识不能为空");
      return;
    }
    const found = chains.find((item) => item.chain_id === target);
    const displayName = (found?.name || "").trim() || "未命名链路";
    if (!window.confirm(`确认删除链路“${displayName}”？`)) {
      return;
    }

    setDeletingChainID(target);
    setChainStatus(`正在删除链路：${displayName}`);
    try {
      const response = await deleteProbeLinkChain(props.controllerBaseUrl, props.sessionToken, target);
      if (editingChainID === target) {
        resetChainForm();
      }
      const deleteStatus = response.apply_ok === false && response.apply_error
        ? `删除成功，但探针清理异常：${response.apply_error}`
        : `链路已删除：${displayName}`;
      await ForceRefreshNetworkAssistantNodes(props.controllerBaseUrl, props.sessionToken);
      const total = await loadChainsFromBackendCache({ silentStatus: true });
      setChainStatus(`${deleteStatus}，缓存已更新（${total} 条）`);
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
        ? `已切换到探针 #${nextNodeID}，旧检测连接已自动关闭`
        : "已切换探针，旧检测连接已自动关闭");
    } catch (error) {
      const msg = errorToMessage(error);
      setStatus(`切换探针时关闭旧检测连接失败：${msg}`);
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
        setStatus(`持续延迟检测中：第 ${round} 次，延迟 ${latency === null ? "-" : `${latency}ms`}`);
      } catch (error) {
        if (!continuousTestingRef.current || loopSeq !== continuousTestSeqRef.current) {
          return;
        }
        const msg = errorToMessage(error);
        setResultSummary(`error=${msg}`);
        setStatus(`持续延迟检测异常：${msg}（3秒后重试）`);
      }
      await sleep(3000);
    }
  }

  async function handleStartTest() {
    if (!props.sessionToken.trim()) {
      setStatus("未登录，无法开始延迟检测");
      return;
    }
    const nodeID = selectedNodeID.trim();
    if (!nodeID) {
      setStatus("请选择探针");
      return;
    }
    if (testTargets.length === 0) {
      setStatus("未找到可用探针地址，请先在探针管理里配置公网地址，或确认 Cloudflare 已生成 api 域名");
      return;
    }
    if (isTesting) {
      setStatus("探针延迟检测已在持续运行中，请先停止检测");
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
    setStatus("正在下发延迟检测命令...");
    try {
      stopLocalContinuousTestLoop();
      await closeLocalProbeLinkSessionSilently();

      const startResp = await startProbeLinkTestOnController(props.controllerBaseUrl, props.sessionToken, {
        node_id: nodeID,
        protocol,
        internal_port: safeInternalPort,
      });
      const startMessage = startResp.message || "探针已启动延迟检测服务";
      setStatus(`${startMessage}，正在连接 ${testTarget.host}:${safeExternalPort} ...`);

      const maxConnectAttemptsPerTarget = 4;
      let first: ProbeLinkConnectResult | null = null;
      let connectedTarget: ProbeTestTarget | null = null;
      const connectErrors: string[] = [];
      for (let targetIndex = 0; targetIndex < testTargets.length && !first; targetIndex += 1) {
        const target = testTargets[targetIndex];
        for (let attempt = 1; attempt <= maxConnectAttemptsPerTarget; attempt += 1) {
          try {
            setStatus(`延迟检测服务已启动，正在连接 ${target.host}:${safeExternalPort}（${target.source}，第 ${attempt}/${maxConnectAttemptsPerTarget} 次）...`);
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
              setStatus(`等待检测链路就绪：${target.host}:${safeExternalPort}（${target.source}）失败：${lastConnectErr}`);
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
        setStatus(`延迟检测已启动，连接已建立，持续检测中：${connectedTarget.host}:${safeExternalPort}（${connectedTarget.source}）`);
      } else {
        setStatus(`延迟检测已启动，连接已建立，持续检测中：${safeExternalPort}`);
      }
      void runContinuousTestLoop(currentSeq);
    } catch (error) {
      const msg = errorToMessage(error);
      setStatus(`延迟检测失败：${msg}（探针检测服务保持开启，便于排查；如需关闭请点击“停止检测”）`);
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
      const baseMessage = stopResp.message || "已停止延迟检测，探针检测服务已停止";
      if (localCloseErr) {
        setStatus(`${baseMessage}（本地连接关闭异常：${localCloseErr}）`);
      } else {
        setStatus(baseMessage);
      }
    } catch (error) {
      const msg = errorToMessage(error);
      setStatus(`停止延迟检测失败：${msg}`);
    } finally {
      setIsOperating(false);
    }
  }

  return (
    <div className="content-block">
      <h2>{isForwardOnlyTab ? "端口转发" : "链路管理"}</h2>

      {!isForwardOnlyTab ? (
        <div className="subtab-list" style={{ marginBottom: 12 }}>
          <button className={`subtab-btn ${subTab === "list" ? "active" : ""}`} onClick={() => setSubTab("list")}>链路列表</button>
          <button className={`subtab-btn ${subTab === "test" ? "active" : ""}`} onClick={() => setSubTab("test")}>探针延迟</button>
        </div>
      ) : null}

      {!isForwardOnlyTab && subTab === "list" ? (
        <>
          <div className="identity-card">
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
              <label>逐探针链路配置</label>
              <div style={{ width: "100%" }}>
                <div className="content-actions" style={{ marginTop: 0, marginBottom: 8 }}>
                  <button className="btn" onClick={addHopConfig} disabled={isOperatingChain}>
                    添加探针
                  </button>
                </div>
                {chainHopConfigs.length > 0 ? (
                  <div className="probe-table-wrap" style={{ marginTop: 4 }}>
                    <table className="probe-table" style={{ minWidth: 860 }}>
                      <thead>
                        <tr>
                          <th>顺序</th>
                          <th>探针</th>
                          <th>监听地址</th>
                          <th>监听端口</th>
                          <th>外部端口</th>
                          <th>链路协议</th>
                          <th>建立方向</th>
                          <th style={{ width: 220 }}>操作</th>
                        </tr>
                      </thead>
                      <tbody>
                        {chainHopConfigs.map((cfg, index) => {
                          const nodeNo = cfg.nodeNo;
                          const node = nodes.find((item) => item.node_no === nodeNo);
                          const usedNodeNos = new Set(
                            chainHopConfigs
                              .filter((item) => item.nodeNo !== nodeNo)
                              .map((item) => item.nodeNo),
                          );
                          return (
                            <tr key={`hop-${nodeNo}-${index}`}>
                              <td>{index + 1}{index === chainHopConfigs.length - 1 ? " (出口)" : ""}</td>
                              <td>
                                <select
                                  className="input"
                                  value={String(nodeNo)}
                                  onChange={(event) => updateHopConfigNodeNo(nodeNo, event.target.value)}
                                  disabled={isOperatingChain}
                                >
                                  {node ? null : (
                                    <option value={String(nodeNo)}>
                                      #{nodeNo} (未在探针列表)
                                    </option>
                                  )}
                                  {nodes.map((item) => {
                                    const candidate = Math.trunc(Number(item.node_no || 0));
                                    if (!Number.isFinite(candidate) || candidate <= 0) {
                                      return null;
                                    }
                                    return (
                                      <option
                                        key={`hop-node-option-${nodeNo}-${candidate}`}
                                        value={String(candidate)}
                                        disabled={usedNodeNos.has(candidate)}
                                      >
                                        #{candidate} {item.node_name || ""}
                                      </option>
                                    );
                                  })}
                                </select>
                              </td>
                              <td>
                                <input
                                  className="input"
                                  value={cfg.listenHost}
                                  placeholder={defaultLinkChainListenHost}
                                  onChange={(event) => updateHopConfig(nodeNo, { listenHost: event.target.value })}
                                  disabled={isOperatingChain}
                                />
                              </td>
                              <td>
                                <input
                                  className="input"
                                  type="number"
                                  min={1}
                                  max={65535}
                                  value={cfg.servicePort > 0 ? cfg.servicePort : ""}
                                  placeholder={String(defaultLinkChainListenPort)}
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
                              <td>
                                <select
                                  className="input"
                                  value={cfg.dialMode}
                                  onChange={(event) => updateHopConfig(nodeNo, { dialMode: normalizeProbeLinkDialMode(event.target.value) })}
                                  disabled={isOperatingChain}
                                >
                                  <option value="forward">正向(本跳拨下一跳)</option>
                                  <option value="reverse">反向(下一跳拨本跳)</option>
                                </select>
                              </td>
                              <td>
                                <div className="probe-table-actions">
                                  <button
                                    className="btn"
                                    onClick={() => moveHopConfig(nodeNo, -1)}
                                    disabled={isOperatingChain || index <= 0}
                                  >
                                    上移
                                  </button>
                                  <button
                                    className="btn"
                                    onClick={() => moveHopConfig(nodeNo, 1)}
                                    disabled={isOperatingChain || index >= chainHopConfigs.length - 1}
                                  >
                                    下移
                                  </button>
                                  <button
                                    className="btn"
                                    onClick={() => removeHopConfig(nodeNo)}
                                    disabled={isOperatingChain}
                                  >
                                    删除
                                  </button>
                                </div>
                              </td>
                            </tr>
                          );
                        })}
                      </tbody>
                    </table>
                  </div>
                ) : (
                  <div className="status">请点击“添加探针”，最后一个探针将自动作为出口。</div>
                )}
              </div>
            </div>
          </div>

          <div className="content-actions">
            <button
              className="btn"
              onClick={() => {
                void loadChainsFromController();
              }}
              disabled={isOperatingChain || isLoadingChainUsers}
            >
              {isLoadingChains ? "获取中..." : "从主控获取链路"}
            </button>
            <button
              className="btn"
              onClick={() => {
                void handleForceRefreshProbeDNSCache();
              }}
              disabled={isOperatingChain || isLoadingChainUsers}
            >
              {isRefreshingDNSCache ? "刷新中..." : "强制刷新 DNS 缓存"}
            </button>
            <button
              className="btn"
              onClick={() => {
                void loadChainUsers();
              }}
              disabled={isOperatingChain || isLoadingChainUsers}
            >
              {isLoadingChainUsers ? "刷新中..." : "刷新用户列表"}
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
            {buildProbeChainRouteSummaryTextFromNodeIDTexts(chainRouteNodeNos.map((nodeNo) => String(nodeNo)))}
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
                  <th style={{ width: 250 }}>操作</th>
                </tr>
              </thead>
              <tbody>
                {chains.length > 0 ? chains.map((item) => (
                  <tr key={item.chain_id}>
                    <td>
                      <div className="probe-table-name">{item.name || "-"}</div>
                    </td>
                    <td>
                      <div>{item.user_id || "-"}</div>
                    </td>
                    <td>
                      <div>{buildChainRouteSummary(item, nodeNameByID)}</div>
                      {item.hop_configs && item.hop_configs.length > 0 ? (
                        <div className="probe-table-sub">
                          hop: {item.hop_configs.map((cfg) => {
                            const listenPort = normalizePort(Number(cfg.listen_port || 0));
                            const externalPort = normalizePort(Number(cfg.external_port || 0));
                            const listenHost = normalizeProbeLinkHopListenHost(cfg.listen_host || item.listen_host || defaultLinkChainListenHost);
                            const dialMode = normalizeProbeLinkDialMode(cfg.dial_mode || "forward");
                            return `#${cfg.node_no}(host:${listenHost || "-"}, listen:${listenPort || "-"}, ext:${externalPort || listenPort || "-"}, ${normalizeProbeLinkLayer(cfg.link_layer)}, ${dialMode})`;
                          }).join(" | ")}
                        </div>
                      ) : null}
                      {item.port_forwards && item.port_forwards.length > 0 ? (
                        <>
                          <div className="probe-table-sub">
                            端口转发: {item.port_forwards.filter((rule) => rule.enabled).length}/{item.port_forwards.length} 启用
                          </div>
                          <div className="probe-table-sub">
                            {item.port_forwards
                              .slice(0, 2)
                              .map((rule) => `${rule.name || rule.id || "未命名规则"}（${buildProbeLinkPFDirectionSummary(normalizeProbeLinkPFEntrySide(rule.entry_side), normalizeProbeChainRouteNodeIDTextsFromChain(item), nodeNameByID)}）`)
                              .join(" | ")}
                            {item.port_forwards.length > 2 ? ` 等 ${item.port_forwards.length} 条` : ""}
                          </div>
                        </>
                      ) : null}
                    </td>
                    <td>{item.hop_configs && item.hop_configs.length > 0 ? "按探针配置" : `${item.listen_host || defaultLinkChainListenHost}:${normalizePort(item.listen_port || 0)}`}</td>
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
                        <button
                          className="btn"
                          id={`chain-ping-btn-${item.chain_id}`}
                          onClick={() => void handlePingChain(item.chain_id)}
                          disabled={!!chainPingingID}
                          style={{
                            minWidth: 54,
                            background: chainPingingID === item.chain_id ? "#555" : undefined,
                          }}
                        >
                          {chainPingingID === item.chain_id ? "测试中" : "测试"}
                        </button>
                      </div>
                      {chainPingStates[item.chain_id] && (
                        <div
                          className="status"
                          style={{
                            marginTop: 4,
                            color:
                              chainPingStates[item.chain_id].ok === null
                                ? "#aaa"
                                : chainPingStates[item.chain_id].ok
                                  ? "#4ade80"
                                  : "#f87171",
                            fontSize: 12,
                          }}
                        >
                          {chainPingStates[item.chain_id].ok === null
                            ? "⏳ " + chainPingStates[item.chain_id].message
                            : chainPingStates[item.chain_id].ok
                              ? `✅ ${chainPingStates[item.chain_id].durationMS}ms`
                              : `❌ ${chainPingStates[item.chain_id].message}`}
                        </div>
                      )}
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

      {isForwardOnlyTab || subTab === "forward" ? (
        <>
          <div className="identity-card">
            <div className="row">
              <label>链路</label>
              <select
                className="input"
                value={String(editingChainID || chainForm.chainID || "")}
                onChange={(event) => handleSelectChainForPortForward(event.target.value)}
                disabled={isOperatingChain || isLoadingChains}
              >
                <option value="">请选择链路</option>
                {chains.map((item) => (
                  <option key={`forward-chain-${item.chain_id}`} value={item.chain_id}>
                    {item.name || `链路 ${item.chain_id}`} ({item.chain_id})
                  </option>
                ))}
              </select>
            </div>
            <div className="row">
              <label>链路路由</label>
              <div className="status-inline">{selectedChainForPortForward ? buildChainRouteSummary(selectedChainForPortForward, nodeNameByID) : "请选择链路"}</div>
            </div>
            <div className="row">
              <label>规则摘要</label>
              <div className="status-inline">
                {portForwardItems.length > 0
                  ? `共 ${portForwardItems.length} 条，链路入口端监听 ${portForwardItems.filter((item) => item.entrySide === "chain_entry").length} 条，链路出口端监听 ${portForwardItems.filter((item) => item.entrySide === "chain_exit").length} 条`
                  : "暂无规则"}
              </div>
            </div>
            <div className="row">
              <label>端口转发规则</label>
              <div style={{ width: "100%" }}>
                <div className="content-actions" style={{ marginTop: 0, marginBottom: 8 }}>
                  <button className="btn" onClick={addPortForward} disabled={isOperatingChain || !selectedChainForPortForward}>添加规则</button>
                </div>
                {portForwardItems.length > 0 ? (
                  <div className="probe-table-wrap" style={{ marginTop: 4 }}>
                    <table className="probe-table" style={{ minWidth: 1240 }}>
                      <thead>
                        <tr>
                          <th>规则</th>
                          <th>业务入口</th>
                          <th>本地监听</th>
                          <th>远端目标</th>
                          <th>协议</th>
                          <th>启用</th>
                          <th style={{ width: 180 }}>操作</th>
                        </tr>
                      </thead>
                      <tbody>
                        {portForwardItems.map((item) => (
                          <tr key={`port-forward-${item.id}`}>
                            <td>
                              <input
                                className="input"
                                value={item.name}
                                placeholder={item.id}
                                onChange={(event) => updatePortForward(item.id, { name: event.target.value })}
                                disabled={isOperatingChain}
                              />
                            </td>
                            <td>
                              <div style={{ display: "grid", gap: 6 }}>
                                <select
                                  className="input"
                                  value={item.entrySide}
                                  onChange={(event) => updatePortForward(item.id, { entrySide: normalizeProbeLinkPFEntrySide(event.target.value) })}
                                  disabled={isOperatingChain}
                                >
                                  <option value="chain_entry">链路入口端</option>
                                  <option value="chain_exit">链路出口端</option>
                                </select>
                                <div className="probe-table-sub">
                                  {buildProbeLinkPFDirectionSummary(item.entrySide, chainRouteNodeNos.map((nodeNo) => String(nodeNo)), nodeNameByID)}
                                </div>
                              </div>
                            </td>
                            <td>
                              <div style={{ display: "grid", gap: 6, gridTemplateColumns: "1fr 120px" }}>
                                <input
                                  className="input"
                                  value={item.listenHost}
                                  placeholder={defaultPortForwardListenHost}
                                  onChange={(event) => updatePortForward(item.id, { listenHost: event.target.value })}
                                  disabled={isOperatingChain}
                                />
                                <input
                                  className="input"
                                  type="number"
                                  min={1}
                                  max={65535}
                                  value={item.listenPort > 0 ? item.listenPort : ""}
                                  onChange={(event) => updatePortForward(item.id, { listenPort: Number(event.target.value) || 0 })}
                                  disabled={isOperatingChain}
                                />
                              </div>
                            </td>
                            <td>
                              <div style={{ display: "grid", gap: 6, gridTemplateColumns: "1fr 120px" }}>
                                <input
                                  className="input"
                                  value={item.targetHost}
                                  placeholder="127.0.0.1"
                                  onChange={(event) => updatePortForward(item.id, { targetHost: event.target.value })}
                                  disabled={isOperatingChain}
                                />
                                <input
                                  className="input"
                                  type="number"
                                  min={1}
                                  max={65535}
                                  value={item.targetPort > 0 ? item.targetPort : ""}
                                  onChange={(event) => updatePortForward(item.id, { targetPort: Number(event.target.value) || 0 })}
                                  disabled={isOperatingChain}
                                />
                              </div>
                            </td>
                            <td>
                              <select
                                className="input"
                                value={item.network}
                                onChange={(event) => updatePortForward(item.id, { network: normalizeProbeLinkPFNetwork(event.target.value) })}
                                disabled={isOperatingChain}
                              >
                                <option value="tcp">tcp</option>
                                <option value="udp">udp</option>
                                <option value="both">both</option>
                              </select>
                            </td>
                            <td>
                              <label className="probe-direct-toggle compact">
                                <input
                                  type="checkbox"
                                  checked={item.enabled}
                                  onChange={(event) => updatePortForward(item.id, { enabled: event.target.checked })}
                                  disabled={isOperatingChain}
                                />
                                启用
                              </label>
                            </td>
                            <td>
                              <div className="probe-table-actions">
                                <button className="btn" onClick={() => removePortForward(item.id)} disabled={isOperatingChain}>删除</button>
                              </div>
                              <div className="probe-table-sub">ID: {item.id}</div>
                            </td>
                          </tr>
                        ))}
                      </tbody>
                    </table>
                  </div>
                ) : (
                  <div className="status">暂无端口转发规则。每条规则都可以独立指定由链路入口端或链路出口端负责监听。</div>
                )}
              </div>
            </div>
          </div>

          <div className="content-actions">
            <button
              className="btn"
              onClick={() => {
                void loadChainsFromController();
              }}
              disabled={isOperatingChain}
            >
              {isLoadingChains ? "获取中..." : "从主控获取链路"}
            </button>
            <button
              className="btn"
              onClick={() => {
                void handleForceRefreshProbeDNSCache();
              }}
              disabled={isOperatingChain}
            >
              {isRefreshingDNSCache ? "刷新中..." : "强制刷新 DNS 缓存"}
            </button>
            <button
              className="btn"
              onClick={() => void handleSaveChain()}
              disabled={isOperatingChain || !selectedChainForPortForward}
            >
              {isSavingChain ? "保存中..." : "保存端口转发"}
            </button>
          </div>

          <div className="status">{chainStatus}</div>
          <div className="status">当前规则条数：{portForwardItems.length}</div>
        </>
      ) : null}

      {!isForwardOnlyTab && subTab === "test" ? (
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
              {isTesting ? "检测中..." : isOperating ? "处理中..." : "开始检测"}
            </button>
            <button className="btn" onClick={() => void handleStopTest()} disabled={isOperating || !selectedNodeID}>
              {isOperating ? "停止中..." : "停止检测"}
            </button>
          </div>

          <div className="status">{status}</div>
          <div className="status">检测目标：{testTarget.host || "-"} {testTarget.host ? `(${testTarget.source})` : ""}</div>
          <div className="status">候选目标：{testTargets.length > 0 ? testTargets.map((item) => `${item.host}(${item.source})`).join(" | ") : "-"}</div>
          <div className="status">探针延迟：{latencyMS === null ? "-" : `${latencyMS} ms`}</div>
          <div className="status">{resultSummary || "暂无延迟检测详情"}</div>
        </>
      ) : null}
    </div>
  );
}

function createEmptyProbeLinkChainForm(
  defaultUserID = "",
  defaultUserPublicKey = "",
): ProbeLinkChainFormState {
  return {
    chainID: "",
    name: "",
    userID: normalizeChainUsername(defaultUserID),
    userPublicKey: String(defaultUserPublicKey || "").trim(),
    secret: "",
    hopConfigs: [],
    portForwards: [],
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

function normalizeProbeLinkDialMode(raw: unknown): ProbeLinkDialMode {
  const value = String(raw || "").trim().toLowerCase();
  if (value === "reverse" || value === "rev") {
    return "reverse";
  }
  return "forward";
}

function normalizeProbeLinkPFNetwork(raw: unknown): ProbeLinkPFNetwork {
  const value = String(raw || "").trim().toLowerCase();
  if (value === "udp") {
    return "udp";
  }
  if (value === "both" || value === "tcp+udp" || value === "udp+tcp") {
    return "both";
  }
  return "tcp";
}

function normalizeProbeLinkPFEntrySide(raw: unknown): ProbeLinkPFEntrySide {
  const value = String(raw || "").trim().toLowerCase();
  if (value === "chain_exit" || value === "exit" || value === "egress") {
    return "chain_exit";
  }
  return "chain_entry";
}

function buildProbeLinkPFEntrySideLabel(side: ProbeLinkPFEntrySide): string {
  return side === "chain_exit" ? "链路出口端监听" : "链路入口端监听";
}

function buildProbeLinkPFDirectionSummary(
  side: ProbeLinkPFEntrySide,
  routeNodeIDs: string[],
  nodeNameByID: Record<string, string>,
): string {
  const normalized = routeNodeIDs
    .map((item) => normalizeNodeIDText(item))
    .filter((item) => item !== "");
  if (normalized.length === 0) {
    return `${buildProbeLinkPFEntrySideLabel(side)}：未配置链路`;
  }
  const businessEntryNodeID = side === "chain_exit" ? normalized[normalized.length - 1] : normalized[0];
  const businessExitNodeID = side === "chain_exit" ? normalized[0] : normalized[normalized.length - 1];
  return `${buildProbeLinkPFEntrySideLabel(side)}：${resolveProbeRouteNodeLabel(businessEntryNodeID, nodeNameByID)} -> ${resolveProbeRouteNodeLabel(businessExitNodeID, nodeNameByID)}`;
}

function normalizePortForwardHost(raw: unknown): string {
  const value = String(raw ?? "").trim();
  return value || defaultPortForwardListenHost;
}

function normalizePortForwardTargetHost(raw: unknown): string {
  return String(raw ?? "").trim();
}

function buildPortForwardFormID(): string {
  if (typeof crypto !== "undefined" && typeof crypto.randomUUID === "function") {
    return `pf-${crypto.randomUUID().slice(0, 8)}`;
  }
  return `pf-${Math.random().toString(16).slice(2, 10)}`;
}

function normalizeProbeLinkPortForwardFormItems(values?: ProbeLinkPortForwardFormItem[]): ProbeLinkPortForwardFormItem[] {
  if (!Array.isArray(values) || values.length === 0) {
    return [];
  }
  const out: ProbeLinkPortForwardFormItem[] = [];
  const seen = new Set<string>();
  for (const item of values) {
    const id = String(item.id || "").trim() || buildPortForwardFormID();
    if (seen.has(id)) {
      continue;
    }
    seen.add(id);
    out.push({
      id,
      name: String(item.name || ""),
      entrySide: normalizeProbeLinkPFEntrySide(item.entrySide),
      listenHost: normalizePortForwardHost(item.listenHost),
      listenPort: normalizePort(Number(item.listenPort || 0)),
      targetHost: normalizePortForwardTargetHost(item.targetHost),
      targetPort: normalizePort(Number(item.targetPort || 0)),
      network: normalizeProbeLinkPFNetwork(item.network),
      enabled: item.enabled !== false,
    });
  }
  return out;
}

function normalizeProbeLinkPortForwardFormItemsFromChain(
  values: ProbeLinkChainItem["port_forwards"] | undefined,
): ProbeLinkPortForwardFormItem[] {
  if (!Array.isArray(values) || values.length === 0) {
    return [];
  }
  const out: ProbeLinkPortForwardFormItem[] = [];
  const seen = new Set<string>();
  for (const item of values) {
    const id = String(item.id || "").trim() || buildPortForwardFormID();
    if (seen.has(id)) {
      continue;
    }
    seen.add(id);
    out.push({
      id,
      name: String(item.name || ""),
      entrySide: normalizeProbeLinkPFEntrySide(item.entry_side),
      listenHost: normalizePortForwardHost(item.listen_host),
      listenPort: normalizePort(Number(item.listen_port || 0)),
      targetHost: normalizePortForwardTargetHost(item.target_host),
      targetPort: normalizePort(Number(item.target_port || 0)),
      network: normalizeProbeLinkPFNetwork(item.network),
      enabled: item.enabled !== false,
    });
  }
  return out;
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
      listenHost: normalizeProbeLinkHopListenHost(item.listenHost) || defaultLinkChainListenHost,
      servicePort: normalizePort(Number(item.servicePort || 0)),
      externalPort: normalizePort(Number(item.externalPort || 0)),
      linkLayer: normalizeProbeLinkLayer(item.linkLayer),
      dialMode: normalizeProbeLinkDialMode(item.dialMode),
    });
  }
  return out;
}

function normalizeProbeLinkHopFormItemsFromChain(
  values: Array<{ node_no: number; listen_host?: string; service_port?: number; external_port?: number; listen_port?: number; link_layer?: "http" | "http2" | "http3" | ""; dial_mode?: "forward" | "reverse" | "" }> | undefined,
  defaultListenHost: string,
  defaultServicePort: number,
  defaultLinkLayer: ProbeLinkLayer,
  routeNodeIDs?: string[],
): ProbeLinkHopFormItem[] {
  const safeDefaultListenHost = normalizeProbeLinkHopListenHost(defaultListenHost) || defaultLinkChainListenHost;
  const safeDefaultServicePort = normalizePort(defaultServicePort) || defaultLinkChainListenPort;
  const safeDefaultLayer = normalizeProbeLinkLayer(defaultLinkLayer);

  const fromStoreMap = new Map<number, ProbeLinkHopFormItem>();
  for (const item of Array.isArray(values) ? values : []) {
    const nodeNo = Math.trunc(Number(item.node_no || 0));
    if (!Number.isFinite(nodeNo) || nodeNo <= 0 || fromStoreMap.has(nodeNo)) {
      continue;
    }
    fromStoreMap.set(nodeNo, {
      nodeNo,
      listenHost: normalizeProbeLinkHopListenHost(item.listen_host) || safeDefaultListenHost,
      servicePort: normalizePort(Number(item.service_port || 0)) || safeDefaultServicePort,
      externalPort: normalizePort(Number(item.external_port || item.listen_port || 0)),
      linkLayer: normalizeProbeLinkLayer(item.link_layer || safeDefaultLayer),
      dialMode: normalizeProbeLinkDialMode(item.dial_mode || defaultLinkChainDialMode),
    });
  }

  const routeNodeNos: number[] = [];
  const routeSeen = new Set<number>();
  for (const raw of Array.isArray(routeNodeIDs) ? routeNodeIDs : []) {
    const nodeNo = Math.trunc(Number(normalizeNodeIDText(raw)));
    if (!Number.isFinite(nodeNo) || nodeNo <= 0 || routeSeen.has(nodeNo)) {
      continue;
    }
    routeSeen.add(nodeNo);
    routeNodeNos.push(nodeNo);
  }

  const out: ProbeLinkHopFormItem[] = [];
  const outSeen = new Set<number>();
  for (const nodeNo of routeNodeNos) {
    const found = fromStoreMap.get(nodeNo);
    if (found) {
      out.push(found);
    } else {
      out.push({
        nodeNo,
        listenHost: safeDefaultListenHost,
        servicePort: safeDefaultServicePort,
        externalPort: 0,
        linkLayer: safeDefaultLayer,
        dialMode: defaultLinkChainDialMode,
      });
    }
    outSeen.add(nodeNo);
  }
  for (const [nodeNo, found] of fromStoreMap.entries()) {
    if (outSeen.has(nodeNo)) {
      continue;
    }
    out.push(found);
  }
  return out;
}

function buildProbeLinkHopConfigsPayload(form: ProbeLinkChainFormState): {
  items: Array<{ node_no: number; listen_host?: string; listen_port?: number; external_port?: number; link_layer?: ProbeLinkLayer; dial_mode?: ProbeLinkDialMode }>;
  error: string;
} {
  const normalizedHopConfigs = normalizeProbeLinkHopFormItems(form.hopConfigs);
  if (normalizedHopConfigs.length === 0) {
    return { items: [], error: "" };
  }
  const items: Array<{ node_no: number; listen_host?: string; listen_port?: number; external_port?: number; link_layer?: ProbeLinkLayer; dial_mode?: ProbeLinkDialMode }> = [];
  for (const cfg of normalizedHopConfigs) {
    const listenHost = normalizeProbeLinkHopListenHost(cfg.listenHost);
    if (!listenHost) {
      return { items: [], error: `探针 #${cfg.nodeNo} 的监听地址不能为空` };
    }
    const servicePort = normalizePort(cfg.servicePort);
    const externalPort = normalizePort(cfg.externalPort);
    const layer = normalizeProbeLinkLayer(cfg.linkLayer);
    const dialMode = normalizeProbeLinkDialMode(cfg.dialMode);
    if (servicePort <= 0) {
      return { items: [], error: `探针 #${cfg.nodeNo} 的监听端口必须在 1-65535 范围内` };
    }
    items.push({
      node_no: cfg.nodeNo,
      listen_host: listenHost,
      listen_port: servicePort,
      external_port: externalPort > 0 ? externalPort : undefined,
      link_layer: layer,
      dial_mode: dialMode,
    });
  }
  return { items, error: "" };
}

function buildProbeLinkPortForwardsPayload(form: ProbeLinkChainFormState): {
  items: Array<{
    id?: string;
    name?: string;
    entry_side?: ProbeLinkPFEntrySide;
    listen_host?: string;
    listen_port: number;
    target_host: string;
    target_port: number;
    network?: ProbeLinkPFNetwork;
    enabled: boolean;
  }>;
  error: string;
} {
  const normalized = normalizeProbeLinkPortForwardFormItems(form.portForwards);
  if (normalized.length === 0) {
    return { items: [], error: "" };
  }
  const items: Array<{
    id?: string;
    name?: string;
    entry_side?: ProbeLinkPFEntrySide;
    listen_host?: string;
    listen_port: number;
    target_host: string;
    target_port: number;
    network?: ProbeLinkPFNetwork;
    enabled: boolean;
  }> = [];
  for (const cfg of normalized) {
    const entrySide = normalizeProbeLinkPFEntrySide(cfg.entrySide);
    const listenHost = normalizePortForwardHost(cfg.listenHost);
    const listenPort = normalizePort(cfg.listenPort);
    const targetHost = normalizePortForwardTargetHost(cfg.targetHost);
    const targetPort = normalizePort(cfg.targetPort);
    const network = normalizeProbeLinkPFNetwork(cfg.network);
    if (listenPort <= 0) {
      return { items: [], error: `端口转发 ${cfg.name || cfg.id} 的监听端口必须在 1-65535 范围内` };
    }
    if (!targetHost) {
      return { items: [], error: `端口转发 ${cfg.name || cfg.id} 的目标地址不能为空` };
    }
    if (targetPort <= 0) {
      return { items: [], error: `端口转发 ${cfg.name || cfg.id} 的目标端口必须在 1-65535 范围内` };
    }
    items.push({
      id: cfg.id,
      name: cfg.name,
      entry_side: entrySide,
      listen_host: listenHost,
      listen_port: listenPort,
      target_host: targetHost,
      target_port: targetPort,
      network,
      enabled: cfg.enabled,
    });
  }
  return { items, error: "" };
}

function normalizeProbeChainRouteNodeIDTextsFromChain(item: ProbeLinkChainItem): string[] {
  const out: string[] = [];
  const seen = new Set<string>();
  const pushNodeID = (raw: unknown) => {
    const nodeID = normalizeNodeIDText(raw);
    if (!nodeID || seen.has(nodeID)) {
      return;
    }
    seen.add(nodeID);
    out.push(nodeID);
  };
  for (const nodeID of Array.isArray(item.cascade_node_ids) ? item.cascade_node_ids : []) {
    pushNodeID(nodeID);
  }
  pushNodeID(item.exit_node_id || "");
  return out;
}

function buildProbeChainRouteSummaryTextFromNodeIDTexts(nodeIDs: string[]): string {
  const normalized = nodeIDs
    .map((item) => normalizeNodeIDText(item))
    .filter((item) => item !== "");
  if (normalized.length === 0) {
    return " -> (未添加探针)";
  }
  const parts: string[] = [];
  for (let i = 0; i < normalized.length; i += 1) {
    const nodeID = normalized[i];
    if (i === normalized.length - 1) {
      parts.push(` -> #${nodeID}(出口)`);
    } else {
      parts.push(` -> #${nodeID}`);
    }
  }
  return parts.join("");
}

function buildChainRouteSummary(item: ProbeLinkChainItem, nodeNameByID: Record<string, string>): string {
	const route = ["管理端"];
	const cascades = Array.isArray(item.cascade_node_ids) ? item.cascade_node_ids : [];
	for (const nodeID of cascades) {
		const normalized = normalizeNodeIDText(nodeID);
		if (!normalized) {
			continue;
		}
		route.push(resolveProbeRouteNodeLabel(normalized, nodeNameByID));
	}
	const exitNodeID = normalizeNodeIDText(item.exit_node_id || "");
	if (exitNodeID) {
		route.push(`${resolveProbeRouteNodeLabel(exitNodeID, nodeNameByID)}(出口)`);
	} else {
		route.push("(未配置出口)");
	}
	return route.join(" -> ");
}

function resolveProbeRouteNodeLabel(nodeID: string, nodeNameByID: Record<string, string>): string {
	const normalized = normalizeNodeIDText(nodeID);
	if (!normalized) {
		return "";
	}
	const nodeName = String(nodeNameByID[normalized] || "").trim();
	if (nodeName) {
		return nodeName;
	}
	return `#${normalized}`;
}

function normalizeProbeLinkHopListenHost(raw: unknown): string {
  return String(raw ?? "").trim();
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
