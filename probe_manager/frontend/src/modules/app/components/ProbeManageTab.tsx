import { useEffect, useRef, useState } from "react";
import {
  createProbeNodeOnController,
  deleteProbeShellShortcut,
  execProbeShellSessionOnController,
  fetchProbeNodeLogs,
  fetchProbeNodeStatus,
  fetchProbeNodes,
  fetchProbeReportIntervalSettings,
  fetchProbeShellShortcuts,
  setProbeReportInterval,
  startProbeShellSessionOnController,
  stopProbeShellSessionOnController,
  upsertProbeShellShortcut,
  updateProbeNodeOnController,
  upgradeAllProbeNodes,
  upgradeProbeNode,
  type ProbeNodeLogsResponse,
  type ProbeShellShortcutItem,
  type ProbeShellSessionControlResponse,
  type ProbeNodeStatusItem,
  type ProbeReportIntervalSettings,
} from "../services/controller-api";

type ProbeManageTabProps = {
  controllerBaseUrl: string;
  sessionToken: string;
};

type ProbeSubTab = "list" | "status" | "logs" | "shell";
type ProbeTargetSystem = "linux" | "windows";

type ProbeNodeItem = {
  node_no: number;
  node_name: string;
  remark?: string;
  ddns?: string;
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
    ip_locations?: Record<string, string>;
    system?: {
      cpu_percent?: number;
      memory_total_bytes?: number;
      memory_used_bytes?: number;
      memory_used_percent?: number;
      swap_total_bytes?: number;
      swap_used_bytes?: number;
      swap_used_percent?: number;
      disk_total_bytes?: number;
      disk_used_bytes?: number;
      disk_used_percent?: number;
    };
  };
};

type ProbeNodeSettingsDraft = {
  node_no: number;
  node_name: string;
  remark: string;
  ddns: string;
  target_system: ProbeTargetSystem;
  direct_connect: boolean;
  payment_cycle: string;
  cost: string;
  expire_at: string;
  vendor_name: string;
  vendor_url: string;
};

export function ProbeManageTab(props: ProbeManageTabProps) {
  const [subTab, setSubTab] = useState<ProbeSubTab>("list");
  const [showCreateModal, setShowCreateModal] = useState(false);
  const logOutputRef = useRef<HTMLPreElement | null>(null);
  const shellOutputRef = useRef<HTMLPreElement | null>(null);
  const upgradeLogPollingDeadlineRef = useRef(0);
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
  const [logNodeIDInput, setLogNodeIDInput] = useState("");
  const [logLinesInput, setLogLinesInput] = useState("200");
  const [logSinceMinutesInput, setLogSinceMinutesInput] = useState("0");
  const [probeLogSource, setProbeLogSource] = useState("-");
  const [probeLogFilePath, setProbeLogFilePath] = useState("-");
  const [probeLogFetchedAt, setProbeLogFetchedAt] = useState("-");
  const [probeLogContent, setProbeLogContent] = useState("");
  const [probeLogAutoScroll, setProbeLogAutoScroll] = useState(true);
  const [upgradeLogPollingNodeID, setUpgradeLogPollingNodeID] = useState("");
  const [shellNodeIDInput, setShellNodeIDInput] = useState("");
  const [shellSessionID, setShellSessionID] = useState("");
  const [shellSessionNodeID, setShellSessionNodeID] = useState("");
  const [shellCommandInput, setShellCommandInput] = useState("");
  const [shellTimeoutSecInput, setShellTimeoutSecInput] = useState("60");
  const [shellOutput, setShellOutput] = useState("");
  const [shellAutoScroll, setShellAutoScroll] = useState(true);
  const [isShellRunning, setIsShellRunning] = useState(false);
  const [shellShortcuts, setShellShortcuts] = useState<ProbeShellShortcutItem[]>([]);
  const [shortcutNameInput, setShortcutNameInput] = useState("");
  const [shortcutCommandInput, setShortcutCommandInput] = useState("");

  useEffect(() => {
    if (!controllerAddress.trim() && props.controllerBaseUrl.trim()) {
      setControllerAddress(props.controllerBaseUrl.trim());
    }
  }, [controllerAddress, props.controllerBaseUrl]);

  useEffect(() => {
    void loadNodes();
  }, []);

  useEffect(() => {
    if (!probeLogAutoScroll || subTab !== "logs" || !logOutputRef.current) {
      return;
    }
    logOutputRef.current.scrollTop = logOutputRef.current.scrollHeight;
  }, [probeLogAutoScroll, probeLogContent, subTab]);

  useEffect(() => {
    if (subTab !== "logs" || logNodeIDInput.trim()) {
      return;
    }
    const firstNode = nodes.find((item) => item.node_no > 0);
    if (firstNode) {
      setLogNodeIDInput(String(firstNode.node_no));
    }
  }, [logNodeIDInput, nodes, subTab]);

  useEffect(() => {
    if (subTab !== "logs") {
      return;
    }
    if (nodes.length > 0 || isLoading) {
      return;
    }
    void loadNodes();
  }, [isLoading, nodes.length, subTab]);

  useEffect(() => {
    if (subTab !== "logs") {
      return;
    }
    const nodeID = logNodeIDInput.trim();
    if (!nodeID) {
      return;
    }
    void loadSelectedNodeLogs({ nodeID, silent: true });
  }, [subTab, logNodeIDInput]);

  useEffect(() => {
    if (subTab !== "logs") {
      return;
    }
    const nodeID = upgradeLogPollingNodeID.trim();
    if (!nodeID) {
      return;
    }
    if (logNodeIDInput.trim() !== nodeID) {
      setUpgradeLogPollingNodeID("");
      return;
    }

    const timer = window.setInterval(() => {
      if (Date.now() >= upgradeLogPollingDeadlineRef.current) {
        setUpgradeLogPollingNodeID("");
        return;
      }
      void loadSelectedNodeLogs({ nodeID, silent: true });
    }, 3000);
    return () => window.clearInterval(timer);
  }, [subTab, upgradeLogPollingNodeID, logNodeIDInput]);

  useEffect(() => {
    if (!shellAutoScroll || subTab !== "shell" || !shellOutputRef.current) {
      return;
    }
    shellOutputRef.current.scrollTop = shellOutputRef.current.scrollHeight;
  }, [shellAutoScroll, shellOutput, subTab]);

  useEffect(() => {
    if (subTab !== "shell" || shellNodeIDInput.trim()) {
      return;
    }
    const firstNode = nodes.find((item) => item.node_no > 0);
    if (firstNode) {
      setShellNodeIDInput(String(firstNode.node_no));
    }
  }, [nodes, shellNodeIDInput, subTab]);

  useEffect(() => {
    if (subTab !== "shell") {
      return;
    }
    if (nodes.length > 0 || isLoading) {
      return;
    }
    void loadNodes();
  }, [isLoading, nodes.length, subTab]);

  useEffect(() => {
    if (subTab !== "shell") {
      return;
    }
    void loadShellShortcuts({ silent: true });
  }, [subTab]);

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
      setNodes(mergedNodes);
      setStatus(remoteNodes.length ? "已从主控同步探针列表" : "主控暂无探针，请先创建");
    } catch (error) {
      const msg = error instanceof Error ? error.message : "unknown error";
      setStatus(`加载探针列表失败：${msg}`);
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
      const created = await createProbeNodeFromController(controllerAddress, props.sessionToken, cleanName);
      await loadNodes();
      setNodeNameInput("");
      setShowCreateModal(false);
      setSubTab("list");
      setStatus(`节点已创建：${created.node_name}（节点号 ${created.node_no}）`);
    } catch (error) {
      const msg = error instanceof Error ? error.message : "unknown error";
      setStatus(`创建节点失败：${msg}`);
    } finally {
      setIsLoading(false);
    }
  }

  async function updateNode(
    nodeNo: number,
    patch: Partial<Pick<ProbeNodeItem, "node_name" | "remark" | "ddns" | "target_system" | "direct_connect" | "payment_cycle" | "cost" | "expire_at" | "vendor_name" | "vendor_url">>,
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
    const nextDDNS = (patch.ddns ?? current.ddns ?? "").trim();
    const nextTargetSystem = patch.target_system ?? current.target_system;
    const nextDirectConnect = patch.direct_connect ?? current.direct_connect;
    const nextPaymentCycle = (patch.payment_cycle ?? current.payment_cycle ?? "").trim();
    const nextCost = (patch.cost ?? current.cost ?? "").trim();
    const nextExpireAt = (patch.expire_at ?? current.expire_at ?? "").trim();
    const nextVendorName = (patch.vendor_name ?? current.vendor_name ?? "").trim();
    const nextVendorURL = (patch.vendor_url ?? current.vendor_url ?? "").trim();

    setIsLoading(true);
    try {
      const updated = await updateProbeNodeOnControllerOnly(controllerAddress, props.sessionToken, {
        node_no: nodeNo,
        node_name: nextNodeName,
        remark: nextRemark,
        ddns: nextDDNS,
        target_system: nextTargetSystem,
        direct_connect: nextDirectConnect,
        payment_cycle: nextPaymentCycle,
        cost: nextCost,
        expire_at: nextExpireAt,
        vendor_name: nextVendorName,
        vendor_url: nextVendorURL,
      });
      setNodes((prev) => sortNodes(prev.map((node) => (node.node_no === nodeNo ? { ...node, ...updated } : node))));
      setStatus(`节点已更新：${updated.node_name}`);
      void loadNodeStatus();
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
      ddns: node.ddns || "",
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
      setSubTab("logs");
      setLogNodeIDInput(String(node.node_no));
      setLogSinceMinutesInput("10");
      upgradeLogPollingDeadlineRef.current = Date.now() + (2 * 60 * 1000);
      setUpgradeLogPollingNodeID(String(node.node_no));
      void loadSelectedNodeLogs({ nodeID: String(node.node_no), silent: true });
      setStatus(`已下发升级命令：${node.node_name}`);
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
      setSubTab("logs");
      const result = await upgradeAllProbeNodes(base, token);
      upgradeLogPollingDeadlineRef.current = Date.now() + (2 * 60 * 1000);
      if (logNodeIDInput.trim()) {
        setUpgradeLogPollingNodeID(logNodeIDInput.trim());
        void loadSelectedNodeLogs({ nodeID: logNodeIDInput.trim(), silent: true });
      }
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

  async function loadSelectedNodeLogs(options?: { nodeID?: string; silent?: boolean }) {
    const nodeID = (options?.nodeID ?? logNodeIDInput).trim();
    const silent = options?.silent === true;
    if (!nodeID) {
      if (!silent) {
        setStatus("请选择探针节点");
      }
      return;
    }

    const lines = normalizeIntInput(logLinesInput, 200, 1, 2000);
    const sinceMinutes = normalizeIntInput(logSinceMinutesInput, 0, 0, 2000);
    setLogLinesInput(String(lines));
    setLogSinceMinutesInput(String(sinceMinutes));

    if (!silent) {
      setIsLoading(true);
    }
    try {
      const data = await fetchProbeLogsFromController(controllerAddress, props.sessionToken, nodeID, lines, sinceMinutes);
      setProbeLogSource((data.source || "-").trim() || "-");
      setProbeLogFilePath((data.file_path || "-").trim() || "-");
      setProbeLogFetchedAt(formatTime(data.fetched || data.timestamp || ""));
      setProbeLogContent(data.content || "");
      if (!silent) {
        setStatus(`已拉取探针日志：${data.node_name || nodeID}`);
      }
    } catch (error) {
      const msg = error instanceof Error ? error.message : "unknown error";
      if (!silent) {
        setStatus(`拉取探针日志失败：${msg}`);
      }
    } finally {
      if (!silent) {
        setIsLoading(false);
      }
    }
  }

  async function copySelectedNodeLogs() {
    if (!probeLogContent.trim()) {
      setStatus("暂无可复制的探针日志");
      return;
    }
    try {
      await copyText(probeLogContent);
      setStatus("已复制探针日志");
    } catch (error) {
      const msg = error instanceof Error ? error.message : "unknown error";
      setStatus(`复制探针日志失败：${msg}`);
    }
  }

  function appendShellOutput(text: string) {
    if (!text) {
      return;
    }
    setShellOutput((prev) => prev + text);
  }

  async function loadShellShortcuts(options?: { silent?: boolean }) {
    const silent = options?.silent === true;
    try {
      const items = await fetchProbeShellShortcutsFromController(controllerAddress, props.sessionToken);
      setShellShortcuts(items);
      if (!silent) {
        setStatus(`已同步快捷指令：${items.length} 条`);
      }
    } catch (error) {
      const msg = error instanceof Error ? error.message : "unknown error";
      if (!silent) {
        setStatus(`同步快捷指令失败：${msg}`);
      }
    }
  }

  async function connectShellSession() {
    const nodeID = shellNodeIDInput.trim();
    if (!nodeID) {
      setStatus("请选择探针节点");
      return;
    }
    if (shellSessionID.trim()) {
      setStatus(`当前已连接会话 ${shellSessionID.trim()}，请先关闭`);
      return;
    }

    setIsShellRunning(true);
    try {
      const result = await startProbeShellSessionFromController(controllerAddress, props.sessionToken, nodeID);
      const sessionID = (result.session_id || "").trim();
      if (!result.ok || !sessionID) {
        throw new Error(result.error || "controller returned empty shell session");
      }
      setShellSessionID(sessionID);
      setShellSessionNodeID(nodeID);
      appendShellOutput(`\n[已连接] 节点 #${nodeID}，会话 ${sessionID}\n`);
      setStatus(`已建立 Shell 会话：节点 ${nodeID}`);
    } catch (error) {
      const msg = error instanceof Error ? error.message : "unknown error";
      setStatus(`建立 Shell 会话失败：${msg}`);
    } finally {
      setIsShellRunning(false);
    }
  }

  async function disconnectShellSession(options?: { silent?: boolean }) {
    const silent = options?.silent === true;
    const sessionID = shellSessionID.trim();
    const nodeID = (shellSessionNodeID || shellNodeIDInput).trim();
    if (!sessionID || !nodeID) {
      setShellSessionID("");
      setShellSessionNodeID("");
      if (!silent) {
        setStatus("当前没有活动 Shell 会话");
      }
      return;
    }

    setIsShellRunning(true);
    try {
      await stopProbeShellSessionFromController(controllerAddress, props.sessionToken, nodeID, sessionID);
      if (!silent) {
        appendShellOutput(`\n[已断开] 节点 #${nodeID}，会话 ${sessionID}\n`);
        setStatus("Shell 会话已关闭");
      }
    } catch (error) {
      const msg = error instanceof Error ? error.message : "unknown error";
      if (!silent) {
        setStatus(`关闭 Shell 会话失败：${msg}`);
      }
    } finally {
      setShellSessionID("");
      setShellSessionNodeID("");
      setIsShellRunning(false);
    }
  }

  async function runShellCommand(commandOverride?: string) {
    const sessionID = shellSessionID.trim();
    const nodeID = (shellSessionNodeID || shellNodeIDInput).trim();
    if (!sessionID || !nodeID) {
      setStatus("请先连接 Shell 会话");
      return;
    }

    const commandText = commandOverride ?? shellCommandInput;
    if (!commandText.trim()) {
      setStatus("请输入命令");
      return;
    }
    const timeoutSec = normalizeIntInput(shellTimeoutSecInput, 60, 5, 300);
    setShellTimeoutSecInput(String(timeoutSec));
    if (commandOverride === undefined) {
      setShellCommandInput("");
    }

    appendShellOutput(`\n#${nodeID}> ${commandText}\n`);
    setIsShellRunning(true);
    try {
      const result = await execProbeShellSessionFromController(
        controllerAddress,
        props.sessionToken,
        nodeID,
        sessionID,
        commandText,
        timeoutSec,
      );
      const stdout = result.stdout || "";
      const stderr = result.stderr || "";
      const errText = result.error || "";
      let merged = "";
      if (stdout) {
        merged += stdout;
      }
      if (stderr) {
        if (merged && !merged.endsWith("\n")) {
          merged += "\n";
        }
        merged += stderr;
      }
      if (!merged) {
        merged = "(无输出)\n";
      }
      if (!merged.endsWith("\n")) {
        merged += "\n";
      }
      if (errText) {
        merged += `[error] ${errText}\n`;
      }
      appendShellOutput(merged);
      if (result.ok) {
        setStatus(`命令执行完成（节点 ${nodeID}）`);
      } else {
        setStatus(`命令执行失败：${errText || "unknown error"}`);
      }
    } catch (error) {
      const msg = error instanceof Error ? error.message : "unknown error";
      appendShellOutput(`[rpc error] ${msg}\n`);
      setStatus(`命令执行失败：${msg}`);
    } finally {
      setIsShellRunning(false);
    }
  }

  async function saveShellShortcut() {
    const name = shortcutNameInput.trim();
    const command = shortcutCommandInput;
    if (!name) {
      setStatus("快捷指令名称不能为空");
      return;
    }
    if (!command.trim()) {
      setStatus("快捷指令命令不能为空");
      return;
    }
    setIsShellRunning(true);
    try {
      const items = await upsertProbeShellShortcutFromController(controllerAddress, props.sessionToken, name, command);
      setShellShortcuts(items);
      setShortcutNameInput("");
      setShortcutCommandInput("");
      setStatus(`已保存快捷指令：${name}`);
    } catch (error) {
      const msg = error instanceof Error ? error.message : "unknown error";
      setStatus(`保存快捷指令失败：${msg}`);
    } finally {
      setIsShellRunning(false);
    }
  }

  async function removeShellShortcut(name: string) {
    const cleanName = name.trim();
    if (!cleanName) {
      return;
    }
    setIsShellRunning(true);
    try {
      const items = await deleteProbeShellShortcutFromController(controllerAddress, props.sessionToken, cleanName);
      setShellShortcuts(items);
      setStatus(`已删除快捷指令：${cleanName}`);
    } catch (error) {
      const msg = error instanceof Error ? error.message : "unknown error";
      setStatus(`删除快捷指令失败：${msg}`);
    } finally {
      setIsShellRunning(false);
    }
  }

  return (
    <div className="content-block">
      <h2>探针管理</h2>

      <div className="subtab-list">
        <button className={`subtab-btn ${subTab === "list" ? "active" : ""}`} onClick={() => setSubTab("list")}>探针列表</button>
        <button className={`subtab-btn ${subTab === "status" ? "active" : ""}`} onClick={() => { setSubTab("status"); void loadNodeStatus(); }}>探针状态</button>
        <button className={`subtab-btn ${subTab === "logs" ? "active" : ""}`} onClick={() => setSubTab("logs")}>探针日志</button>
        <button className={`subtab-btn ${subTab === "shell" ? "active" : ""}`} onClick={() => { setSubTab("shell"); void loadShellShortcuts({ silent: true }); }}>Shell 终端</button>
      </div>

      {subTab === "list" ? (
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
              <button className="btn" onClick={() => setShowCreateModal(true)} disabled={isLoading}>新建探针</button>
              <button className="btn" onClick={() => void upgradeAll()} disabled={isLoading || isUpgradingAll || nodes.length === 0}>一键升级（全部探针）</button>
            </div>
            <div>升级命令通过主控下发；直连节点直连 GitHub，非直连节点走主控代理升级。</div>
          </div>

          {nodes.length === 0 ? (
            <div className="status">暂无探针，请点击“新建探针”创建节点。</div>
          ) : (
            <div className="probe-table-wrap">
              <table className="probe-table">
                <thead>
                  <tr>
                    <th>节点号</th>
                    <th>节点信息</th>
                    <th>版本</th>
                    <th>厂家</th>
                    <th>付款周期</th>
                    <th>费用</th>
                    <th>到期</th>
                    <th>系统</th>
                    <th>接入方式</th>
                    <th>操作</th>
                  </tr>
                </thead>
                <tbody>
                  {nodes.map((node) => (
                    <tr key={node.node_no}>
                      <td>{node.node_no}</td>
                      <td>
                        <div className="probe-table-name">{node.node_name}</div>
                        {node.remark ? <div className="probe-table-sub">备注：{node.remark}</div> : null}
                      </td>
                      <td>{node.runtime?.version || "-"}</td>
                      <td>
                        {node.vendor_name ? (
                          <button className="vendor-copy-link" type="button" title={node.vendor_url || "点击复制厂家URL"} onClick={() => void copyVendorURL(node, setStatus)}>
                            {node.vendor_name}
                          </button>
                        ) : "-"}
                      </td>
                      <td>{node.payment_cycle || "-"}</td>
                      <td>{node.cost || "-"}</td>
                      <td>{formatExpireWithRemainingDays(node.expire_at || "")}</td>
                      <td>{node.target_system === "windows" ? "Windows" : "Linux"}</td>
                      <td>{node.direct_connect ? "直连" : "主控代理"}</td>
                      <td>
                        <div className="probe-table-actions">
                          <button className="btn" onClick={() => openSettings(node)} disabled={isLoading}>设置</button>
                          <button className="btn" onClick={() => void copyInstallCommand(node)} disabled={isLoading}>安装</button>
                          <button className="btn" onClick={() => void upgradeOne(node)} disabled={isLoading || isUpgradingAll || upgradingNodeNos.includes(node.node_no)}>
                            {upgradingNodeNos.includes(node.node_no) ? "下发中..." : "升级"}
                          </button>
                        </div>
                      </td>
                    </tr>
                  ))}
                </tbody>
              </table>
            </div>
          )}
        </div>
      ) : subTab === "status" ? (
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
            <div className="status">暂无探针，请先在“探针列表”页签点击“新建探针”。</div>
          ) : (
            <div className="probe-table-wrap">
              <table className="probe-table" style={{ minWidth: 1180 }}>
                <thead>
                  <tr>
                    <th>节点号</th>
                    <th>节点名称</th>
                    <th>状态</th>
                    <th>版本</th>
                    <th>资源状态</th>
                    <th>IP（归属地）</th>
                    <th>最后上报</th>
                  </tr>
                </thead>
                <tbody>
                  {nodeStatusItems.map((item) => {
                    const ips = collectIPs(item);
                    const online = item.runtime?.online === true;
                    const ipLocations = item.runtime?.ip_locations || {};
                    return (
                      <tr key={`status-${item.node_no}`}>
                        <td>{item.node_no > 0 ? item.node_no : (item.runtime?.node_id || "-")}</td>
                        <td>{item.node_name || "-"}</td>
                        <td>{online ? "在线" : "离线"}</td>
                        <td>{item.runtime?.version || "-"}</td>
                        <td>
                          {online
                            ? `CPU ${formatPercent(item.runtime?.system?.cpu_percent)} / RAM ${formatPercentWithBytes(item.runtime?.system?.memory_used_percent, item.runtime?.system?.memory_used_bytes, item.runtime?.system?.memory_total_bytes)} / SWAP ${formatPercentWithBytes(item.runtime?.system?.swap_used_percent, item.runtime?.system?.swap_used_bytes, item.runtime?.system?.swap_total_bytes)} / 磁盘 ${formatPercentWithBytes(item.runtime?.system?.disk_used_percent, item.runtime?.system?.disk_used_bytes, item.runtime?.system?.disk_total_bytes)}`
                            : "-"}
                        </td>
                        <td>
                          {ips.length === 0 ? "-" : (
                            <div style={{ display: "flex", flexWrap: "wrap", gap: 6 }}>
                              {ips.map((ip) => (
                                <button
                                  key={`${item.node_no}-${ip}`}
                                  className="ip-copy-chip"
                                  onClick={() => void copyStatusIP(ip, setStatus)}
                                  title="点击复制IP"
                                  type="button"
                                >
                                  {formatIPWithLocation(ip, ipLocations[ip])}
                                </button>
                              ))}
                            </div>
                          )}
                        </td>
                        <td>{formatTime(item.runtime?.last_seen || "")}</td>
                      </tr>
                    );
                  })}
                </tbody>
              </table>
            </div>
          )}
        </div>
      ) : subTab === "logs" ? (
        <div style={{ marginTop: 12 }}>
          <div className="identity-card" style={{ marginBottom: 12 }}>
            <div>探针日志（通过主控代理拉取）</div>
            <div className="row" style={{ marginBottom: 0 }}>
              <label>探针节点</label>
              <select className="input" value={logNodeIDInput} onChange={(event) => setLogNodeIDInput(event.target.value)} disabled={isLoading || nodes.length === 0}>
                {nodes.length === 0 ? (
                  <option value="">暂无探针</option>
                ) : (
                  nodes.map((node) => (
                    <option key={`log-node-${node.node_no}`} value={String(node.node_no)}>
                      {node.node_name} (#{node.node_no})
                    </option>
                  ))
                )}
              </select>
            </div>
            <div className="row" style={{ marginBottom: 0 }}>
              <label>显示行数</label>
              <input className="input" value={logLinesInput} onChange={(event) => setLogLinesInput(event.target.value)} disabled={isLoading} />
            </div>
            <div className="row" style={{ marginBottom: 0 }}>
              <label>最近分钟</label>
              <input className="input" value={logSinceMinutesInput} onChange={(event) => setLogSinceMinutesInput(event.target.value)} disabled={isLoading} />
            </div>
            <div className="content-actions">
              <button className="btn" onClick={() => void loadSelectedNodeLogs()} disabled={isLoading || nodes.length === 0}>
                {isLoading ? "刷新中..." : "刷新日志"}
              </button>
              <button className="btn" onClick={() => void copySelectedNodeLogs()} disabled={isLoading || !probeLogContent.trim()}>复制日志</button>
              <label className="log-auto-scroll-toggle">
                <input type="checkbox" checked={probeLogAutoScroll} onChange={(event) => setProbeLogAutoScroll(event.target.checked)} disabled={isLoading} />
                自动滚动
              </label>
            </div>
            <div>来源：{probeLogSource || "-"}</div>
            <div>日志文件：{probeLogFilePath || "-"}</div>
            <div>拉取时间：{probeLogFetchedAt || "-"}</div>
          </div>
          <pre ref={logOutputRef} className="log-viewer-output">{probeLogContent || "暂无探针日志内容"}</pre>
        </div>
      ) : (
        <div style={{ marginTop: 12 }}>
          <div className="identity-card" style={{ marginBottom: 12 }}>
            <div>探针 Shell（长会话，支持上下文）</div>
            <div className="row" style={{ marginBottom: 0 }}>
              <label>探针节点</label>
              <select
                className="input"
                value={shellNodeIDInput}
                onChange={(event) => setShellNodeIDInput(event.target.value)}
                disabled={isShellRunning || nodes.length === 0}
              >
                {nodes.length === 0 ? (
                  <option value="">暂无探针</option>
                ) : (
                  nodes.map((node) => (
                    <option key={`shell-node-${node.node_no}`} value={String(node.node_no)}>
                      {node.node_name} (#{node.node_no})
                    </option>
                  ))
                )}
              </select>
            </div>
            <div className="row" style={{ marginBottom: 0 }}>
              <label>命令超时(秒)</label>
              <input
                className="input"
                value={shellTimeoutSecInput}
                onChange={(event) => setShellTimeoutSecInput(event.target.value)}
                disabled={isShellRunning}
              />
            </div>
            <div className="content-actions">
              <button className="btn" onClick={() => void connectShellSession()} disabled={isShellRunning || nodes.length === 0 || !!shellSessionID.trim()}>
                {isShellRunning && !shellSessionID.trim() ? "连接中..." : "建立会话"}
              </button>
              <button className="btn" onClick={() => void disconnectShellSession()} disabled={isShellRunning || !shellSessionID.trim()}>
                关闭会话
              </button>
              <button className="btn" onClick={() => setShellOutput("")} disabled={isShellRunning || !shellOutput.trim()}>
                清空输出
              </button>
              <button className="btn" onClick={() => void loadShellShortcuts()} disabled={isShellRunning}>
                刷新快捷指令
              </button>
              <label className="log-auto-scroll-toggle">
                <input type="checkbox" checked={shellAutoScroll} onChange={(event) => setShellAutoScroll(event.target.checked)} disabled={isShellRunning} />
                自动滚动
              </label>
            </div>
            <div>当前会话：{shellSessionID.trim() ? `${shellSessionID}（节点 #${shellSessionNodeID || shellNodeIDInput}）` : "未连接"}</div>
            <div className="row" style={{ marginBottom: 0 }}>
              <label>命令输入</label>
              <textarea
                className="input"
                rows={4}
                value={shellCommandInput}
                onChange={(event) => setShellCommandInput(event.target.value)}
                placeholder={shellSessionID.trim() ? "例如：pwd 或 cd /tmp && ls -la" : "请先建立会话"}
                disabled={isShellRunning || !shellSessionID.trim()}
              />
            </div>
            <div className="content-actions">
              <button className="btn" onClick={() => void runShellCommand()} disabled={isShellRunning || !shellSessionID.trim() || !shellCommandInput.trim()}>
                {isShellRunning ? "执行中..." : "执行命令"}
              </button>
            </div>
          </div>

          <pre ref={shellOutputRef} className="log-viewer-output">{shellOutput || "暂无 Shell 输出"}</pre>

          <div className="identity-card" style={{ marginTop: 12 }}>
            <div>快捷指令（全局共享）</div>
            <div className="row" style={{ marginBottom: 0 }}>
              <label>名称</label>
              <input
                className="input"
                value={shortcutNameInput}
                onChange={(event) => setShortcutNameInput(event.target.value)}
                placeholder="例如：查看系统信息"
                disabled={isShellRunning}
              />
            </div>
            <div className="row" style={{ marginBottom: 0 }}>
              <label>命令</label>
              <textarea
                className="input"
                rows={3}
                value={shortcutCommandInput}
                onChange={(event) => setShortcutCommandInput(event.target.value)}
                placeholder="例如：uname -a && uptime"
                disabled={isShellRunning}
              />
            </div>
            <div className="content-actions">
              <button className="btn" onClick={() => void saveShellShortcut()} disabled={isShellRunning || !shortcutNameInput.trim() || !shortcutCommandInput.trim()}>
                保存快捷指令
              </button>
            </div>
            {shellShortcuts.length === 0 ? (
              <div className="status">暂无快捷指令</div>
            ) : (
              <div className="probe-table-wrap">
                <table className="probe-table" style={{ minWidth: 860 }}>
                  <thead>
                    <tr>
                      <th>名称</th>
                      <th>命令</th>
                      <th>更新时间</th>
                      <th>操作</th>
                    </tr>
                  </thead>
                  <tbody>
                    {shellShortcuts.map((item) => (
                      <tr key={`shortcut-${item.name}`}>
                        <td>{item.name}</td>
                        <td style={{ whiteSpace: "pre-wrap", wordBreak: "break-all" }}>{item.command}</td>
                        <td>{formatTime(item.updated_at || "")}</td>
                        <td>
                          <div className="probe-table-actions">
                            <button className="btn" onClick={() => setShellCommandInput(item.command)} disabled={isShellRunning || !shellSessionID.trim()}>
                              填充
                            </button>
                            <button className="btn" onClick={() => void runShellCommand(item.command)} disabled={isShellRunning || !shellSessionID.trim()}>
                              执行
                            </button>
                            <button className="btn" onClick={() => { setShortcutNameInput(item.name); setShortcutCommandInput(item.command); }} disabled={isShellRunning}>
                              编辑
                            </button>
                            <button className="btn" onClick={() => void removeShellShortcut(item.name)} disabled={isShellRunning}>
                              删除
                            </button>
                          </div>
                        </td>
                      </tr>
                    ))}
                  </tbody>
                </table>
              </div>
            )}
          </div>
        </div>
      )}

      {showCreateModal ? (
        <div className="probe-settings-modal-mask" onClick={() => setShowCreateModal(false)}>
          <div className="probe-settings-modal" onClick={(event) => event.stopPropagation()}>
            <h3 style={{ marginTop: 0 }}>新建探针</h3>
            <div className="row">
              <label>节点名称</label>
              <input
                className="input"
                value={nodeNameInput}
                placeholder="例如：华东-生产-01"
                onChange={(event) => setNodeNameInput(event.target.value)}
                maxLength={64}
                disabled={isLoading}
              />
            </div>
            <div className="content-actions">
              <button className="btn" onClick={() => void createNode()} disabled={isLoading}>新建节点</button>
              <button className="btn" onClick={() => setShowCreateModal(false)} disabled={isLoading}>取消</button>
            </div>
            <div>创建后会自动生成数字节点号与节点 Secret（仅保存到主控端探针配置）。</div>
          </div>
        </div>
      ) : null}

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
              <label>DDNS标识</label>
              <input
                className="input"
                value={settingsDraft.ddns}
                onChange={(event) => setSettingsDraft((prev) => prev ? { ...prev, ddns: event.target.value } : prev)}
                disabled={isLoading}
                placeholder="留空则使用节点号Base64"
              />
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
                type="date"
                value={toDateInputValue(settingsDraft.expire_at)}
                onChange={(event) => setSettingsDraft((prev) => prev ? { ...prev, expire_at: fromDateInputValue(event.target.value) } : prev)}
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

function formatIPWithLocation(ip: string, location?: string): string {
  const label = (location || "").trim();
  if (!label) {
    return `${ip} (查询中...)`;
  }
  return `${ip} (${label})`;
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

function normalizeIntInput(raw: string, fallback: number, min: number, max: number): number {
  const value = Number.parseInt(raw.trim(), 10);
  if (!Number.isFinite(value)) {
    return fallback;
  }
  if (value < min) {
    return min;
  }
  if (value > max) {
    return max;
  }
  return value;
}

function formatPercentWithBytes(percent: number | undefined, usedBytes: number | undefined, totalBytes: number | undefined): string {
  const percentText = formatPercent(percent);
  const usageText = formatByteUsage(usedBytes, totalBytes);
  if (percentText === "-" && usageText === "-") {
    return "-";
  }
  if (percentText === "-") {
    return usageText;
  }
  if (usageText === "-") {
    return percentText;
  }
  return `${percentText} (${usageText})`;
}

function formatByteUsage(usedBytes: number | undefined, totalBytes: number | undefined): string {
  if (!isValidBytes(usedBytes) || !isValidBytes(totalBytes) || totalBytes <= 0) {
    return "-";
  }
  return `${formatBytes(usedBytes)} / ${formatBytes(totalBytes)}`;
}

function formatBytes(value: number): string {
  if (!isValidBytes(value)) {
    return "-";
  }
  const units = ["B", "KiB", "MiB", "GiB", "TiB", "PiB"];
  let v = value;
  let unitIndex = 0;
  while (v >= 1024 && unitIndex < units.length - 1) {
    v /= 1024;
    unitIndex += 1;
  }
  if (unitIndex === 0) {
    return `${Math.round(v)} ${units[unitIndex]}`;
  }
  return `${v.toFixed(1)} ${units[unitIndex]}`;
}

function isValidBytes(value: number | undefined): value is number {
  return typeof value === "number" && Number.isFinite(value) && value >= 0;
}

function toDateInputValue(value: string): string {
  const raw = (value || "").trim();
  if (!raw) {
    return "";
  }
  if (/^\d{4}-\d{2}-\d{2}$/.test(raw)) {
    return raw;
  }
  const dt = new Date(raw);
  if (Number.isNaN(dt.getTime())) {
    return raw;
  }
  return dt.toISOString().slice(0, 10);
}

function fromDateInputValue(value: string): string {
  const raw = value.trim();
  if (!raw) {
    return "";
  }
  if (/^\d{4}-\d{2}-\d{2}$/.test(raw)) {
    return raw;
  }
  const dt = new Date(raw);
  if (Number.isNaN(dt.getTime())) {
    return "";
  }
  return dt.toISOString().slice(0, 10);
}

function formatExpireWithRemainingDays(expireAt: string): string {
  const dateText = formatDateOnly(expireAt);
  if (dateText === "-") {
    return "-";
  }
  const remain = formatRemainingDays(expireAt);
  return `${dateText} (${remain})`;
}

function formatDateOnly(value: string): string {
  const raw = (value || "").trim();
  if (!raw) {
    return "-";
  }
  if (/^\d{4}-\d{2}-\d{2}$/.test(raw)) {
    return raw;
  }
  const dt = new Date(raw);
  if (Number.isNaN(dt.getTime())) {
    return raw;
  }
  return dt.toISOString().slice(0, 10);
}

function formatRemainingDays(value: string): string {
  const raw = (value || "").trim();
  if (!raw) {
    return "未知";
  }
  const today = new Date();
  const nowUTC = Date.UTC(today.getFullYear(), today.getMonth(), today.getDate());

  let expireUTC = 0;
  if (/^\d{4}-\d{2}-\d{2}$/.test(raw)) {
    const parts = raw.split("-");
    const year = Number.parseInt(parts[0], 10);
    const month = Number.parseInt(parts[1], 10);
    const day = Number.parseInt(parts[2], 10);
    if (!Number.isFinite(year) || !Number.isFinite(month) || !Number.isFinite(day)) {
      return "未知";
    }
    expireUTC = Date.UTC(year, month - 1, day);
  } else {
    const dt = new Date(raw);
    if (Number.isNaN(dt.getTime())) {
      return "未知";
    }
    expireUTC = Date.UTC(dt.getUTCFullYear(), dt.getUTCMonth(), dt.getUTCDate());
  }

  const diffDays = Math.floor((expireUTC - nowUTC) / (24 * 60 * 60 * 1000));
  if (diffDays > 0) {
    return `剩余${diffDays}天`;
  }
  if (diffDays === 0) {
    return "今天到期";
  }
  return `已过期${Math.abs(diffDays)}天`;
}

function sanitizeControllerAddress(rawAddress: string): string {
  const value = rawAddress.trim().replace(/\/+$/, "");
  if (!value) {
    return "http://127.0.0.1:15030";
  }
  return value;
}

function quotePowerShellSingleQuoted(value: string): string {
  return "'" + value.replace(/'/g, "''") + "'";
}

function quotePosixSingleQuoted(value: string): string {
  return "'" + value.replace(/'/g, "'\"'\"'") + "'";
}

function buildInstallCommand(node: ProbeNodeItem, controllerAddress: string): string {
  const base = sanitizeControllerAddress(controllerAddress);
  const proxyBaseURL = base + "/api/probe/proxy";
  const envPairs = [
    "PROBE_NODE_ID=" + quotePosixSingleQuoted(String(node.node_no)),
    "PROBE_NODE_SECRET=" + quotePosixSingleQuoted(node.node_secret),
    "PROBE_CONTROLLER_URL=" + quotePosixSingleQuoted(base),
  ];
  if (!node.direct_connect) {
    envPairs.push("PROBE_PROXY_BASE_URL=" + quotePosixSingleQuoted(proxyBaseURL));
  }
  const params = new URLSearchParams({
    node_id: String(node.node_no),
    secret: node.node_secret,
  });

  if (node.target_system === "windows") {
    const scriptURL = node.direct_connect
      ? "https://raw.githubusercontent.com/fengzhanhuaer/CloudHelper/main/scripts/install_probe_node_service_windows.ps1"
      : base + "/api/probe/proxy/probe-node/install-script?" + params.toString() + "&target=windows";
    return [
      "$env:PROBE_NODE_ID=" + quotePowerShellSingleQuoted(String(node.node_no)),
      "$env:PROBE_NODE_SECRET=" + quotePowerShellSingleQuoted(node.node_secret),
      "$env:PROBE_CONTROLLER_URL=" + quotePowerShellSingleQuoted(base),
      node.direct_connect ? "" : "$env:PROBE_PROXY_BASE_URL=" + quotePowerShellSingleQuoted(proxyBaseURL),
      "$scriptUrl=" + quotePowerShellSingleQuoted(scriptURL),
      "$scriptPath=Join-Path $env:TEMP 'cloudhelper-probe-node-install.ps1'",
      "Invoke-WebRequest -UseBasicParsing -Uri $scriptUrl -OutFile $scriptPath",
      "& $scriptPath",
    ].filter((line) => line).join("; ");
  }

  if (!node.direct_connect) {
    return buildLinuxInstallCommand(base + "/api/probe/proxy/probe-node/install-script?" + params.toString() + "&target=linux", envPairs, "/tmp/cloudhelper-probe-node-install.sh");
  }

  return buildLinuxInstallCommand("https://raw.githubusercontent.com/fengzhanhuaer/CloudHelper/main/scripts/install_probe_node_service.sh", envPairs, "/tmp/cloudhelper-probe-node-install.sh");
}

function buildLinuxInstallCommand(scriptURL: string, envPairs: string[], scriptPath: string): string {
  return [
    "SCRIPT_URL=" + quotePosixSingleQuoted(scriptURL),
    "SCRIPT_PATH=" + quotePosixSingleQuoted(scriptPath),
    "curl -fsSL \"$SCRIPT_URL\" -o \"$SCRIPT_PATH\"",
    "env " + envPairs.join(" ") + " bash \"$SCRIPT_PATH\"",
  ].join("; ");
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

async function createProbeNodeFromController(controllerBaseUrl: string, sessionToken: string, nodeName: string): Promise<ProbeNodeItem> {
  const base = sanitizeControllerAddress(controllerBaseUrl);
  const token = sessionToken.trim();
  if (!token) {
    throw new Error("session token is empty, cannot create probe node on controller");
  }
  return (await createProbeNodeOnController(base, token, nodeName)) as ProbeNodeItem;
}

async function updateProbeNodeOnControllerOnly(
  controllerBaseUrl: string,
  sessionToken: string,
  payload: {
    node_no: number;
    node_name: string;
    remark: string;
    ddns: string;
    target_system: ProbeTargetSystem;
    direct_connect: boolean;
    payment_cycle: string;
    cost: string;
    expire_at: string;
    vendor_name: string;
    vendor_url: string;
  },
): Promise<ProbeNodeItem> {
  const base = sanitizeControllerAddress(controllerBaseUrl);
  const token = sessionToken.trim();
  if (!token) {
    throw new Error("session token is empty, cannot update probe node on controller");
  }
  return (await updateProbeNodeOnController(base, token, payload)) as ProbeNodeItem;
}

async function fetchProbeStatusFromController(controllerBaseUrl: string, sessionToken: string, nodeID?: number): Promise<ProbeNodeStatusItem[]> {
  const base = sanitizeControllerAddress(controllerBaseUrl);
  const token = sessionToken.trim();
  if (!token) {
    throw new Error("session token is empty, cannot fetch status from controller");
  }
  return await fetchProbeNodeStatus(base, token, nodeID);
}

async function fetchProbeLogsFromController(
  controllerBaseUrl: string,
  sessionToken: string,
  nodeID: string,
  lines: number,
  sinceMinutes: number,
): Promise<ProbeNodeLogsResponse> {
  const base = sanitizeControllerAddress(controllerBaseUrl);
  const token = sessionToken.trim();
  if (!token) {
    throw new Error("session token is empty, cannot fetch probe logs from controller");
  }
  return await fetchProbeNodeLogs(base, token, nodeID, lines, sinceMinutes);
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

async function startProbeShellSessionFromController(
  controllerBaseUrl: string,
  sessionToken: string,
  nodeID: string,
): Promise<ProbeShellSessionControlResponse> {
  const base = sanitizeControllerAddress(controllerBaseUrl);
  const token = sessionToken.trim();
  if (!token) {
    throw new Error("session token is empty, cannot start probe shell session");
  }
  return await startProbeShellSessionOnController(base, token, { node_id: String(nodeID) });
}

async function execProbeShellSessionFromController(
  controllerBaseUrl: string,
  sessionToken: string,
  nodeID: string,
  sessionID: string,
  command: string,
  timeoutSec: number,
): Promise<ProbeShellSessionControlResponse> {
  const base = sanitizeControllerAddress(controllerBaseUrl);
  const token = sessionToken.trim();
  if (!token) {
    throw new Error("session token is empty, cannot execute probe shell command");
  }
  return await execProbeShellSessionOnController(base, token, {
    node_id: String(nodeID),
    session_id: String(sessionID),
    command,
    timeout_sec: timeoutSec,
  });
}

async function stopProbeShellSessionFromController(
  controllerBaseUrl: string,
  sessionToken: string,
  nodeID: string,
  sessionID: string,
): Promise<ProbeShellSessionControlResponse> {
  const base = sanitizeControllerAddress(controllerBaseUrl);
  const token = sessionToken.trim();
  if (!token) {
    throw new Error("session token is empty, cannot stop probe shell session");
  }
  return await stopProbeShellSessionOnController(base, token, {
    node_id: String(nodeID),
    session_id: String(sessionID),
  });
}

async function fetchProbeShellShortcutsFromController(controllerBaseUrl: string, sessionToken: string): Promise<ProbeShellShortcutItem[]> {
  const base = sanitizeControllerAddress(controllerBaseUrl);
  const token = sessionToken.trim();
  if (!token) {
    throw new Error("session token is empty, cannot fetch probe shell shortcuts");
  }
  return await fetchProbeShellShortcuts(base, token);
}

async function upsertProbeShellShortcutFromController(
  controllerBaseUrl: string,
  sessionToken: string,
  name: string,
  command: string,
): Promise<ProbeShellShortcutItem[]> {
  const base = sanitizeControllerAddress(controllerBaseUrl);
  const token = sessionToken.trim();
  if (!token) {
    throw new Error("session token is empty, cannot save probe shell shortcut");
  }
  return await upsertProbeShellShortcut(base, token, { name, command });
}

async function deleteProbeShellShortcutFromController(
  controllerBaseUrl: string,
  sessionToken: string,
  name: string,
): Promise<ProbeShellShortcutItem[]> {
  const base = sanitizeControllerAddress(controllerBaseUrl);
  const token = sessionToken.trim();
  if (!token) {
    throw new Error("session token is empty, cannot delete probe shell shortcut");
  }
  return await deleteProbeShellShortcut(base, token, name);
}

