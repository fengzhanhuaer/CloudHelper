let saveFeedbackTimer = 0;
let toastTimer = 0;
let upgradeStatusTimer = 0;
let infoBoxPollTimer = 0;
let infoBoxPendingAction = "";
let infoBoxRefreshPending = false;
let infoBoxLastRevision = "";
let bootErrorLogged = false;
let vrouteNodeNames = new Map();
let routeSettingsState = { groups: [], nodes: [] };

const pages = {
  status: ["状态", "当前 Android 节点配置与运行状态。"],
  route: ["路由", "通过 Android VPN 启用或关闭本机路由能力。"],
  information: ["信息框", "全部探针共享的信息流。"],
  settings: ["设置", "配置主控、路由与应用升级。"]
};

window.CloudHelperUI = {
  setStatus(message) {
    setStatus(message || "");
    setUpgradeStatus(message || "");
    refreshVRouteIfVisible();
    refreshConnectionsIfVisible();
    refreshLogsIfVisible();
  },
  setLinkStatus(payload) {
    renderLinkResult(payload || "");
    refreshLogsIfVisible();
  },
  setVPNStatus(payload) {
    renderVPNDiagnostics(parseJSON(payload || "{}"));
    setStatus(formatVPNSelfCheck((parseJSON(payload || "{}").self_check) || parseJSON(payload || "{}")));
    const button = byId("vpnSelfCheckButton");
    if (button) {
      button.disabled = false;
      button.textContent = "VPN 自检";
    }
    refreshRouteControl();
    refreshVRouteIfVisible();
    refreshConnectionsIfVisible();
    refreshLogsIfVisible();
  },
  setVRouteRTT(payload) {
    renderVRouteRTTResult(parseJSON(payload || "{}"));
    const button = byId("vrouteRTTButton");
    if (button) {
      button.disabled = false;
      button.textContent = "测量 RTT";
    }
    refreshLogsIfVisible();
  },
  setInfoBox(payload) {
    completeInfoBoxRequest(parseJSON(payload || "{}"));
  },
  infoBoxChanged(revision) {
    const nextRevision = String(revision || "").trim();
    if (nextRevision && nextRevision === infoBoxLastRevision) return;
    infoBoxLastRevision = nextRevision;
    requestInfoBoxRefresh();
  }
};

function byId(id) {
  return document.getElementById(id);
}

function hasCloudHelper(method) {
  return !!(window.CloudHelper && typeof window.CloudHelper[method] === "function");
}

function callCloudHelperString(method, fallback, ...args) {
  if (!hasCloudHelper(method)) {
    return fallback || "";
  }
  try {
    const result = window.CloudHelper[method](...args);
    return result == null ? (fallback || "") : String(result);
  } catch (error) {
    return fallback || `CloudHelper.${method} failed: ${error && error.message ? error.message : error}`;
  }
}

function readConfig() {
  if (!hasCloudHelper("loadConfig")) {
    return {
      ready: false,
      status: "Android bridge 未就绪",
      localVersion: "-"
    };
  }
  return JSON.parse(callCloudHelperString("loadConfig", "{}") || "{}");
}

function loadConfig() {
  const config = readConfig();
  const controller = byId("controllerUrl");
  const nodeId = byId("nodeId");
  const nodeSecret = byId("nodeSecret");
  if (controller) {
    controller.value = config.controllerUrl || "";
  }
  if (nodeId) {
    nodeId.value = config.nodeId || "";
  }
  if (nodeSecret) {
    nodeSecret.value = config.nodeSecret || "";
  }
  refreshSummary(config);
  if (byId("status")) {
    setStatus(config.ready ? `状态：${config.status}` : "请设置主控地址、节点 ID 和节点密钥。");
  }
}

function saveConfig(showFeedback) {
  try {
    const status = window.CloudHelper.saveConfig(
      byId("controllerUrl").value,
      byId("nodeId").value,
      byId("nodeSecret").value
    );
    refreshSummary();
    appendUILog("settings", "配置已保存。");
    setStatus(`已保存。状态：${status}`);
    if (showFeedback) {
      showSaveFeedback(false);
    }
    return true;
  } catch (error) {
    const message = `保存失败：${error && error.message ? error.message : error}`;
    appendUILog("settings", message);
    setStatus(message);
    showSaveFeedback(true, message);
    return false;
  }
}

function checkUpgrade(mode) {
  if (!saveConfig(false)) {
    return;
  }
  const modeLabel = mode === "controller" ? "Controller" : "Direct";
  const text = `正在检查 ${modeLabel} 升级...`;
  setUpgradeButtonsDisabled(true);
  setUpgradeStatus(text);
  setStatus(text);
  appendUILog("upgrade", text);
  window.CloudHelper.checkUpgrade(mode);
  startUpgradeStatusPolling();
}

function refreshConfig() {
  if (!saveConfig(false)) {
    return;
  }
  const text = "正在从主控刷新配置...";
  setUpgradeStatus(text);
  setStatus(text);
  appendUILog("settings", text);
  window.CloudHelper.refreshConfig();
}

function refreshLinks() {
  const status = byId("linkStatus");
  if (status) {
    status.textContent = "正在读取本地链路配置...";
  }
  try {
    renderLinkStatus(window.CloudHelper.linkStatus());
  } catch (error) {
    setText("linkStatus", `读取链路失败：${error && error.message ? error.message : error}`);
  }
}

function isVPNRunning(data) {
  if (!data) {
    return false;
  }
  if (Object.prototype.hasOwnProperty.call(data, "android_data_plane_running")) {
    return !!data.android_data_plane_running;
  }
  return !!(data.running || data.status === "running");
}

function isVPNStarting(data) {
  return !!(data && (data.android_starting || data.status === "starting" || data.android_phase === "data_plane_pending" || data.android_phase === "start_data_plane"));
}

function runLinkLatency(chainId) {
  setText("linkStatus", `正在测试链路延迟：${chainId}`);
  appendUILog("link", `正在测试链路延迟：${chainId}`);
  setLinkPanelStatus(chainId, "正在执行 relay ping-pong 延迟测试...", false);
  window.CloudHelper.linkLatency(chainId);
}

function runLinkSpeed(chainId, protocol) {
  const label = protocol ? protocol : "默认";
  setText("linkStatus", `正在测速：${chainId} (${label})`);
  appendUILog("link", `正在测速：${chainId} (${label})`);
  setLinkPanelStatus(chainId, `正在执行 relay speed_test 测速 (${label})...`, false);
  window.CloudHelper.linkSpeed(chainId, protocol || "");
}

function renderLinkStatus(payload) {
  const data = parseJSON(payload);
  const list = byId("linkList");
  if (!list) {
    return;
  }
  list.innerHTML = "";
  if (!data.ok) {
    setText("linkStatus", data.error || "链路配置不可用。");
    return;
  }
  const chains = Array.isArray(data.chains) ? data.chains : [];
  setText("linkStatus", chains.length ? `已加载 ${chains.length} 条链路。` : "暂无链路配置，请先在设置中刷新配置。");
  chains.forEach((chain) => {
    list.appendChild(renderLinkItem(chain));
  });
}

function renderLinkItem(chain) {
  const item = document.createElement("article");
  item.className = "link-item";
  const chainId = chain.chain_id || chain.client_entry_id || "";
  item.dataset.chainId = chain.chain_id || "";
  item.dataset.clientEntryId = chain.client_entry_id || "";
  item.dataset.relayChainId = chain.relay_chain_id || "";
  const title = document.createElement("div");
  title.className = "link-title";
  title.textContent = chain.chain_name || chainId || "未命名链路";
  const meta = document.createElement("div");
  meta.className = "link-meta";
  meta.textContent = [
    chainId ? `ID ${chainId}` : "",
    chain.relay_chain_id ? `Relay ${chain.relay_chain_id}` : "",
    chain.entry_host && chain.entry_port ? `${chain.entry_host}:${chain.entry_port}` : "",
    chain.link_layer ? `Layer ${chain.link_layer}` : "",
    chain.status || ""
  ].filter(Boolean).join(" · ");
  const actions = document.createElement("div");
  actions.className = "actions compact";
  const latency = document.createElement("button");
  latency.className = "command";
  latency.textContent = "延迟";
  latency.disabled = !chainId || chain.status !== "configured";
  latency.onclick = () => runLinkLatency(chainId);
  const speedDefault = document.createElement("button");
  speedDefault.className = "command secondary";
  speedDefault.textContent = "测速";
  speedDefault.disabled = !chainId || chain.status !== "configured";
  speedDefault.onclick = () => runLinkSpeed(chainId, "");
  actions.appendChild(latency);
  actions.appendChild(speedDefault);
  const result = document.createElement("div");
  result.className = "link-result";
  result.textContent = chain.status === "configured" ? "等待测试" : "链路未完整配置";
  if (chain.error) {
    const error = document.createElement("div");
    error.className = "inline-feedback error";
    error.textContent = chain.error;
    item.append(title, meta, error, result, actions);
  } else {
    item.append(title, meta, result, actions);
  }
  return item;
}

function renderLinkResult(payload) {
  const data = parseJSON(payload);
  const status = byId("linkStatus");
  if (!status) {
    return;
  }
  if (Array.isArray(data.results) && data.source === "active_speed_test") {
    const text = formatSpeedResult(data);
    status.textContent = text;
    setLinkPanelContent(data.chain_id, renderSpeedResultDetail(data), !data.ok);
    return;
  }
  if (Array.isArray(data.results)) {
    const text = formatLatencyResult(data);
    status.textContent = text;
    setLinkPanelStatus(data.chain_id, text, !data.ok);
    return;
  }
  if (!data.ok) {
    const text = `测试失败：${data.error || data.status || "unknown"}`;
    status.textContent = text;
    setLinkPanelStatus(data.chain_id, text, true);
    return;
  }
  const text = formatLatencyResult(data);
  status.textContent = text;
  setLinkPanelStatus(data.chain_id, text, false);
}

function setLinkPanelStatus(chainId, message, isError) {
  setLinkPanelContent(chainId, message, isError);
}

function setLinkPanelContent(chainId, content, isError) {
  const panel = findLinkPanel(chainId);
  if (!panel) {
    return;
  }
  const result = panel.querySelector(".link-result");
  if (!result) {
    return;
  }
  result.replaceChildren();
  if (content instanceof Node) {
    result.appendChild(content);
  } else {
    result.textContent = content || "";
  }
  result.classList.toggle("error", !!isError);
}

function findLinkPanel(chainId) {
  const clean = String(chainId || "").trim().toLowerCase();
  if (!clean) {
    return null;
  }
  return Array.from(document.querySelectorAll(".link-item")).find((item) => {
    return [item.dataset.chainId, item.dataset.clientEntryId, item.dataset.relayChainId]
      .some((value) => String(value || "").trim().toLowerCase() === clean);
  }) || null;
}

function formatLatencyResult(data) {
  const details = Array.isArray(data.results)
    ? data.results.map((item) => `${item.protocol}:${item.ok ? `${item.latency_ms}ms` : item.error || "fail"}`).join("；")
    : "";
  return `延迟测试：${data.chain_name || data.chain_id} ${data.status}，最佳 ${data.best_protocol || "-"} ${data.latency_ms || "-"}ms。${details}`;
}

function formatSpeedResult(data) {
  const rate = formatRateText(data.rate_bps);
  const details = Array.isArray(data.results)
    ? data.results.map(formatSpeedProbeDetail).join("；")
    : "";
  const remote = formatRemoteSpeedDebug(data.remote_speed_debug, data);
  return `测速：${data.chain_name || data.chain_id} ${data.status}，${rate}。\n路径：relay speed_test 独立数据连接，最长 10s。\n本地读侧：${details || "-"}\n远方写侧：${remote}`;
}

function formatSpeedProbeDetail(item) {
  const label = formatRelayProtocolLabel(item && item.protocol);
  if (!item || !item.ok) {
    return `${label}:${item && item.error ? item.error : "fail"}`;
  }
  const blocks = [
    formatRateText(item.rate_bps),
    `${formatBytes(item.bytes)}/${formatDurationText(item.duration_ms)}`,
    `首包${formatDurationText(item.first_byte_ms || item.latency_ms)}`,
    `握手${formatDurationText(item.open_handshake_ms)}`,
    `读${item.read_calls || 0}次`,
    `读阻塞max ${formatDurationText(item.max_read_block_ms)}`,
    `avg${formatBytes(item.avg_read_bytes || 0)}`
  ];
  return `${label}:${blocks.join(" ")}`;
}

function renderSpeedResultDetail(data) {
  const root = document.createElement("div");
  root.className = "speed-detail";

  const summary = document.createElement("div");
  summary.className = "speed-summary";
  summary.textContent = [
    data.ok ? "测速完成" : "测速不可达",
    data.status || "",
    formatRateText(data.rate_bps),
    "relay speed_test",
    "最长 10s"
  ].filter(Boolean).join(" · ");
  root.appendChild(summary);

  const grid = document.createElement("div");
  grid.className = "speed-detail-grid";
  grid.appendChild(renderSpeedMetricBox("本地读侧", localSpeedMetricLines(data).join("\n") || "-"));
  grid.appendChild(renderSpeedMetricBox("远方写侧", remoteSpeedMetricLines(data.remote_speed_debug, data).join("\n") || "-"));
  root.appendChild(grid);
  return root;
}

function renderSpeedMetricBox(title, text) {
  const box = document.createElement("div");
  box.className = "speed-metric-box";
  const heading = document.createElement("div");
  heading.className = "speed-metric-title";
  heading.textContent = title;
  const body = document.createElement("div");
  body.className = "speed-metric-body";
  body.textContent = text || "-";
  box.append(heading, body);
  return box;
}

function localSpeedMetricLines(data) {
  const results = Array.isArray(data && data.results) ? data.results : [];
  if (!results.length) {
    return [];
  }
  return results.map((item) => {
    const label = formatRelayProtocolLabel(item && item.protocol);
    if (!item || !item.ok) {
      const err = String(item && item.error || "").trim();
      return `${label}: 失败${err ? ` ${err}` : ""}`;
    }
    return `${label}: ${[
      formatRateText(item.rate_bps),
      `读取 ${formatBytes(item.bytes)}/${formatBytes(item.requested_bytes)}`,
      `用时 ${formatDurationText(item.duration_ms)}`,
      `首包 ${formatDurationText(item.first_byte_ms || item.latency_ms)}`,
      `握手 ${formatDurationText(item.open_handshake_ms)}`,
      `读阻塞max ${formatDurationText(item.max_read_block_ms)}`,
      `读 ${Math.trunc(Number(item.read_calls) || 0)}次`
    ].join(" ｜ ")}`;
  });
}

function formatRemoteSpeedDebug(wrapper, linkItem) {
  const lines = remoteSpeedMetricLines(wrapper, linkItem);
  return lines.length ? lines.join("；") : "-";
}

function remoteSpeedMetricLines(wrapper, linkItem) {
  if (!wrapper || typeof wrapper !== "object") {
    return [];
  }
  if (wrapper.ok === false) {
    return [`拉取失败:${wrapper.error || "-"}`];
  }
  const remote = wrapper.remote && typeof wrapper.remote === "object" ? wrapper.remote : null;
  if (!remote) {
    return [];
  }
  if (remote.ok === false) {
    return [`拉取失败:${remote.error || "-"}`];
  }
  const allSamples = []
    .concat(Array.isArray(remote.active) ? remote.active : [])
    .concat(Array.isArray(remote.recent) ? remote.recent : []);
  const matchIDs = new Set();
  addChainIDMatchVariants(matchIDs, linkItem && linkItem.chain_id);
  addChainIDMatchVariants(matchIDs, linkItem && linkItem.relay_chain_id);
  addChainIDMatchVariants(matchIDs, linkItem && linkItem.client_entry_id);
  const matchedSamples = matchIDs.size
    ? allSamples.filter((item) => chainIDMatches(item && item.chain_id, matchIDs))
    : allSamples;
  const fallback = !matchedSamples.length && allSamples.length > 0;
  const samples = fallback ? allSamples : matchedSamples;
  const source = formatRemoteSpeedSource(wrapper.source);
  const fetched = String(wrapper.fetched || remote.fetched_at || "").trim();
  const head = `node=${remote.node_id || "-"}${source ? `/${source}` : ""}${fallback ? "/最近样本" : ""}${fetched ? `/${formatCompactTime(fetched)}` : ""}`;
  if (!samples.length) {
    return [`${head}/暂无写侧样本`];
  }
  return [`${head}`].concat(samples.slice(0, 4).map(formatRemoteSpeedDebugItem));
}

function formatRemoteSpeedSource(source) {
  const value = String(source || "").trim();
  if (value === "relay_entry") return "链路入口";
  if (value === "management") return "管理通道";
  return value;
}

function formatRemoteSpeedDebugItem(item) {
  if (!item) {
    return "-";
  }
  const protocol = formatRelayProtocolLabel(item.transport || "-");
  const status = item.status || "-";
  const blocks = [
    `${protocol}:${status}`,
    formatRateText(item.rate_bps),
    `${formatBytes(item.bytes || 0)}/${formatBytes(item.requested_bytes || 0)}`,
    `用时${formatDurationText(item.duration_ms || item.age_ms)}`,
    `写${item.write_calls || 0}次`,
    `写阻塞max ${formatDurationText(item.max_write_block_ms)}`
  ];
  if (item.remaining_bytes) {
    blocks.push(`剩余${formatBytes(item.remaining_bytes)}`);
  }
  if (item.error) {
    blocks.push(`错误${item.error}`);
  }
  return blocks.join("/");
}

function addChainIDMatchVariants(set, value) {
  const clean = String(value || "").trim();
  if (!clean) {
    return;
  }
  const variants = [clean, clean.toLowerCase()];
  ["_pub", "_cf"].forEach((suffix) => {
    if (clean.toLowerCase().endsWith(suffix)) {
      variants.push(clean.slice(0, -suffix.length));
      variants.push(clean.slice(0, -suffix.length).toLowerCase());
    }
  });
  variants.forEach((item) => {
    if (item) {
      set.add(item);
    }
  });
}

function chainIDMatches(value, matchIDs) {
  const variants = new Set();
  addChainIDMatchVariants(variants, value);
  for (const item of variants) {
    if (matchIDs.has(item) || matchIDs.has(String(item || "").toLowerCase())) {
      return true;
    }
  }
  return false;
}

function formatRelayProtocolLabel(protocol) {
  const clean = String(protocol || "").trim().toLowerCase();
  if (clean === "websocket-h3") return "WS-H3";
  if (clean === "websocket") return "WS";
  if (!clean || clean === "auto" || clean === "default" || clean === "http" || clean === "http2" || clean === "h2" || clean === "http3" || clean === "h3") return "默认";
  return clean || "-";
}

function formatDurationText(ms) {
  const value = Number(ms);
  if (!Number.isFinite(value) || value <= 0) {
    return "-";
  }
  if (value >= 1000) {
    return `${(value / 1000).toFixed(1)}s`;
  }
  return `${Math.trunc(value)}ms`;
}

function formatRateText(bps) {
  const value = Number(bps || 0);
  if (!Number.isFinite(value) || value <= 0) {
    return "-";
  }
  return `${formatBytes(value)}/s`;
}

function formatBytes(value) {
  const bytes = Number(value || 0);
  if (bytes >= 1024 * 1024) {
    return `${(bytes / 1024 / 1024).toFixed(1)} MB`;
  }
  if (bytes >= 1024) {
    return `${(bytes / 1024).toFixed(1)} KB`;
  }
  return `${bytes} B`;
}

function parseJSON(payload) {
  try {
    return JSON.parse(payload || "{}");
  } catch (error) {
    return { ok: false, error: payload || "invalid json" };
  }
}

function setupStatusTabs() {
  const buttons = Array.from(document.querySelectorAll("[data-status-tab]"));
  if (!buttons.length) {
    return;
  }
  buttons.forEach((button) => {
    button.onclick = () => activateStatusTab(button.dataset.statusTab || "overview");
  });
  const refresh = byId("refreshLogsButton");
  if (refresh) {
    refresh.onclick = refreshLogs;
  }
  const clear = byId("clearLogsButton");
  if (clear) {
    clear.onclick = clearLogs;
  }
  const refreshConnectionsButton = byId("refreshConnectionsButton");
  if (refreshConnectionsButton) {
    refreshConnectionsButton.onclick = refreshConnections;
  }
  const selfCheck = byId("vpnSelfCheckButton");
  if (selfCheck) {
    selfCheck.onclick = runVPNSelfCheck;
  }
  activateStatusTab("overview");
  refreshVPNDiagnostics();
}

function activateStatusTab(tab) {
  const clean = ["logs", "connections"].includes(tab) ? tab : "overview";
  document.querySelectorAll("[data-status-tab]").forEach((button) => {
    const active = button.dataset.statusTab === clean;
    button.classList.toggle("active", active);
    button.setAttribute("aria-selected", active ? "true" : "false");
  });
  const overview = byId("statusOverviewPanel");
  const connections = byId("statusConnectionsPanel");
  const logs = byId("statusLogsPanel");
  if (overview) {
    overview.hidden = clean !== "overview";
  }
  if (connections) {
    connections.hidden = clean !== "connections";
  }
  if (logs) {
    logs.hidden = clean !== "logs";
  }
  if (clean === "connections") {
    refreshConnections();
  }
  if (clean === "logs") {
    refreshLogs();
  }
}

function refreshVRouteIfVisible() {
  const panel = byId("statusVRoutePanel");
  if (byId("vrouteStatus") && (!panel || !panel.hidden)) {
    refreshVRoute();
  }
}

function refreshConnectionsIfVisible() {
  const panel = byId("statusConnectionsPanel");
  if (panel && !panel.hidden) {
    refreshConnections();
  }
}

function refreshLogsIfVisible() {
  const logs = byId("statusLogsPanel");
  if (logs && !logs.hidden) {
    refreshLogs();
  }
}

function refreshVRoute() {
  if (!byId("vrouteStatus")) {
    return;
  }
  try {
    const vpnData = parseJSON(hasCloudHelper("vpnStatus") ? window.CloudHelper.vpnStatus() : "{}");
    renderVRouteStatus(vpnData);
  } catch (error) {
    setText("vrouteStatus", `读取虚拟路由失败：${error && error.message ? error.message : error}`);
  }
}

function renderVRouteStatus(vpnData) {
  const vroute = (vpnData && vpnData.vroute) || {};
  const config = vroute.config || {};
  const carriers = vroute.carriers || {};
  const capabilities = vroute.capabilities || {};
  const carrierItems = Array.isArray(carriers.items) ? carriers.items : [];
  const links = Array.isArray(vroute.links) ? vroute.links : [];
  const exitNodes = Array.isArray(config.exit_node_items) ? config.exit_node_items : [];
  updateVRouteNodeNames(config);
  const connectedLinks = links.filter(vrouteLinkConnected).length;
  const enabled = !!config.enabled;
  const error = String(config.error || carriers.last_error || "").trim();
  setText("vrouteStatus", [
    enabled ? "虚拟路由已启用" : "虚拟路由未启用",
    `拓扑 ${Number(config.topology_rules || 0)}`,
    `规则 ${Number(config.route_rules || 0)}`,
    `节点连接 ${connectedLinks}/${links.length}`,
    `Carrier ${Number(carriers.active || carrierItems.length || 0)}`,
    error ? `错误：${error}` : ""
  ].filter(Boolean).join("；"));
  renderVRouteHealth(enabled, error, config.updated_at, carriers.last_error_at);
  renderVRouteSummary(config, carriers, links);
  renderVRouteRTTTargets(config);
  renderVRouteLinks(links);
  renderVRouteExitNodes(exitNodes, config.exit_nodes);
  renderVRouteCarriers(carrierItems);
  renderVRouteCapabilities(capabilities);
}

function updateVRouteNodeNames(config) {
  const names = new Map();
  const localNodeID = String(config.local_node_id || "").trim();
  const localNodeName = String(config.local_node_name || "").trim();
  if (localNodeID) {
    names.set(localNodeID, localNodeName || localNodeID);
  }
  const items = Array.isArray(config.probe_items) ? config.probe_items : [];
  items.forEach((item) => {
    const nodeID = String(item.node_id || "").trim();
    const displayName = String(item.display_name || "").trim();
    if (nodeID) {
      names.set(nodeID, displayName || nodeID);
    }
  });
  vrouteNodeNames = names;
}

function vrouteNodeLabel(nodeID) {
  const clean = String(nodeID || "").trim();
  return vrouteNodeNames.get(clean) || clean || "-";
}

function vroutePathLabel(path) {
  return Array.isArray(path) ? path.map(vrouteNodeLabel).join(" > ") : "";
}

function renderVRouteRTTTargets(config) {
  const select = byId("vrouteRTTTarget");
  if (!select) {
    return;
  }
  const current = String(select.value || "").trim();
  const items = Array.isArray(config.probe_items) ? config.probe_items : [];
  select.innerHTML = "";
  const empty = document.createElement("option");
  empty.value = "";
  empty.textContent = items.length ? "选择目标节点" : "暂无可达节点";
  select.appendChild(empty);
  items.forEach((item) => {
    const nodeID = String(item.node_id || "").trim();
    if (!nodeID) {
      return;
    }
    const option = document.createElement("option");
    option.value = nodeID;
    const displayName = String(item.display_name || "").trim() || vrouteNodeLabel(nodeID);
    option.textContent = item.ip ? `${displayName} (${item.ip})` : displayName;
    select.appendChild(option);
  });
  if (current && items.some((item) => String(item.node_id || "").trim() === current)) {
    select.value = current;
  }
}

function runVRouteRTT() {
  const select = byId("vrouteRTTTarget");
  const targetNodeID = select ? String(select.value || "").trim() : "";
  if (!targetNodeID) {
    setText("vrouteRTTResult", "请选择目标节点");
    return;
  }
  const button = byId("vrouteRTTButton");
  if (button) {
    button.disabled = true;
    button.textContent = "测量中...";
  }
  setText("vrouteRTTResult", `正在测量到 ${vrouteNodeLabel(targetNodeID)} 的 RTT...`);
  try {
    const message = window.CloudHelper && window.CloudHelper.vroutePathRTT
      ? window.CloudHelper.vroutePathRTT(targetNodeID)
      : "RTT 接口不可用";
    if (message && !String(message).includes("已开始")) {
      setText("vrouteRTTResult", message);
      if (button) {
        button.disabled = false;
        button.textContent = "测量 RTT";
      }
    }
  } catch (error) {
    setText("vrouteRTTResult", `RTT 测量失败：${error && error.message ? error.message : error}`);
    if (button) {
      button.disabled = false;
      button.textContent = "测量 RTT";
    }
  }
}

function renderVRouteRTTResult(result) {
  const path = vroutePathLabel(result.path);
  if (!result.ok) {
    setText("vrouteRTTResult", `RTT 失败：${result.error || "未收到响应"}${path ? `；路径 ${path}` : ""}`);
    return;
  }
  setText("vrouteRTTResult", `${vrouteNodeLabel(result.target_node_id || result.responder)}：${Number(result.latency_ms || 0)} ms${path ? `；路径 ${path}` : ""}`);
}

function renderVRouteHealth(enabled, error, updatedAt, lastErrorAt) {
  const target = byId("vrouteHealth");
  if (!target) {
    return;
  }
  target.innerHTML = "";
  const state = document.createElement("div");
  state.className = "vroute-health";
  if (error) {
    state.classList.add("error");
  } else if (!enabled) {
    state.classList.add("off");
  }
  const title = document.createElement("strong");
  title.textContent = error ? "虚拟路由异常" : (enabled ? "虚拟路由正在接管匹配流量" : "虚拟路由尚未启用");
  const detail = document.createElement("span");
  detail.textContent = error
    ? `${error}${lastErrorAt ? `（${formatCompactTime(lastErrorAt)}）` : ""}`
    : (enabled
      ? `配置更新时间：${formatCompactTime(updatedAt) || "未知"}。承载连接会在规则需要时建立或重连。`
      : "请在主控为此节点启用虚拟路由并下发规则。");
  state.append(title, detail);
  target.appendChild(state);
}

function renderVRouteSummary(config, carriers, links) {
  const target = byId("vrouteSummary");
  if (!target) {
    return;
  }
  target.innerHTML = "";
  appendVRouteMetric(target, "本机节点", config.local_node_name || vrouteNodeLabel(config.local_node_id));
  appendVRouteMetric(target, "虚拟 IP", config.local_ip || "-");
  const exitNodes = Array.isArray(config.exit_nodes) ? config.exit_nodes : [];
  appendVRouteMetric(target, "Fake IP", [config.fake_ip_cidr || "-", `出口 ${exitNodes.length}`].join(" / "));
  appendVRouteMetric(target, "更新时间", formatCompactTime(config.updated_at) || "-");
  appendVRouteMetric(target, "节点连接", `${links.filter(vrouteLinkConnected).length} / ${links.length}`);
  appendVRouteMetric(target, "Carrier", `${Number(carriers.active || 0)} 活动`);
  appendVRouteMetric(target, "最近错误", carriers.last_error || config.error || "-");
}

function vrouteLinkConnected(item) {
  const bridge = (item && item.bridge_status) || {};
  const sessions = Array.isArray(item && item.bridge_sessions) ? item.bridge_sessions : [];
  return !!(item && item.next_state) ||
    Number(bridge.upstream_active || 0) > 0 ||
    Number(bridge.downstream_active || 0) > 0 ||
    sessions.some((session) => !session.closed);
}

function renderVRouteLinks(items) {
  const target = byId("vrouteLinks");
  if (!target) {
    return;
  }
  target.innerHTML = "";
  appendVRouteSectionTitle(target, `节点连接 (${items.length})`);
  if (!items.length) {
    appendVRouteEmpty(target, "当前没有与本机相连的虚拟路由拓扑节点。");
    return;
  }
  items.forEach((item) => {
    const runtime = item.virtual_router || {};
    const bridge = item.bridge_status || {};
    const sessions = Array.isArray(item.bridge_sessions) ? item.bridge_sessions : [];
    const session = sessions.find((value) => !value.closed) || sessions[0] || {};
    const nextState = item.next_state || {};
    const peerNodeID = String(item.next_node_id || item.prev_node_id || "-").trim();
    const peerNode = vrouteNodeLabel(peerNodeID);
    const connected = vrouteLinkConnected(item);
    const error = String(runtime.last_open_error || "").trim();
    const stateLabel = error ? "异常" : (connected ? "已连接" : "待连接");
    const endpoint = String(nextState.endpoint || session.remote_addr || "").trim();
    const protocol = vrouteProtocolLabel(nextState.selected_protocol || item.route_layer || "auto");
    const direction = item.next_node_id ? `本机 → ${peerNode}` : `${peerNode} → 本机`;
    const activityAt = session.last_frame_received_at || session.last_frame_sent_at || nextState.updated_at || bridge.updated_at;

    const card = document.createElement("article");
    card.className = `vroute-card ${error ? "error" : (connected ? "connected" : "pending")}`;
    const heading = document.createElement("div");
    heading.className = "vroute-card-heading";
    const title = document.createElement("div");
    title.className = "vroute-card-title";
    title.textContent = item.route_name ? `${peerNode} · ${item.route_name}` : peerNode;
    const state = document.createElement("span");
    state.className = `vroute-link-state ${error ? "error" : (connected ? "connected" : "pending")}`;
    state.textContent = stateLabel;
    heading.append(title, state);
    const meta = document.createElement("div");
    meta.className = "vroute-card-meta";
    meta.textContent = `${direction} · Carrier ${protocol}${endpoint ? ` · ${endpoint}` : ""}`;
    const stats = document.createElement("div");
    stats.className = "vroute-card-grid";
    appendVRouteMetric(stats, "Carrier", connected ? (endpoint || "已建立") : "未建立");
    appendVRouteMetric(stats, "连接方向", direction);
    appendVRouteMetric(stats, "最近活动", formatCompactTime(activityAt) || "-");
    appendVRouteMetric(stats, "Route ID", item.route_id || "-");
    if (error) {
      appendVRouteMetric(stats, "错误", error);
    }
    card.append(heading, meta, stats);
    target.appendChild(card);
  });
}

function renderVRouteExitNodes(items, fallbackIDs) {
  const target = byId("vrouteExitNodes");
  if (!target) {
    return;
  }
  target.innerHTML = "";
  const fallback = Array.isArray(fallbackIDs) ? fallbackIDs : [];
  const nodes = items.length ? items : fallback.map((nodeID) => ({ node_id: nodeID }));
  appendVRouteSectionTitle(target, `出口节点 (${nodes.length})`);
  if (!nodes.length) {
    appendVRouteEmpty(target, "当前规则没有指定远端出口节点；命中本机出口的规则会直接由本机处理。");
    return;
  }
  nodes.forEach((item) => {
    const card = document.createElement("article");
    card.className = "vroute-card";
    const title = document.createElement("div");
    title.className = "vroute-card-title";
    title.textContent = String(item.display_name || "").trim() || vrouteNodeLabel(item.node_id);
    const meta = document.createElement("div");
    meta.className = "vroute-card-meta";
    const host = String(item.ip || "").trim();
    const port = Number(item.service_port || 0);
    meta.textContent = host ? `地址 ${host}${port > 0 ? `:${port}` : ""}` : "未下发节点地址";
    card.append(title, meta);
    target.appendChild(card);
  });
}

function appendVRouteMetric(parent, label, value) {
  const item = document.createElement("div");
  item.className = "vroute-metric";
  const key = document.createElement("span");
  key.textContent = label;
  const val = document.createElement("strong");
  val.textContent = value || "-";
  item.append(key, val);
  parent.appendChild(item);
}

function renderVRouteCarriers(items) {
  const target = byId("vrouteCarriers");
  if (!target) {
    return;
  }
  target.innerHTML = "";
  appendVRouteSectionTitle(target, `承载连接 (${items.length})`);
  if (!items.length) {
    appendVRouteEmpty(target, "暂无活动虚拟路由承载。命中远端出口规则后会建立连接。");
    return;
  }
  items.forEach((item) => {
    const card = document.createElement("article");
    card.className = "vroute-card";
    if (item.last_error) {
      card.classList.add("error");
    }
    const title = document.createElement("div");
    title.className = "vroute-card-title";
    const carrierNodeID = item.exit_node || item.next_node;
    title.textContent = carrierNodeID ? `Carrier · ${vrouteNodeLabel(carrierNodeID)}` : (item.route_id || "Carrier");
    const meta = document.createElement("div");
    meta.className = "vroute-card-meta";
    meta.textContent = [
      item.layer ? `协议 ${vrouteProtocolLabel(item.layer)}` : "",
      item.relay ? `连接 ${item.relay}` : "",
      item.next_node ? `下一跳 ${vrouteNodeLabel(item.next_node)}` : "",
      item.exit_node ? `出口 ${vrouteNodeLabel(item.exit_node)}` : ""
    ].filter(Boolean).join(" · ");
    const stats = document.createElement("div");
    stats.className = "vroute-card-grid";
    const txIPFrames = Number(item.tx_ip_frames || 0);
    const txIPBytes = Number(item.tx_ip_bytes || 0);
    const rxIPFrames = Number(item.rx_ip_frames || 0);
    const rxIPBytes = Number(item.rx_ip_bytes || 0);
    const txControlFrames = Number(item.tx_control_frames || 0);
    const rxControlFrames = Number(item.rx_control_frames || 0);
    const tunWriteFrames = Number(item.tun_write_frames || 0);
    const tunWriteBytes = Number(item.tun_write_bytes || 0);
    appendVRouteMetric(stats, "TX业务", `${txIPFrames} / ${formatBytes(txIPBytes)}`);
    appendVRouteMetric(stats, "RX业务", `${rxIPFrames} / ${formatBytes(rxIPBytes)}`);
    appendVRouteMetric(stats, "控制帧", `TX ${txControlFrames} / RX ${rxControlFrames}`);
    appendVRouteMetric(stats, "TUN回写", `${tunWriteFrames} / ${formatBytes(tunWriteBytes)}`);
    appendVRouteMetric(stats, "路径", vroutePathLabel(item.path) || "-");
    appendVRouteMetric(stats, "Route ID", item.route_id || "-");
    appendVRouteMetric(stats, "活动", formatCompactTime(item.last_activity_at) || "-");
    if (item.last_error) {
      appendVRouteMetric(stats, "错误", item.last_error);
    }
    card.append(title, meta, stats);
    target.appendChild(card);
  });
}

function renderVRouteCapabilities(capabilities) {
  const target = byId("vrouteCapabilities");
  if (!target) {
    return;
  }
  target.innerHTML = "";
  appendVRouteSectionTitle(target, "移动端能力边界");
  const items = [
    ["IP帧", capabilities.ip_frame],
    ["IPv4", capabilities.ipv4],
    ["WebSocket", capabilities.websocket_carrier],
    ["HTTP/3", capabilities.websocket_h3],
    ["主动拨出", capabilities.outbound_dialer],
    ["入站监听", capabilities.inbound_listener],
    ["TUN回写", capabilities.vpn_tun_writeback],
    ["热刷新", capabilities.config_hot_refresh]
  ];
  const grid = document.createElement("div");
  grid.className = "vroute-capability-grid";
  items.forEach(([label, ok]) => {
    const chip = document.createElement("span");
    chip.className = ok ? "vroute-chip ok" : "vroute-chip off";
    chip.textContent = `${label} ${ok ? "on" : "off"}`;
    grid.appendChild(chip);
  });
  target.appendChild(grid);
}

function appendVRouteSectionTitle(parent, title) {
  const heading = document.createElement("div");
  heading.className = "vroute-section-title";
  heading.textContent = title;
  parent.appendChild(heading);
}

function appendVRouteEmpty(parent, text) {
  const empty = document.createElement("div");
  empty.className = "vroute-empty";
  empty.textContent = text;
  parent.appendChild(empty);
}

function vrouteProtocolLabel(value) {
  const text = String(value || "").trim().toLowerCase();
  if (!text || text === "default" || text === "auto") return "auto";
  if (["websocket", "ws", "wss", "http", "https", "http2", "h2"].includes(text)) return "http2";
  if (["websocket-h3", "ws-h3", "h3-websocket", "h3-ws", "h3", "http3", "quic"].includes(text)) return "http3";
  return text;
}

function refreshConnections() {
  const list = byId("connectionList");
  if (!list) {
    return;
  }
  try {
    const vpnData = parseJSON(window.CloudHelper.vpnStatus ? window.CloudHelper.vpnStatus() : "{}");
    renderConnections(vpnData);
  } catch (error) {
    setText("connectionStatus", `读取连接失败：${error && error.message ? error.message : error}`);
  }
}

function renderConnections(vpnData) {
  const list = byId("connectionList");
  if (!list) {
    return;
  }
  list.innerHTML = "";
  const connectionData = vpnData.connections || {};
  const active = Array.isArray(connectionData.active) ? connectionData.active : [];
  const completed = Array.isArray(connectionData.completed) ? connectionData.completed : [];
  const failures = Array.isArray(connectionData.failures) ? connectionData.failures : [];
  const rows = buildConnectionRows(active, completed, failures);
  const runtimeText = [
    isVPNRunning(vpnData) ? "VPN 运行中" : (isVPNStarting(vpnData) ? "VPN 启动中" : "VPN 未启动"),
    connectionData.fetched_at ? `刷新 ${formatCompactTime(connectionData.fetched_at)}` : ""
  ].filter(Boolean).join("；");
  setText("connectionStatus", `${runtimeText}；活动 ${active.length}；完成 ${completed.length}；失败 ${failures.length}`);
  if (!vpnData.ok) {
    const item = document.createElement("div");
    item.className = "status-box";
    item.textContent = vpnData.error || "VPN 状态不可用。";
    list.appendChild(item);
    return;
  }
  if (!rows.length) {
    const empty = document.createElement("div");
    empty.className = "status-box";
    empty.textContent = "暂无活动 VPN 连接。";
    list.appendChild(empty);
    return;
  }
  const table = document.createElement("div");
  table.className = "connection-table";
  table.setAttribute("role", "table");
  table.appendChild(renderConnectionHeader());
  rows.slice(0, 64).forEach((item) => table.appendChild(renderConnectionRow(item)));
  list.appendChild(table);
}

function buildConnectionRows(active, completed, failures) {
  const rows = new Map();
  const add = (item, state, index) => {
    const value = item || {};
    const target = value.target || value.route_target || "-";
    const key = value.flow_id || value.id || `${state}|${target}|${value.last_seen || value.closed_at || value.opened_at || index}`;
    const previous = rows.get(key) || {};
    if (state === "active") {
      rows.set(key, { ...value, error: "", reason: "", connection_state: state });
      return;
    }
    const clearedFailure = state === "completed" ? { error: "", reason: "" } : {};
    rows.set(key, { ...previous, ...value, ...clearedFailure, connection_state: state });
  };
  const historical = [
    ...completed.slice(0, 32).map((item, index) => ({ item, state: "completed", index, order: 0 })),
    ...failures.slice(0, 32).map((item, index) => ({ item, state: "failed", index, order: 1 }))
  ];
  historical.sort((left, right) => {
    const timeOrder = connectionTimestamp(left.item) - connectionTimestamp(right.item);
    return timeOrder !== 0 ? timeOrder : left.order - right.order;
  });
  historical.forEach((entry) => add(entry.item, entry.state, entry.index));
  active.forEach((item, index) => add(item, "active", index));
  const priority = { active: 0, failed: 1, completed: 2 };
  return Array.from(rows.values()).sort((left, right) => {
    const stateOrder = Number(priority[left.connection_state] || 0) - Number(priority[right.connection_state] || 0);
    if (stateOrder !== 0) return stateOrder;
    return connectionTimestamp(right) - connectionTimestamp(left);
  });
}

function connectionTimestamp(item) {
  const value = item.last_seen || item.closed_at || item.last_active || item.opened_at || "";
  const timestamp = Date.parse(value);
  return Number.isFinite(timestamp) ? timestamp : 0;
}

function renderConnectionHeader() {
  const row = document.createElement("div");
  row.className = "connection-row connection-header";
  row.setAttribute("role", "row");
  ["域名", "上行", "下行", "路由", "状态"].forEach((label) => {
    const cell = document.createElement("div");
    cell.setAttribute("role", "columnheader");
    cell.textContent = label;
    row.appendChild(cell);
  });
  return row;
}

function renderConnectionRow(item) {
  const row = document.createElement("div");
  row.className = `connection-row ${item.connection_state || ""}`.trim();
  row.setAttribute("role", "row");

  const domainCell = document.createElement("div");
  domainCell.className = "connection-domain";
  domainCell.setAttribute("role", "cell");
  const domain = document.createElement("strong");
  domain.textContent = connectionTargetHost(item.target || item.route_target || "-");
  const detail = document.createElement("span");
  detail.textContent = connectionRowDetail(item);
  domainCell.append(domain, detail);

  row.append(
    domainCell,
    renderConnectionValue(formatBytes(item.bytes_up)),
    renderConnectionValue(formatBytes(item.bytes_down)),
    renderConnectionValue(item.direct ? "直连" : "代理", "connection-route"),
    renderConnectionValue(connectionStateLabel(item.connection_state), `connection-state ${item.connection_state || ""}`)
  );
  return row;
}

function renderConnectionValue(value, className) {
  const cell = document.createElement("div");
  cell.className = className || "connection-value";
  cell.setAttribute("role", "cell");
  cell.textContent = value || "-";
  return cell;
}

function connectionTargetHost(target) {
  const value = String(target || "").trim();
  if (!value) return "-";
  if (value.startsWith("[")) {
    const end = value.indexOf("]");
    return end > 0 ? value.slice(1, end) : value;
  }
  const firstColon = value.indexOf(":");
  const lastColon = value.lastIndexOf(":");
  return firstColon > 0 && firstColon === lastColon ? value.slice(0, firstColon) : value;
}

function connectionRowDetail(item) {
  const target = String(item.target || item.route_target || "").trim();
  const host = connectionTargetHost(target);
  const error = String(item.error || item.reason || "").trim();
  return [
    target && target !== host ? target : "",
    String(item.transport || "").toUpperCase(),
    error
  ].filter(Boolean).join(" · ") || "-";
}

function connectionStateLabel(state) {
  const labels = { active: "活动", completed: "完成", failed: "失败" };
  return labels[String(state || "").trim()] || "-";
}

function refreshLogs() {
  const list = byId("logList");
  if (!list) {
    return;
  }
  try {
    renderLogs(window.CloudHelper.logs());
  } catch (error) {
    setText("logStatus", `读取日志失败：${error && error.message ? error.message : error}`);
  }
}

function clearLogs() {
  try {
    renderLogs(window.CloudHelper.clearLogs());
  } catch (error) {
    setText("logStatus", `清空日志失败：${error && error.message ? error.message : error}`);
  }
}

function renderLogs(payload) {
  const data = parseJSON(payload);
  const list = byId("logList");
  if (!list) {
    return;
  }
  list.innerHTML = "";
  const entries = Array.isArray(data.entries) ? data.entries : [];
  setText("logStatus", entries.length ? `共 ${entries.length} 条日志。` : "暂无日志。");
  if (!data.ok) {
    setText("logStatus", data.error || "日志不可用。");
    return;
  }
  entries.slice().reverse().forEach((entry) => {
    list.appendChild(renderLogItem(entry));
  });
}

function renderLogItem(entry) {
  const item = document.createElement("article");
  item.className = "log-item";
  const level = String(entry.level || "info").toLowerCase();
  item.classList.toggle("error", level === "error");
  item.classList.toggle("warn", level === "warn" || level === "warning");

  const meta = document.createElement("div");
  meta.className = "log-meta";
  meta.textContent = [formatLogTime(entry.time), entry.level || "info", entry.source || "android"].filter(Boolean).join(" · ");

  const message = document.createElement("div");
  message.className = "log-message";
  message.textContent = entry.message || "";
  item.append(meta, message);
  return item;
}

function formatLogTime(value) {
  if (!value) {
    return "";
  }
  const date = new Date(value);
  if (Number.isNaN(date.getTime())) {
    return value;
  }
  return date.toLocaleString();
}

function appendUILog(source, message) {
  if (!message || !window.CloudHelper || !window.CloudHelper.logEvent) {
    return;
  }
  try {
    window.CloudHelper.logEvent(source || "ui", String(message));
  } catch (_) {
  }
}

function runVPNSelfCheck() {
  const button = byId("vpnSelfCheckButton");
  if (button) {
    button.disabled = true;
    button.textContent = "检测中";
  }
  setStatus("正在执行 VPN 自检...");
  try {
    const message = window.CloudHelper.vpnSelfCheck ? window.CloudHelper.vpnSelfCheck() : "VPN 自检不可用";
    setStatus(message || "VPN 自检已开始");
  } catch (error) {
    setStatus(`VPN 自检失败：${error && error.message ? error.message : error}`);
    if (button) {
      button.disabled = false;
      button.textContent = "VPN 自检";
    }
  }
}

function refreshVPNDiagnostics() {
  if (!byId("summaryVpn")) {
    return;
  }
  try {
    renderVPNDiagnostics(parseJSON(window.CloudHelper.vpnStatus ? window.CloudHelper.vpnStatus() : "{}"));
  } catch (_) {
  }
}

let routeToggleBusy = false;

function setupRouteControl() {
  const toggle = byId("routeEnabled");
  if (!toggle) {
    return;
  }
  toggle.onchange = () => setRouteEnabled(toggle.checked);
  refreshRouteControl();
  setupVRoutePanel();
}

function setupVRoutePanel() {
  const refreshButton = byId("refreshVRouteButton");
  if (refreshButton) {
    refreshButton.onclick = refreshVRoute;
  }
  const rttButton = byId("vrouteRTTButton");
  if (rttButton) {
    rttButton.onclick = runVRouteRTT;
  }
  refreshVRoute();
}

function refreshRouteControl() {
  const toggle = byId("routeEnabled");
  if (!toggle) {
    return;
  }
  try {
    const data = parseJSON(window.CloudHelper && window.CloudHelper.vpnStatus ? window.CloudHelper.vpnStatus() : "{}");
    const running = isVPNRunning(data);
    const starting = isVPNStarting(data);
    toggle.checked = running || starting;
    toggle.disabled = routeToggleBusy;
    setText("routeState", running ? "已开启" : (starting ? "正在开启" : "已关闭"));
    setText("routeDetail", [
      running ? "VPN 路由正在接管本机流量。" : (starting ? "VPN 正在启动，请稍候。" : "开启后将通过 Android VPN 启用本机路由能力。"),
      data.android_error || data.last_error ? `错误：${data.android_error || data.last_error}` : ""
    ].filter(Boolean).join("；"));
  } catch (error) {
    toggle.checked = false;
    toggle.disabled = routeToggleBusy;
    setText("routeState", "状态不可用");
    setText("routeDetail", `读取 VPN 状态失败：${error && error.message ? error.message : error}`);
  }
}

function setRouteEnabled(enabled) {
  const toggle = byId("routeEnabled");
  if (!toggle || routeToggleBusy) {
    return;
  }
  const method = enabled ? "startVpn" : "stopVpn";
  if (!hasCloudHelper(method)) {
    toggle.checked = !enabled;
    setText("routeState", "不可用");
    setText("routeDetail", "Android VPN 控制不可用。");
    return;
  }
  routeToggleBusy = true;
  toggle.disabled = true;
  setText("routeState", enabled ? "正在开启" : "正在关闭");
  try {
    const message = callCloudHelperString(method, enabled ? "VPN 正在启动..." : "VPN 正在停止...");
    setText("routeDetail", message);
    appendUILog("route", message);
  } catch (error) {
    toggle.checked = !enabled;
    setText("routeState", "操作失败");
    setText("routeDetail", `路由切换失败：${error && error.message ? error.message : error}`);
  } finally {
    window.setTimeout(() => {
      routeToggleBusy = false;
      refreshRouteControl();
    }, 800);
  }
}

function setupInfoBox() {
  const composer = byId("infoBoxComposer");
  const refreshButton = byId("infoBoxRefreshButton");
  const clearButton = byId("infoBoxClearButton");
  if (!composer || !refreshButton || !clearButton) {
    return;
  }
  composer.addEventListener("submit", (event) => {
    event.preventDefault();
    sendInfoBoxMessage();
  });
  refreshButton.addEventListener("click", () => refreshInfoBox(false));
  clearButton.addEventListener("click", clearInfoBox);
  refreshInfoBox(false);
  infoBoxPollTimer = window.setInterval(() => {
    requestInfoBoxRefresh();
  }, 60000);
  document.addEventListener("visibilitychange", () => {
    if (document.visibilityState === "visible") requestInfoBoxRefresh();
  });
}

function setInfoBoxBusy(busy) {
  ["infoBoxRefreshButton", "infoBoxClearButton", "infoBoxSendButton", "infoBoxInput"].forEach((id) => {
    const element = byId(id);
    if (element) element.disabled = Boolean(busy);
  });
}

function setInfoBoxStatus(message, isError) {
  const status = byId("infoBoxStatus");
  if (!status) return;
  status.textContent = message || "";
  status.classList.toggle("error", Boolean(isError));
}

function refreshInfoBox(silent) {
  if (!hasCloudHelper("infoBoxRefresh") || infoBoxPendingAction) {
    if (!silent && !hasCloudHelper("infoBoxRefresh")) setInfoBoxStatus("信息框不可用。", true);
    return;
  }
  infoBoxPendingAction = silent ? "silent-refresh" : "refresh";
  if (!silent) {
    setInfoBoxBusy(true);
    setInfoBoxStatus("正在刷新...", false);
  }
  window.CloudHelper.infoBoxRefresh();
}

function requestInfoBoxRefresh() {
  if (!document.body || document.body.dataset.page !== "information") return;
  if (document.visibilityState !== "visible" || infoBoxPendingAction) {
    infoBoxRefreshPending = true;
    return;
  }
  infoBoxRefreshPending = false;
  refreshInfoBox(true);
}

function drainInfoBoxRefresh() {
  if (!infoBoxRefreshPending || infoBoxPendingAction || document.visibilityState !== "visible") return;
  infoBoxRefreshPending = false;
  window.setTimeout(() => refreshInfoBox(true), 0);
}

function sendInfoBoxMessage() {
  const input = byId("infoBoxInput");
  const message = input ? input.value.trim() : "";
  if (!message || infoBoxPendingAction || !hasCloudHelper("infoBoxSend")) return;
  infoBoxPendingAction = "send";
  setInfoBoxBusy(true);
  setInfoBoxStatus("正在发送...", false);
  window.CloudHelper.infoBoxSend(message);
}

function clearInfoBox() {
  if (infoBoxPendingAction || !hasCloudHelper("infoBoxClear")) return;
  if (!window.confirm("确定清空全部探针共享信息？")) return;
  infoBoxPendingAction = "clear";
  setInfoBoxBusy(true);
  setInfoBoxStatus("正在清空...", false);
  window.CloudHelper.infoBoxClear();
}

function completeInfoBoxRequest(data) {
  const action = infoBoxPendingAction;
  infoBoxPendingAction = "";
  setInfoBoxBusy(false);
  if (!data || data.ok !== true) {
    setInfoBoxStatus(`操作失败：${data && data.error ? data.error : "未知错误"}`, true);
    drainInfoBoxRefresh();
    return;
  }
  renderInfoBox(data);
  if (action === "send") {
    const input = byId("infoBoxInput");
    if (input) {
      input.value = "";
      input.focus();
    }
    setInfoBoxStatus("已发送", false);
  } else if (action === "clear") {
    setInfoBoxStatus("已清空", false);
  } else if (action !== "silent-refresh") {
    setInfoBoxStatus(`已刷新 ${Array.isArray(data.items) ? data.items.length : 0} 条`, false);
  }
  drainInfoBoxRefresh();
}

function renderInfoBox(data) {
  const list = byId("infoBoxList");
  if (!list) return;
  const items = Array.isArray(data && data.items) ? data.items : [];
  list.innerHTML = "";
  if (!items.length) {
    const empty = document.createElement("div");
    empty.className = "info-box-empty";
    empty.textContent = "暂无信息";
    list.appendChild(empty);
    return;
  }
  items.forEach((item) => {
    const button = document.createElement("button");
    button.type = "button";
    button.className = "info-box-item";
    button.title = "点击复制";
    const meta = document.createElement("div");
    meta.className = "info-box-meta";
    const author = document.createElement("strong");
    author.textContent = item.node_name ? `#${item.node_id} ${item.node_name}` : `#${item.node_id || "-"}`;
    const time = document.createElement("span");
    time.textContent = formatInfoBoxTime(item.created_at);
    const message = document.createElement("div");
    message.className = "info-box-message";
    message.textContent = item.message || "";
    meta.append(author, time);
    button.append(meta, message);
    button.addEventListener("click", () => copyInfoBoxText(item.message || ""));
    list.appendChild(button);
  });
  list.scrollTop = list.scrollHeight;
}

function formatInfoBoxTime(value) {
  const date = new Date(value || "");
  return Number.isNaN(date.getTime()) ? String(value || "") : date.toLocaleString();
}

async function copyInfoBoxText(text) {
  try {
    if (hasCloudHelper("copyText")) {
      window.CloudHelper.copyText(text);
    } else if (navigator.clipboard && navigator.clipboard.writeText) {
      await navigator.clipboard.writeText(text);
    } else {
      throw new Error("clipboard unavailable");
    }
    showToast("已复制", false);
  } catch (error) {
    showToast(`复制失败：${error && error.message ? error.message : error}`, true);
  }
}

function renderVPNDiagnostics(data) {
  const vpnRunning = isVPNRunning(data);
  const vpnStarting = isVPNStarting(data);
  const dns = data.dns || {};
  const selfCheck = data.self_check || {};
  setText("summaryVpn", [
    vpnRunning ? "运行中" : (vpnStarting ? "启动中" : "未启动"),
    data.android_message || "",
    data.updated_at ? formatCompactTime(data.updated_at) : "",
    data.last_error ? `错误：${data.last_error}` : "",
    data.android_error ? `Android：${data.android_error}` : ""
  ].filter(Boolean).join("；") || "-");
  setText("summaryDns", dns.enabled ? `${dns.listen || "198.18.0.1:53"}；${dns.fake_ip_cidr || "198.18.0.0/15"}` : "未接管");
  setText("summaryDnsCache", `Fake ${Number(dns.fake_ip_count || 0)} / Hint ${Number(dns.route_hint_count || 0)} / NAT ${Number(dns.real_nat_count || 0)}`);
  setText("summaryVpnSelfCheck", formatVPNSelfCheck(selfCheck));
}

function formatVPNSelfCheck(data) {
  if (!data || Object.keys(data).length === 0) {
    return "未执行";
  }
  const status = data.status || (data.ok ? "ready" : "failed");
  const route = data.route || {};
  const routeText = route.group ? `${route.group}${route.selected_chain_id ? ` / ${route.selected_chain_id}` : ""}` : "";
  return [
    data.ok ? "通过" : "未通过",
    status,
    routeText,
    data.error ? `错误：${data.error}` : "",
    data.duration_ms ? `${data.duration_ms}ms` : "",
    data.updated_at ? formatCompactTime(data.updated_at) : ""
  ].filter(Boolean).join("；");
}

function refreshSummary(config) {
  const data = config || readConfig();
  const runtime = callCloudHelperString("status", "Android bridge 未就绪");
  setText("summaryController", data.controllerUrl || "-");
  setText("summaryNodeId", data.nodeId || "-");
  setText("summaryReady", data.ready ? "已配置" : "未配置");
  setText("summaryRuntime", runtime);
  setText("summaryLocalVersion", data.localVersion || "-");
  setRuntimeStatus(`运行：${runtime}`);
  refreshVPNDiagnostics();
}

function setStatus(message) {
  setText("status", message);
  setText("settingsStatus", message);
  setRuntimeStatus(`运行：${callCloudHelperString("status", "Android bridge 未就绪")}`);
  refreshSummarySilent();
}

function setRuntimeStatus(message) {
  setText("runtimeStatus", message);
}

function setUpgradeStatus(message) {
  setText("upgradeStatus", message);
}

function startUpgradeStatusPolling() {
  stopUpgradeStatusPolling();
  refreshUpgradeStatus();
  upgradeStatusTimer = window.setInterval(refreshUpgradeStatus, 1000);
}

function stopUpgradeStatusPolling() {
  if (upgradeStatusTimer) {
    window.clearInterval(upgradeStatusTimer);
    upgradeStatusTimer = 0;
  }
}

function refreshUpgradeStatus() {
  if (!window.CloudHelper || !window.CloudHelper.upgradeStatus) {
    return;
  }
  try {
    renderUpgradeStatus(JSON.parse(window.CloudHelper.upgradeStatus() || "{}"));
  } catch (err) {
    setUpgradeStatus(`升级状态解析失败：${err && err.message ? err.message : err}`);
  }
}

function renderUpgradeStatus(data) {
  const percent = clampPercent(data && data.percent);
  const fill = byId("upgradeProgressFill");
  if (fill) {
    fill.style.width = `${percent}%`;
  }
  setText("upgradeState", data && data.state ? data.state : "-");
  setText("upgradePhase", data && data.phase ? data.phase : "-");
  setText("upgradePercent", `${percent}%`);
  const downloaded = Number(data && data.downloaded_bytes ? data.downloaded_bytes : 0);
  const total = Number(data && data.total_bytes ? data.total_bytes : 0);
  setText("upgradeDownload", total > 0 ? `${formatBytes(downloaded)} / ${formatBytes(total)}` : formatBytes(downloaded));
  setText("upgradeSpeed", formatSpeed(data && data.speed_bps));
  const current = data && data.current_version ? data.current_version : "";
  const latest = data && data.latest_version ? data.latest_version : "";
  setText("upgradeVersion", current && latest ? `${current} -> ${latest}` : (latest || current || "-"));
  if (data && data.message) {
    setUpgradeStatus(data.message);
  }
  const state = String(data && data.state || "").toLowerCase();
  setUpgradeButtonsDisabled(state === "running");
  if (state && state !== "running") {
    stopUpgradeStatusPolling();
  }
}

function setUpgradeButtonsDisabled(disabled) {
  ["directUpgradeButton", "controllerUpgradeButton"].forEach((id) => {
    const button = byId(id);
    if (button) {
      button.disabled = Boolean(disabled);
    }
  });
}

function clampPercent(value) {
  const n = Number(value);
  if (!Number.isFinite(n)) return 0;
  return Math.max(0, Math.min(100, Math.trunc(n)));
}

function formatBytes(value) {
  let n = Number(value);
  if (!Number.isFinite(n) || n <= 0) return "-";
  const units = ["B", "KB", "MB", "GB", "TB"];
  let unit = 0;
  while (n >= 1024 && unit < units.length - 1) {
    n /= 1024;
    unit += 1;
  }
  return `${unit === 0 ? Math.trunc(n) : n.toFixed(1)} ${units[unit]}`;
}

function formatSpeed(value) {
  const text = formatBytes(value);
  return text === "-" ? "-" : `${text}/s`;
}

function setText(id, message) {
  const element = byId(id);
  if (element) {
    element.textContent = message;
  }
}

function showSaveFeedback(isError, message) {
  const feedback = byId("saveFeedback");
  const saveButton = byId("saveButton");
  window.clearTimeout(saveFeedbackTimer);
  if (feedback) {
    feedback.classList.toggle("error", isError);
    feedback.textContent = message || `已保存 ${new Date().toLocaleTimeString()}`;
  }
  if (saveButton) {
    saveButton.textContent = isError ? "Failed" : "Saved";
    saveButton.classList.toggle("saved", !isError);
  }
  showToast(message || "配置已保存", isError);
  saveFeedbackTimer = window.setTimeout(() => {
    if (saveButton) {
      saveButton.textContent = "Save";
      saveButton.classList.remove("saved");
    }
  }, 1800);
}

function showToast(message, isError) {
  const toast = byId("toast");
  if (!toast) {
    return;
  }
  window.clearTimeout(toastTimer);
  toast.textContent = message;
  toast.style.background = isError ? "#991b1b" : "#166534";
  toast.classList.add("show");
  toastTimer = window.setTimeout(() => {
    toast.classList.remove("show");
  }, 1800);
}

function refreshSummarySilent() {
  try {
    const data = readConfig();
    const runtime = callCloudHelperString("status", "Android bridge 未就绪");
    setText("summaryController", data.controllerUrl || "-");
    setText("summaryNodeId", data.nodeId || "-");
    setText("summaryReady", data.ready ? "已配置" : "未配置");
    setText("summaryRuntime", runtime);
    setText("summaryLocalVersion", data.localVersion || "-");
    refreshVPNDiagnostics();
    refreshRouteControl();
    refreshVRouteIfVisible();
  } catch (_) {
  }
}

function formatCompactTime(value) {
  if (!value) {
    return "";
  }
  const date = new Date(value);
  if (Number.isNaN(date.getTime())) {
    return value;
  }
  return date.toLocaleTimeString();
}

function setupSettingsTabs() {
  const buttons = Array.from(document.querySelectorAll("[data-settings-tab]"));
  if (!buttons.length) {
    return;
  }
  buttons.forEach((button) => {
    button.addEventListener("click", () => activateSettingsTab(button.dataset.settingsTab));
  });
  activateSettingsTab("upgrade");
}

function activateSettingsTab(tab) {
  const clean = ["upgrade", "controller", "route"].includes(tab) ? tab : "upgrade";
  document.querySelectorAll("[data-settings-tab]").forEach((button) => {
    button.classList.toggle("active", button.dataset.settingsTab === clean);
  });
  document.querySelectorAll("[data-settings-panel]").forEach((panel) => {
    panel.hidden = panel.dataset.settingsPanel !== clean;
  });
  if (clean === "upgrade") {
    refreshUpgradeStatus();
  } else if (clean === "route") {
    loadRouteSettings();
  }
}

function loadRouteSettings() {
  const target = byId("routeSettingsGrid");
  if (!target) return;
  if (!hasCloudHelper("vrouteSettings")) {
    renderRouteSettings({ ok: false, error: "Android 路由设置接口不可用" });
    return;
  }
  try {
    renderRouteSettings(parseJSON(callCloudHelperString("vrouteSettings", "{}")));
  } catch (error) {
    renderRouteSettings({ ok: false, error: `读取路由设置失败：${error && error.message ? error.message : error}` });
  }
}

function renderRouteSettings(data) {
  const target = byId("routeSettingsGrid");
  if (!target) return;
  target.replaceChildren();
  routeSettingsState = {
    groups: Array.isArray(data && data.groups) ? data.groups : [],
    nodes: Array.isArray(data && data.nodes) ? data.nodes : []
  };
  if (!data || data.ok === false) {
    appendRouteSettingsEmpty(target, String(data && data.error || "路由设置不可用"));
    return;
  }
  if (!routeSettingsState.groups.length || !routeSettingsState.nodes.length) {
    appendRouteSettingsEmpty(target, !routeSettingsState.groups.length ? "暂无路由组" : "暂无可用出口节点");
    return;
  }
  routeSettingsState.groups.forEach((group, index) => {
    const item = document.createElement("div");
    item.className = "route-setting-item";
    const label = document.createElement("label");
    const selectID = `routeSettingExit${index}`;
    label.htmlFor = selectID;
    label.textContent = String(group.name || group.id || "路由组");
    const select = document.createElement("select");
    select.id = selectID;
    select.dataset.routeGroupId = String(group.id || "");
    routeSettingsState.nodes.forEach((node) => {
      const option = document.createElement("option");
      option.value = String(node.node_id || "");
      option.textContent = String(node.display_name || "未命名节点");
      option.selected = option.value === String(group.exit_node_id || "");
      select.appendChild(option);
    });
    item.append(label, select);
    target.appendChild(item);
  });
  setRouteSettingsFeedback("");
}

function appendRouteSettingsEmpty(target, message) {
  const empty = document.createElement("div");
  empty.className = "route-settings-empty";
  empty.textContent = message;
  target.appendChild(empty);
}

function saveRouteSettings() {
  if (!hasCloudHelper("saveVrouteSettings")) {
    setRouteSettingsFeedback("Android 路由设置保存接口不可用", true);
    return;
  }
  const exitNodes = {};
  document.querySelectorAll("[data-route-group-id]").forEach((select) => {
    const groupID = String(select.dataset.routeGroupId || "").trim();
    if (groupID) exitNodes[groupID] = String(select.value || "").trim();
  });
  const button = byId("saveRouteSettingsButton");
  if (button) button.disabled = true;
  try {
    const response = parseJSON(callCloudHelperString("saveVrouteSettings", "{}", JSON.stringify({ exit_nodes: exitNodes })));
    if (!response || response.ok === false) {
      throw new Error(String(response && response.error || "保存失败"));
    }
    renderRouteSettings(response);
    setRouteSettingsFeedback(String(response.warning || response.message || "路由设置已保存"), !!response.warning);
    appendUILog("route", "移动端路由设置已保存。");
  } catch (error) {
    setRouteSettingsFeedback(`保存失败：${error && error.message ? error.message : error}`, true);
  } finally {
    if (button) button.disabled = false;
  }
}

function setRouteSettingsFeedback(message, error) {
  const target = byId("routeSettingsFeedback");
  if (!target) return;
  target.textContent = message || "";
  target.classList.toggle("error", !!error);
}

function initPage() {
  const page = document.body.dataset.page || "status";
  const info = pages[page] || pages.status;
  setText("pageTitle", info[0]);
  setText("pageSubtitle", info[1]);
  document.querySelectorAll(".nav-button").forEach((item) => {
    item.classList.toggle("active", item.dataset.page === page);
  });
  loadConfig();
  if (page === "status") {
    setupStatusTabs();
  }
  if (page === "route") {
    setupRouteControl();
  }
  if (page === "information") {
    setupInfoBox();
  }
  if (page === "settings") {
    setupSettingsTabs();
  }
  setInterval(refreshSummarySilent, 5000);
}

document.addEventListener("DOMContentLoaded", () => {
  try {
    initPage();
  } catch (error) {
    handleBootFailure(error);
  }
});

window.addEventListener("error", (event) => {
  handleBootFailure(event && event.error ? event.error : event && event.message ? event.message : "页面加载失败");
});

window.addEventListener("unhandledrejection", (event) => {
  handleBootFailure(event && event.reason ? event.reason : "页面加载失败");
});

function handleBootFailure(error) {
  const message = error && error.message ? error.message : String(error || "页面加载失败");
  if (!bootErrorLogged) {
    bootErrorLogged = true;
  }
  const status = byId("status") || byId("runtimeStatus") || byId("settingsStatus") || byId("linkStatus") || byId("infoBoxStatus");
  if (status) {
    status.textContent = `启动失败：${message}`;
  }
  const root = document.body;
  if (root) {
    root.dataset.bootError = "1";
  }
  try {
    if (window.CloudHelper && window.CloudHelper.loadConfig) {
      appendUILog("ui", `启动失败：${message}`);
    }
  } catch (_) {
  }
}
