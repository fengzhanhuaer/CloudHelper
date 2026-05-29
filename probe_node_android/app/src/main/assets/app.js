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
}

function stopCore() {
  const result = window.CloudHelper.stopProxy();
  setRuntimeStatus(`停止：${result}`);
  refreshSummary();
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

function runLinkLatency(chainId) {
  setText("linkStatus", `正在测试链路延迟：${chainId}`);
  window.CloudHelper.linkLatency(chainId);
}

function runLinkSpeed(chainId, protocol) {
  const label = protocol ? protocol : "auto";
  setText("linkStatus", `正在测速：${chainId} (${label})`);
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
  if (chain.error) {
    const error = document.createElement("div");
    error.className = "inline-feedback error";
    error.textContent = chain.error;
    item.append(title, meta, error, actions);
  } else {
    item.append(title, meta, actions);
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
    status.textContent = formatSpeedResult(data);
    return;
  }
  if (Array.isArray(data.results)) {
    status.textContent = formatLatencyResult(data);
    return;
  }
  if (!data.ok) {
    status.textContent = `测试失败：${data.error || data.status || "unknown"}`;
    return;
  }
  status.textContent = formatLatencyResult(data);
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
  setInterval(refreshSummarySilent, 5000);
}

document.addEventListener("DOMContentLoaded", initPage);
