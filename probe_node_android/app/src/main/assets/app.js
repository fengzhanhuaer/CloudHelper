let saveFeedbackTimer = 0;
let toastTimer = 0;

const pages = {
  status: ["状态", "当前 Android 节点配置与运行状态。"],
  proxy: ["代理", "启动或停止 Android 代理运行时。"],
  settings: ["设置", "配置主控与节点密钥，并执行直连或主控代理升级。"]
};

window.CloudHelperUI = {
  setStatus(message) {
    setStatus(message || "");
    setUpgradeStatus(message || "");
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

function refreshSummary(config) {
  const data = config || readConfig();
  setText("summaryController", data.controllerUrl || "-");
  setText("summaryNodeId", data.nodeId || "-");
  setText("summaryReady", data.ready ? "已配置" : "未配置");
  setText("summaryRuntime", window.CloudHelper.status());
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
  setInterval(refreshSummarySilent, 5000);
}

document.addEventListener("DOMContentLoaded", initPage);
