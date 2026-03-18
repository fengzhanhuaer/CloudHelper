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
  mux_connected?: boolean;
  mux_active_streams?: number;
  mux_reconnects?: number;
  mux_last_recv?: string;
  mux_last_pong?: string;
};

export type NetworkAssistantLogSource = "manager" | "controller";

export type NetworkAssistantLogFilterSource = "all" | NetworkAssistantLogSource;

export type NetworkAssistantLogEntry = {
  time: string;
  source: NetworkAssistantLogSource;
  category: string;
  message: string;
  line: string;
};

export type NetworkAssistantLogResponse = {
  lines: number;
  content: string;
  fetched_at: string;
  entries: NetworkAssistantLogEntry[];
};

export type LogSource = "local" | "server";

export type LogContentResponse = {
  source: LogSource;
  file_path: string;
  lines: number;
  content: string;
  fetched_at: string;
};

export type TGAssistantAccount = {
  id: string;
  label: string;
  phone: string;
  api_id: number;
  authorized: boolean;
  pending_code: boolean;
  last_error: string;
  created_at: string;
  updated_at: string;
  last_login_at: string;
  self_user_id: number;
  self_username: string;
  self_display_name: string;
  self_phone: string;
  schedules?: TGAssistantSchedule[];
};

export type TGAssistantAPIKey = {
  api_id: number;
  api_hash: string;
  configured: boolean;
};

export type TGAssistantSchedule = {
  id: string;
  task_type: string;
  enabled: boolean;
  target: string;
  send_at: string;
  message: string;
  delay_min_sec: number;
  delay_max_sec: number;
  created_at: string;
  updated_at: string;
};

export type TGAssistantTarget = {
  id: string;
  name: string;
  username?: string;
  type?: string;
};

export type CloudflareAPIKey = {
  api_key: string;
  configured: boolean;
};

export type CloudflareDDNSRecord = {
  node_id: string;
  node_no: number;
  node_name: string;
  zone_name: string;
  record_class?: string;
  record_name: string;
  record_id: string;
  record_type: string;
  sequence?: number;
  content_ip: string;
  updated_at: string;
  last_message?: string;
};

export type CloudflareDDNSApplyItem = {
  node_id: string;
  node_no: number;
  node_name: string;
  record_class?: string;
  record_name: string;
  record_type?: string;
  sequence?: number;
  record_id?: string;
  content_ip?: string;
  status: string;
  message: string;
};

export type CloudflareDDNSApplyResult = {
  zone_name: string;
  applied: number;
  skipped: number;
  items: CloudflareDDNSApplyItem[];
  records: CloudflareDDNSRecord[];
};

export type TabKey =
  | "overview"
  | "probe-manage"
  | "link-manage"
  | "network-assistant"
  | "cloudflare-assistant"
  | "tg-assistant"
  | "log-viewer"
  | "system-settings";

export type TabItem = {
  key: TabKey;
  label: string;
};

export type StatusTone = "info" | "success" | "error";
