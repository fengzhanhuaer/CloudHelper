let saveFeedbackTimer = 0;
let toastTimer = 0;
let upgradeStatusTimer = 0;
const proxyGroupExpanded = new Set();

const pages = {
  status: ["状态", "当前 Android 节点配置与运行状态。"],
  link: ["链路", "查看链路入口，并执行真实 relay 延迟与测速测试。"],
  proxy: ["代理", "启动或停止 Android 代理运行时。"],
  settings: ["设置", "配置主控与节点密钥，并执行直连或主控代理升级。"]
};

window.CloudHelperUI = {
  setStatus(message) {
    setStatus(message || "");
    setUpgradeStatus(message || "");
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
  const text = `正在检查 ${mode} 升级...`;
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
  setRuntimeStatus("正在读取代理组...");
  try {
    renderProxyGroups(window.CloudHelper.proxyStatus(), window.CloudHelper.vpnStatus ? window.CloudHelper.vpnStatus() : "{}");
  } catch (error) {
    setRuntimeStatus(`读取代理组失败：${error && error.message ? error.message : error}`);
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
    setRuntimeStatus(data.error || "代理组状态不可用。");
    return;
  }
  renderProxyRuntimeStatus(data, vpnData);
  const groups = sortProxyGroups(Array.isArray(data.groups) ? data.groups : []);
  const chains = Array.isArray(data.chains) ? data.chains : [];
  if (!groups.length) {
    const empty = document.createElement("div");
    empty.className = "status-box";
    empty.textContent = "暂无代理组配置，请先在设置中刷新配置。";
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
    `VPN：${vpnRunning ? "运行中" : "未启动"}`,
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
  appendUILog("proxy", `代理组已保存：${group} -> ${formatProxyAction(action)} ${selectedChainId || ""}`.trim());
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
  const label = protocol ? protocol : "auto";
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
  const speedAuto = document.createElement("button");
  speedAuto.className = "command secondary";
  speedAuto.textContent = "测速";
  speedAuto.disabled = !chainId || chain.status !== "configured";
  speedAuto.onclick = () => runLinkSpeed(chainId, "");
  actions.appendChild(latency);
  actions.appendChild(speedAuto);
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
    setLinkPanelStatus(data.chain_id, text, !data.ok);
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
  const panel = findLinkPanel(chainId);
  if (!panel) {
    return;
  }
  const result = panel.querySelector(".link-result");
  if (!result) {
    return;
  }
  result.textContent = message;
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
  const mbps = data.rate_bps ? ((data.rate_bps * 8) / 1000 / 1000).toFixed(2) : "0.00";
  const details = Array.isArray(data.results)
    ? data.results.map((item) => `${item.protocol}:${item.ok ? `${formatBytes(item.bytes)}/${item.duration_ms}ms` : item.error || "fail"}`).join("；")
    : "";
  return `测速：${data.chain_name || data.chain_id} ${data.status}，${mbps} Mbps。${details}`;
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
  const selfCheck = byId("vpnSelfCheckButton");
  if (selfCheck) {
    selfCheck.onclick = runVPNSelfCheck;
  }
  activateStatusTab("overview");
  refreshVPNDiagnostics();
}

function activateStatusTab(tab) {
  const clean = tab === "logs" ? "logs" : "overview";
  document.querySelectorAll("[data-status-tab]").forEach((button) => {
    const active = button.dataset.statusTab === clean;
    button.classList.toggle("active", active);
    button.setAttribute("aria-selected", active ? "true" : "false");
  });
  const overview = byId("statusOverviewPanel");
  const logs = byId("statusLogsPanel");
  if (overview) {
    overview.hidden = clean !== "overview";
  }
  if (logs) {
    logs.hidden = clean !== "logs";
  }
  if (clean === "logs") {
    refreshLogs();
  }
}

function refreshLogsIfVisible() {
  const logs = byId("statusLogsPanel");
  if (logs && !logs.hidden) {
    refreshLogs();
  }
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
  if (state && state !== "running") {
    stopUpgradeStatusPolling();
  }
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
  setInterval(refreshSummarySilent, 5000);
}

document.addEventListener("DOMContentLoaded", initPage);
