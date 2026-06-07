let saveFeedbackTimer = 0;
let toastTimer = 0;
let upgradeStatusTimer = 0;
const proxyGroupExpanded = new Set();

const pages = {
  status: ["状态", "当前 Android 节点配置与运行状态。"],
  link: ["链路", "查看链路入口，并执行真实 relay 延迟与测速测试。"],
  proxy: ["VNet", "启动或停止 Android VNet 运行时。"],
  settings: ["设置", "配置主控与节点密钥，并执行直连或 VNet 升级。"]
};

window.CloudHelperUI = {
  setStatus(message) {
    setStatus(message || "");
    setUpgradeStatus(message || "");
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
      button.textContent = "VNet 自检";
    }
    refreshConnectionsIfVisible();
    refreshLogsIfVisible();
  }
};

function byId(id) {
  return document.getElementById(id);
}

function readConfig() {
  return JSON.parse(window.CloudHelper.loadConfig() || "{}");
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

function startCore() {
  const result = window.CloudHelper.startProxy();
  appendUILog("proxy", result);
  setRuntimeStatus(`启动：${result}`);
  refreshSummary();
  refreshProxyGroups();
  window.setTimeout(refreshProxyGroups, 1200);
  window.setTimeout(refreshProxyGroups, 3000);
}

function stopCore() {
  const result = window.CloudHelper.stopProxy();
  appendUILog("proxy", result);
  setRuntimeStatus(`停止：${result}`);
  refreshSummary();
  refreshProxyGroups();
  window.setTimeout(refreshProxyGroups, 1200);
}

function checkUpgrade(mode) {
  if (!saveConfig(false)) {
    return;
  }
  const modeLabel = mode === "proxy" ? "VNet" : "Direct";
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

function refreshProxyGroups() {
  const list = byId("proxyGroupList");
  if (!list) {
    return;
  }
  setRuntimeStatus("正在读取线路组...");
  try {
    renderProxyGroups(window.CloudHelper.proxyStatus(), window.CloudHelper.vpnStatus ? window.CloudHelper.vpnStatus() : "{}");
  } catch (error) {
    setRuntimeStatus(`读取线路组失败：${error && error.message ? error.message : error}`);
  }
}

function renderProxyGroups(payload, vpnPayload) {
  const data = parseJSON(payload);
  const vpnData = parseJSON(vpnPayload || "{}");
  const list = byId("proxyGroupList");
  if (!list) {
    return;
  }
  list.innerHTML = "";
  if (!data.ok) {
    setRuntimeStatus(data.error || "线路组状态不可用。");
    return;
  }
  renderProxyRuntimeStatus(data, vpnData);
  const groups = sortProxyGroups(Array.isArray(data.groups) ? data.groups : []);
  const chains = Array.isArray(data.chains) ? data.chains : [];
  if (!groups.length) {
    const empty = document.createElement("div");
    empty.className = "status-box";
    empty.textContent = "暂无线路组配置，请先在设置中刷新配置。";
    list.appendChild(empty);
    return;
  }
  groups.forEach((group) => list.appendChild(renderProxyGroupItem(group, chains)));
}

function renderProxyRuntimeStatus(data, vpnData) {
  const vpnRunning = !!(vpnData.running || vpnData.status === "running");
  const httpRunning = !!data.http_enabled;
  const socksRunning = !!data.socks5_enabled;
  const errors = [data.last_error, vpnData.last_error].filter(Boolean).join("；");
  const text = [
    `VNet：${vpnRunning ? "运行中" : "未启动"}`,
    `HTTP：${httpRunning ? data.http_addr || "运行中" : "未启动"}`,
    `SOCKS5：${socksRunning ? data.socks5_addr || "运行中" : "未启动"}`,
    errors ? `错误：${errors}` : ""
  ].filter(Boolean).join("；");
  setRuntimeStatus(text);
}

function renderProxyGroupItem(group, chains) {
  const item = document.createElement("article");
  item.className = "proxy-group-item";
  const groupKey = group.group || "fallback";
  item.dataset.group = groupKey;
  const expanded = proxyGroupExpanded.has(proxyGroupStorageKey(groupKey));

  const header = document.createElement("button");
  header.className = "proxy-group-header";
  header.type = "button";
  header.setAttribute("aria-expanded", expanded ? "true" : "false");
  header.onclick = () => toggleProxyGroup(groupKey);

  const heading = document.createElement("span");
  heading.className = "proxy-group-heading";
  const title = document.createElement("span");
  title.className = "link-title";
  title.textContent = groupKey;

  const meta = document.createElement("span");
  meta.className = "link-meta";
  meta.textContent = `当前：${formatProxyAction(group.action)}${group.selected_chain_id ? ` · ${chainNameById(chains, group.selected_chain_id)}` : ""}`;
  heading.append(title, meta);

  const toggle = document.createElement("span");
  toggle.className = "proxy-group-toggle";
  toggle.textContent = expanded ? "收起" : "展开";
  header.append(heading, toggle);

  const result = document.createElement("div");
  result.className = "link-result";
  result.textContent = "点击选项立即生效";

  const options = document.createElement("div");
  options.className = "proxy-option-grid";
  options.appendChild(renderProxyOption(item, group, "direct", "", "直连", "不走链路"));
  chains.forEach((entry) => {
    const id = entry.chain_id || entry.client_entry_id || "";
    if (!id) {
      return;
    }
    options.appendChild(renderProxyOption(item, group, "tunnel", id, entry.name || id, entry.relay_chain_id ? `Relay ${entry.relay_chain_id}` : id));
  });
  options.appendChild(renderProxyOption(item, group, "reject", "", "拒绝", "阻断命中流量"));

  const body = document.createElement("div");
  body.className = "proxy-group-body";
  body.hidden = !expanded;
  body.append(options, result);

  item.append(header, body);
  return item;
}

function sortProxyGroups(groups) {
  return groups.slice().sort((left, right) => {
    const leftFallback = String(left.group || "fallback").toLowerCase() === "fallback";
    const rightFallback = String(right.group || "fallback").toLowerCase() === "fallback";
    if (leftFallback === rightFallback) {
      return 0;
    }
    return leftFallback ? 1 : -1;
  });
}

function toggleProxyGroup(group) {
  const key = proxyGroupStorageKey(group);
  if (proxyGroupExpanded.has(key)) {
    proxyGroupExpanded.delete(key);
  } else {
    proxyGroupExpanded.add(key);
  }
  refreshProxyGroups();
}

function proxyGroupStorageKey(group) {
  return String(group || "fallback").trim().toLowerCase() || "fallback";
}

function renderProxyOption(panel, group, action, selectedChainId, label, subLabel) {
  const currentAction = (group.action || "direct").toLowerCase();
  const currentChain = String(group.selected_chain_id || "").trim().toLowerCase();
  const optionChain = String(selectedChainId || "").trim().toLowerCase();
  const active = currentAction === action && (action !== "tunnel" || currentChain === optionChain);
  const button = document.createElement("button");
  button.className = "proxy-option";
  button.classList.toggle("active", active);
  button.type = "button";
  button.onclick = () => saveProxyGroup(group.group || "fallback", action, selectedChainId || "", panel);

  const mark = document.createElement("span");
  mark.className = "proxy-radio";
  const text = document.createElement("span");
  text.className = "proxy-option-text";
  const main = document.createElement("span");
  main.className = "proxy-option-main";
  main.textContent = label;
  const sub = document.createElement("span");
  sub.className = "proxy-option-sub";
  sub.textContent = subLabel || "";
  text.append(main, sub);
  button.append(mark, text);
  return button;
}

function saveProxyGroup(group, action, selectedChainId, item) {
  const result = item ? item.querySelector(".link-result") : null;
  if (result) {
    result.textContent = "正在保存...";
    result.classList.remove("error");
  }
  if (action === "tunnel" && !selectedChainId) {
    if (result) {
      result.textContent = "请选择链路";
      result.classList.add("error");
    }
    return;
  }
  const payload = parseJSON(window.CloudHelper.proxySetGroup(group, action, selectedChainId || ""));
  if (!payload.ok) {
    if (result) {
      result.textContent = payload.error || "保存失败";
      result.classList.add("error");
    }
    return;
  }
  if (result) {
    result.textContent = "已保存";
  }
  appendUILog("proxy", `线路组已保存：${group} -> ${formatProxyAction(action)} ${selectedChainId || ""}`.trim());
  refreshProxyGroups();
}

function formatProxyAction(action) {
  switch ((action || "direct").toLowerCase()) {
    case "tunnel":
      return "链路";
    case "reject":
      return "拒绝";
    default:
      return "直连";
  }
}

function chainNameById(chains, id) {
  const clean = String(id || "").trim().toLowerCase();
  const item = chains.find((entry) => {
    return [entry.chain_id, entry.client_entry_id, entry.relay_chain_id].some((value) => String(value || "").trim().toLowerCase() === clean);
  });
  if (!item) {
    return id;
  }
  return item.name || item.chain_id || id;
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

function refreshConnections() {
  const list = byId("connectionList");
  if (!list) {
    return;
  }
  try {
    const proxyData = parseJSON(window.CloudHelper.proxyStatus ? window.CloudHelper.proxyStatus() : "{}");
    const vpnData = parseJSON(window.CloudHelper.vpnStatus ? window.CloudHelper.vpnStatus() : "{}");
    renderConnections(proxyData, vpnData);
  } catch (error) {
    setText("connectionStatus", `读取连接失败：${error && error.message ? error.message : error}`);
  }
}

function renderConnections(proxyData, vpnData) {
  const list = byId("connectionList");
  if (!list) {
    return;
  }
  list.innerHTML = "";
  const connectionData = proxyData.connections || {};
  const active = Array.isArray(connectionData.active) ? connectionData.active : [];
  const completed = Array.isArray(connectionData.completed) ? connectionData.completed : [];
  const failures = Array.isArray(connectionData.failures) ? connectionData.failures : [];
  const runtimeText = [
    vpnData.running || vpnData.status === "running" ? "VNet 运行中" : "VNet 未启动",
    proxyData.http_enabled ? `HTTP ${proxyData.http_addr || ""}`.trim() : "HTTP 未启动",
    proxyData.socks5_enabled ? `SOCKS5 ${proxyData.socks5_addr || ""}`.trim() : "SOCKS5 未启动",
    connectionData.fetched_at ? `刷新 ${formatCompactTime(connectionData.fetched_at)}` : ""
  ].filter(Boolean).join("；");
  setText("connectionStatus", `${runtimeText}；活动 ${active.length}；完成 ${completed.length}；失败 ${failures.length}`);
  if (!proxyData.ok) {
    const item = document.createElement("div");
    item.className = "status-box";
    item.textContent = proxyData.error || "VNet 状态不可用。";
    list.appendChild(item);
    return;
  }
  if (!active.length && !completed.length && !failures.length) {
    const empty = document.createElement("div");
    empty.className = "status-box";
    empty.textContent = "暂无活动 VNet 连接。打开 VNet 或本地 HTTP/SOCKS 通道后，新连接会显示在这里。";
    list.appendChild(empty);
    return;
  }
  const tcpActive = active.filter((item) => String(item.transport || "").toLowerCase() !== "udp" && String(item.scope || "").toLowerCase() !== "vpn_udp");
  const udpActive = active.filter((item) => String(item.transport || "").toLowerCase() === "udp" || String(item.scope || "").toLowerCase() === "vpn_udp");
  appendConnectionSection(list, "TCP Relay", tcpActive, "暂无 TCP relay 连接");
  appendConnectionSection(list, "UDP Bridge", udpActive, "暂无 UDP bridge 连接");
  if (completed.length) {
    appendConnectionSection(list, "最近完成", completed.slice(0, 8), "暂无完成记录", false, true);
  }
  if (failures.length) {
    appendConnectionSection(list, "最近失败", failures.slice(0, 8), "暂无失败记录", true, false);
  }
}

function appendConnectionSection(list, title, items, emptyText, isFailure, isCompleted) {
  const heading = document.createElement("div");
  heading.className = "connection-section-title";
  heading.textContent = `${title} (${items.length})`;
  list.appendChild(heading);
  if (!items.length) {
    const empty = document.createElement("div");
    empty.className = "connection-empty";
    empty.textContent = emptyText;
    list.appendChild(empty);
    return;
  }
  items.forEach((item) => list.appendChild(renderConnectionItem(item, !!isFailure, !!isCompleted)));
}

function renderConnectionItem(item, isFailure, isCompleted) {
  const card = document.createElement("article");
  card.className = "connection-card";
  card.classList.toggle("error", !!isFailure);
  card.classList.toggle("completed", !!isCompleted);

  const title = document.createElement("div");
  title.className = "connection-title";
  const name = document.createElement("span");
  name.textContent = item.flow_id || item.id || "-";
  const badge = document.createElement("span");
  badge.className = isFailure ? "connection-badge error" : "connection-badge";
  badge.textContent = isFailure ? (item.reason || "失败") : (isCompleted ? (item.close_reason || "closed") : (item.transport || "stream"));
  title.append(name, badge);

  const meta = document.createElement("div");
  meta.className = "connection-meta";
  meta.textContent = [
    item.scope || "-",
    item.side || "-",
    item.direct ? "direct" : (item.group || "tunnel"),
    item.chain_id ? `chain ${item.chain_id}` : ""
  ].filter(Boolean).join(" · ");

  const path = document.createElement("div");
  path.className = "connection-path";
  path.textContent = `${item.target || "-"} -> ${item.route_target || item.target || "-"}`;

  const grid = document.createElement("div");
  grid.className = "connection-grid";
  if (isFailure) {
    appendConnectionMetric(grid, "类型", item.kind || "-");
    appendConnectionMetric(grid, "错误", item.error || item.reason || "-");
    appendConnectionMetric(grid, "链路", item.direct ? "direct" : (item.chain_id || item.group || "-"));
    appendConnectionMetric(grid, "时间", formatCompactTime(item.last_seen) || "-");
  } else {
    appendConnectionMetric(grid, "上行", formatBytes(item.bytes_up));
    appendConnectionMetric(grid, "下行", formatBytes(item.bytes_down));
    appendConnectionMetric(grid, "写次数", `${Number(item.writes_up || 0)}/${Number(item.writes_down || 0)}`);
    appendConnectionMetric(grid, isCompleted ? "持续" : "空闲", isCompleted ? formatDurationSeconds(item.duration_ms) : formatDurationSeconds(item.idle_ms));
    appendConnectionMetric(grid, "阻塞", formatConnectionBlock(item));
    appendConnectionMetric(grid, "最近阻塞", formatLastConnectionBlock(item));
    if (isCompleted) {
      appendConnectionMetric(grid, "结束", [item.close_reason || "-", formatCompactTime(item.closed_at)].filter(Boolean).join(" "));
    }
  }
  card.append(title, meta, path, grid);
  return card;
}

function appendConnectionMetric(parent, label, value) {
  const box = document.createElement("div");
  box.className = "connection-metric";
  const key = document.createElement("span");
  key.textContent = label;
  const val = document.createElement("strong");
  val.textContent = value || "-";
  box.append(key, val);
  parent.appendChild(box);
}

function formatDurationSeconds(value) {
  const ms = Number(value || 0);
  if (!Number.isFinite(ms) || ms <= 0) {
    return "-";
  }
  return `${Math.round(ms / 1000)}s`;
}

function formatConnectionBlock(item) {
  const blockedUp = Number(item.blocked_writes_up || 0);
  const blockedDown = Number(item.blocked_writes_down || 0);
  const maxBlock = Math.max(Number(item.max_write_block_ms_up || 0), Number(item.max_write_block_ms_down || 0));
  const totalBlock = Number(item.write_block_ms_up || 0) + Number(item.write_block_ms_down || 0);
  if (!blockedUp && !blockedDown && !maxBlock) {
    return "-";
  }
  return `${blockedUp}/${blockedDown} max ${formatDurationText(maxBlock)} total ${formatDurationText(totalBlock)}`;
}

function formatLastConnectionBlock(item) {
  const side = String(item.last_congestion_side || "").trim();
  const at = formatCompactTime(item.last_write_blocked_at || "");
  const up = Number(item.last_write_block_ms_up || 0);
  const down = Number(item.last_write_block_ms_down || 0);
  const last = Math.max(up, down);
  if (!side && !at && !last) {
    return "-";
  }
  return [side || "-", formatDurationText(last), at].filter(Boolean).join(" ");
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
  setStatus("正在执行 VNet 自检...");
  try {
    const message = window.CloudHelper.vpnSelfCheck ? window.CloudHelper.vpnSelfCheck() : "VNet 自检不可用";
    setStatus(message || "VNet 自检已开始");
  } catch (error) {
    setStatus(`VNet 自检失败：${error && error.message ? error.message : error}`);
    if (button) {
      button.disabled = false;
      button.textContent = "VNet 自检";
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

function renderVPNDiagnostics(data) {
  const vpnRunning = !!(data.running || data.status === "running");
  const dns = data.dns || {};
  const selfCheck = data.self_check || {};
  setText("summaryVpn", [
    vpnRunning ? "运行中" : "未启动",
    data.updated_at ? formatCompactTime(data.updated_at) : "",
    data.last_error ? `错误：${data.last_error}` : ""
  ].filter(Boolean).join("；") || "-");
  setText("summaryDns", dns.enabled ? `${dns.listen || "10.111.0.2:53"}；${dns.fake_ip_cidr || "198.18.0.0/15"}` : "未接管");
  setText("summaryDnsCache", `Fake ${Number(dns.fake_ip_count || 0)} / Hint ${Number(dns.route_hint_count || 0)}`);
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
  setText("summaryController", data.controllerUrl || "-");
  setText("summaryNodeId", data.nodeId || "-");
  setText("summaryReady", data.ready ? "已配置" : "未配置");
  setText("summaryRuntime", window.CloudHelper.status());
  setText("summaryLocalVersion", data.localVersion || "-");
  setRuntimeStatus(`运行：${window.CloudHelper.status()}`);
  refreshVPNDiagnostics();
}

function setStatus(message) {
  setText("status", message);
  setText("settingsStatus", message);
  setRuntimeStatus(`运行：${window.CloudHelper.status()}`);
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
  ["directUpgradeButton", "proxyUpgradeButton"].forEach((id) => {
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
    setText("summaryController", data.controllerUrl || "-");
    setText("summaryNodeId", data.nodeId || "-");
    setText("summaryReady", data.ready ? "已配置" : "未配置");
    setText("summaryRuntime", window.CloudHelper.status());
    setText("summaryLocalVersion", data.localVersion || "-");
    refreshVPNDiagnostics();
    refreshConnectionsIfVisible();
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
  activateSettingsTab("controller");
}

function activateSettingsTab(tab) {
  const clean = tab === "upgrade" ? "upgrade" : "controller";
  document.querySelectorAll("[data-settings-tab]").forEach((button) => {
    button.classList.toggle("active", button.dataset.settingsTab === clean);
  });
  document.querySelectorAll("[data-settings-panel]").forEach((panel) => {
    panel.hidden = panel.dataset.settingsPanel !== clean;
  });
  if (clean === "upgrade") {
    refreshUpgradeStatus();
  }
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
  if (page === "link") {
    refreshLinks();
  }
  if (page === "proxy") {
    refreshProxyGroups();
  }
  if (page === "settings") {
    setupSettingsTabs();
  }
  setInterval(refreshSummarySilent, 5000);
}

document.addEventListener("DOMContentLoaded", initPage);
