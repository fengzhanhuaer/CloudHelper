import type { TabItem } from "./types";

export const STORAGE_CONTROLLER_URL = "cloudhelper.manager.controller_url";
export const STORAGE_UPGRADE_PROJECT = "cloudhelper.manager.upgrade_project";
export const DEFAULT_UPGRADE_PROJECT = "fengzhanhuaer/CloudHelper";

export const ALL_TABS: TabItem[] = [
  { key: "overview", label: "概要状态" },
  { key: "probe-status", label: "探针状态" },
  { key: "probe-manage", label: "探针管理" },
  { key: "link-manage", label: "链路管理" },
  { key: "network-assistant", label: "网络助手" },
  { key: "system-settings", label: "系统设置" },
];

export const OPERATOR_TABS: TabItem[] = [
  { key: "overview", label: "概要状态" },
  { key: "probe-status", label: "探针状态" },
  { key: "probe-manage", label: "探针管理" },
  { key: "link-manage", label: "链路管理" },
  { key: "network-assistant", label: "网络助手" },
];

export const VIEWER_TABS: TabItem[] = [
  { key: "overview", label: "概要状态" },
  { key: "probe-status", label: "探针状态" },
];
