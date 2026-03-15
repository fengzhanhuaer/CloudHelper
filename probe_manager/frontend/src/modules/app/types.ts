export type NonceResponse = {
  nonce: string;
  expires_at: string;
};

export type LoginResponse = {
  session_token: string;
  ttl: number;
  username?: string;
  user_role?: string;
  cert_type?: string;
};

export type DashboardStatusResponse = {
  message: string;
  service: string;
  uptime?: number;
};

export type StatusWSMessage = {
  type?: string;
  message?: string;
  service?: string;
  uptime?: number;
  server_time?: string;
};

export type ControllerVersionResponse = {
  current_version: string;
  latest_version?: string;
  upgrade_available?: boolean;
  message?: string;
};

export type ControllerUpgradeResponse = {
  current_version: string;
  latest_version: string;
  updated: boolean;
  asset_name?: string;
  message: string;
};

export type PrivateKeyStatus = {
  found: boolean;
  path?: string;
  message?: string;
};

export type ReleaseAsset = {
  name: string;
  size: number;
  download_url: string;
};

export type ReleaseInfo = {
  repo: string;
  tag_name: string;
  release_name?: string;
  html_url?: string;
  published_at?: string;
  assets: ReleaseAsset[];
};

export type ManagerUpgradeResult = {
  current_version: string;
  latest_version: string;
  asset_name?: string;
  mode: "direct" | "proxy";
  updated: boolean;
  message: string;
};

export type UpgradeProgress = {
  active: boolean;
  phase: string;
  percent: number;
  message: string;
};

export type NetworkAssistantMode = "direct" | "global" | "rule";

export type NetworkAssistantStatus = {
  enabled: boolean;
  mode: NetworkAssistantMode;
  node_id: string;
  available_nodes: string[];
  socks5_listen: string;
  tunnel_route: string;
  tunnel_status: string;
  system_proxy_status: string;
  last_error: string;
};

export type LogSource = "local" | "server";

export type LogContentResponse = {
  source: LogSource;
  file_path: string;
  lines: number;
  content: string;
  fetched_at: string;
};

export type TabKey = "overview" | "probe-manage" | "link-manage" | "network-assistant" | "log-viewer" | "system-settings";

export type TabItem = {
  key: TabKey;
  label: string;
};

export type StatusTone = "info" | "success" | "error";
