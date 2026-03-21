import type { TabItem } from "./types";

export const STORAGE_UPGRADE_PROJECT = "cloudhelper.manager.upgrade_project";
export const DEFAULT_UPGRADE_PROJECT = "fengzhanhuaer/CloudHelper";

export const ALL_TABS: TabItem[] = [
  { key: "overview", label: "概要状态" },
  { key: "probe-manage", label: "探针管理" },
  { key: "network-assistant", label: "网络助手" },
  { key: "cloudflare-assistant", label: "Cloudflare助手" },
  { key: "tg-assistant", label: "TG助手" },
  { key: "log-viewer", label: "日志查看" },
  { key: "system-settings", label: "系统设置" },
];

export const OPERATOR_TABS: TabItem[] = [
  { key: "overview", label: "概要状态" },
  { key: "probe-manage", label: "探针管理" },
  { key: "network-assistant", label: "网络助手" },
  { key: "cloudflare-assistant", label: "Cloudflare助手" },
  { key: "tg-assistant", label: "TG助手" },
  { key: "log-viewer", label: "日志查看" },
];

export const VIEWER_TABS: TabItem[] = [
  { key: "overview", label: "概要状态" },
];
