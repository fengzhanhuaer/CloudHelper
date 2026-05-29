let saveFeedbackTimer = 0;
let toastTimer = 0;

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
  },
  setLinkStatus(payload) {
    renderLinkResult(payload || "");
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
    setStatus(`已保存。状态：${status}`);
    if (showFeedback) {
      showSaveFeedback(false);
    }
    return true;
  } catch (error) {
    const message = `保存失败：${error && error.message ? error.message : error}`;
    setStatus(message);
    showSaveFeedback(true, message);
    return false;
  }
}

function startCore() {
  const result = window.CloudHelper.startProxy();
  setRuntimeStatus(`启动：${result}`);
  refreshSummary();
  refreshProxyGroups();
}

function stopCore() {
  const result = window.CloudHelper.stopProxy();
  setRuntimeStatus(`停止：${result}`);
  refreshSummary();
  refreshProxyGroups();
}

function checkUpgrade(mode) {
  if (!saveConfig(false)) {
    return;
  }
  const text = `正在检查 ${mode} 升级...`;
  setUpgradeStatus(text);
  setStatus(text);
  window.CloudHelper.checkUpgrade(mode);
}

function refreshConfig() {
  if (!saveConfig(false)) {
    return;
  }
  const text = "正在从主控刷新配置...";
  setUpgradeStatus(text);
  setStatus(text);
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
    renderProxyGroups(window.CloudHelper.proxyStatus());
  } catch (error) {
    setRuntimeStatus(`读取代理组失败：${error && error.message ? error.message : error}`);
  }
}

function renderProxyGroups(payload) {
  const data = parseJSON(payload);
  const list = byId("proxyGroupList");
  if (!list) {
    return;
  }
  list.innerHTML = "";
  if (!data.ok) {
    setRuntimeStatus(data.error || "代理组状态不可用。");
    return;
  }
  const vpn = data.running || data.status === "running" || data.http_enabled || data.socks5_enabled;
  setRuntimeStatus(`VPN：${vpn ? "运行中" : "未运行"}；HTTP ${data.http_addr || "-"}；SOCKS5 ${data.socks5_addr || "-"}`);
  const groups = Array.isArray(data.groups) ? data.groups : [];
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

function renderProxyGroupItem(group, chains) {
  const item = document.createElement("article");
  item.className = "proxy-group-item";
  item.dataset.group = group.group || "fallback";

  const title = document.createElement("div");
  title.className = "link-title";
  title.textContent = group.group || "fallback";

  const meta = document.createElement("div");
  meta.className = "link-meta";
  meta.textContent = `当前：${formatProxyAction(group.action)}${group.selected_chain_id ? ` · ${chainNameById(chains, group.selected_chain_id)}` : ""}`;

  const controls = document.createElement("div");
  controls.className = "proxy-controls";

  const action = document.createElement("select");
  action.className = "proxy-select";
  [
    ["direct", "直连"],
    ["tunnel", "链路"],
    ["reject", "拒绝"]
  ].forEach(([value, label]) => {
    const option = document.createElement("option");
    option.value = value;
    option.textContent = label;
    option.selected = (group.action || "direct") === value;
    action.appendChild(option);
  });

  const chain = document.createElement("select");
  chain.className = "proxy-select";
  const empty = document.createElement("option");
  empty.value = "";
  empty.textContent = "选择链路";
  chain.appendChild(empty);
  chains.forEach((entry) => {
    const id = entry.chain_id || entry.client_entry_id || "";
    if (!id) {
      return;
    }
    const option = document.createElement("option");
    option.value = id;
    option.textContent = entry.name || id;
    option.selected = id === group.selected_chain_id || entry.client_entry_id === group.selected_chain_id || entry.relay_chain_id === group.selected_chain_id;
    chain.appendChild(option);
  });
  chain.disabled = action.value !== "tunnel";
  action.onchange = () => {
    chain.disabled = action.value !== "tunnel";
  };

  const save = document.createElement("button");
  save.className = "command";
  save.textContent = "保存";
  save.onclick = () => saveProxyGroup(group.group || "fallback", action.value, chain.value, item);

  controls.append(action, chain, save);

  const result = document.createElement("div");
  result.className = "link-result";
  result.textContent = "等待修改";

  item.append(title, meta, controls, result);
  return item;
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
  setLinkPanelStatus(chainId, "正在执行 relay ping-pong 延迟测试...", false);
  window.CloudHelper.linkLatency(chainId);
}

function runLinkSpeed(chainId, protocol) {
  const label = protocol ? protocol : "auto";
  setText("linkStatus", `正在测速：${chainId} (${label})`);
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

function refreshSummary(config) {
  const data = config || readConfig();
  setText("summaryController", data.controllerUrl || "-");
  setText("summaryNodeId", data.nodeId || "-");
  setText("summaryReady", data.ready ? "已配置" : "未配置");
  setText("summaryRuntime", window.CloudHelper.status());
  setText("summaryLocalVersion", data.localVersion || "-");
  setRuntimeStatus(`运行：${window.CloudHelper.status()}`);
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
  } catch (_) {
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
  if (page === "link") {
    refreshLinks();
  }
  if (page === "proxy") {
    refreshProxyGroups();
  }
  setInterval(refreshSummarySilent, 5000);
}

document.addEventListener("DOMContentLoaded", initPage);
