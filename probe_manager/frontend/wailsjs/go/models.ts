export namespace backend {
	
	export class CloudflareIPTestResult {
	    ip: string;
	    latency_ms: number;
	
	    static createFrom(source: any = {}) {
	        return new CloudflareIPTestResult(source);
	    }
	
	    constructor(source: any = {}) {
	        if ('string' === typeof source) source = JSON.parse(source);
	        this.ip = source["ip"];
	        this.latency_ms = source["latency_ms"];
	    }
	}
	export class CloudflareSpeedTestRequest {
	    sample_count: number;
	    timeout_ms: number;
	    top_n: number;
	
	    static createFrom(source: any = {}) {
	        return new CloudflareSpeedTestRequest(source);
	    }
	
	    constructor(source: any = {}) {
	        if ('string' === typeof source) source = JSON.parse(source);
	        this.sample_count = source["sample_count"];
	        this.timeout_ms = source["timeout_ms"];
	        this.top_n = source["top_n"];
	    }
	}
	export class CloudflareSpeedTestResponse {
	    results: CloudflareIPTestResult[];
	    total_tested: number;
	    valid_count: number;
	    message: string;
	
	    static createFrom(source: any = {}) {
	        return new CloudflareSpeedTestResponse(source);
	    }
	
	    constructor(source: any = {}) {
	        if ('string' === typeof source) source = JSON.parse(source);
	        this.results = this.convertValues(source["results"], CloudflareIPTestResult);
	        this.total_tested = source["total_tested"];
	        this.valid_count = source["valid_count"];
	        this.message = source["message"];
	    }
	
		convertValues(a: any, classs: any, asMap: boolean = false): any {
		    if (!a) {
		        return a;
		    }
		    if (a.slice && a.map) {
		        return (a as any[]).map(elem => this.convertValues(elem, classs));
		    } else if ("object" === typeof a) {
		        if (asMap) {
		            for (const key of Object.keys(a)) {
		                a[key] = new classs(a[key]);
		            }
		            return a;
		        }
		        return new classs(a);
		    }
		    return a;
		}
	}
	export class LogEntry {
	    time: string;
	    level: string;
	    message: string;
	    line: string;
	
	    static createFrom(source: any = {}) {
	        return new LogEntry(source);
	    }
	
	    constructor(source: any = {}) {
	        if ('string' === typeof source) source = JSON.parse(source);
	        this.time = source["time"];
	        this.level = source["level"];
	        this.message = source["message"];
	        this.line = source["line"];
	    }
	}
	export class LogViewResponse {
	    source: string;
	    file_path: string;
	    lines: number;
	    content: string;
	    fetched_at: string;
	    entries?: LogEntry[];
	
	    static createFrom(source: any = {}) {
	        return new LogViewResponse(source);
	    }
	
	    constructor(source: any = {}) {
	        if ('string' === typeof source) source = JSON.parse(source);
	        this.source = source["source"];
	        this.file_path = source["file_path"];
	        this.lines = source["lines"];
	        this.content = source["content"];
	        this.fetched_at = source["fetched_at"];
	        this.entries = this.convertValues(source["entries"], LogEntry);
	    }
	
		convertValues(a: any, classs: any, asMap: boolean = false): any {
		    if (!a) {
		        return a;
		    }
		    if (a.slice && a.map) {
		        return (a as any[]).map(elem => this.convertValues(elem, classs));
		    } else if ("object" === typeof a) {
		        if (asMap) {
		            for (const key of Object.keys(a)) {
		                a[key] = new classs(a[key]);
		            }
		            return a;
		        }
		        return new classs(a);
		    }
		    return a;
		}
	}
	export class ManagerUpgradeProgress {
	    active: boolean;
	    mode: string;
	    phase: string;
	    percent: number;
	    message: string;
	
	    static createFrom(source: any = {}) {
	        return new ManagerUpgradeProgress(source);
	    }
	
	    constructor(source: any = {}) {
	        if ('string' === typeof source) source = JSON.parse(source);
	        this.active = source["active"];
	        this.mode = source["mode"];
	        this.phase = source["phase"];
	        this.percent = source["percent"];
	        this.message = source["message"];
	    }
	}
	export class ManagerUpgradeResult {
	    current_version: string;
	    latest_version: string;
	    asset_name?: string;
	    mode: string;
	    updated: boolean;
	    message: string;
	
	    static createFrom(source: any = {}) {
	        return new ManagerUpgradeResult(source);
	    }
	
	    constructor(source: any = {}) {
	        if ('string' === typeof source) source = JSON.parse(source);
	        this.current_version = source["current_version"];
	        this.latest_version = source["latest_version"];
	        this.asset_name = source["asset_name"];
	        this.mode = source["mode"];
	        this.updated = source["updated"];
	        this.message = source["message"];
	    }
	}
	export class NetworkAssistantDNSCacheEntry {
	    domain: string;
	    ip: string;
	    fake_ip: boolean;
	    fake_ip_value: string;
	    direct: boolean;
	    node_id: string;
	    group: string;
	    kind: string;
	    source: string;
	    dns_count: number;
	    ip_connect_count: number;
	    total_count: number;
	    expires_at: string;
	
	    static createFrom(source: any = {}) {
	        return new NetworkAssistantDNSCacheEntry(source);
	    }
	
	    constructor(source: any = {}) {
	        if ('string' === typeof source) source = JSON.parse(source);
	        this.domain = source["domain"];
	        this.ip = source["ip"];
	        this.fake_ip = source["fake_ip"];
	        this.fake_ip_value = source["fake_ip_value"];
	        this.direct = source["direct"];
	        this.node_id = source["node_id"];
	        this.group = source["group"];
	        this.kind = source["kind"];
	        this.source = source["source"];
	        this.dns_count = source["dns_count"];
	        this.ip_connect_count = source["ip_connect_count"];
	        this.total_count = source["total_count"];
	        this.expires_at = source["expires_at"];
	    }
	}
	export class NetworkAssistantDNSUpstreamConfig {
	    prefer: string;
	    dns_servers: string[];
	    dot_servers: string[];
	    doh_servers: string[];
	    fake_ip_cidr: string;
	    fake_ip_whitelist: string[];
	
	    static createFrom(source: any = {}) {
	        return new NetworkAssistantDNSUpstreamConfig(source);
	    }
	
	    constructor(source: any = {}) {
	        if ('string' === typeof source) source = JSON.parse(source);
	        this.prefer = source["prefer"];
	        this.dns_servers = source["dns_servers"];
	        this.dot_servers = source["dot_servers"];
	        this.doh_servers = source["doh_servers"];
	        this.fake_ip_cidr = source["fake_ip_cidr"];
	        this.fake_ip_whitelist = source["fake_ip_whitelist"];
	    }
	}
	export class NetworkAssistantGroupKeepaliveItem {
	    group: string;
	    action: string;
	    tunnel_node_id?: string;
	    tunnel_label?: string;
	    connected: boolean;
	    active_streams: number;
	    last_recv: string;
	    last_pong: string;
	    status: string;
	
	    static createFrom(source: any = {}) {
	        return new NetworkAssistantGroupKeepaliveItem(source);
	    }
	
	    constructor(source: any = {}) {
	        if ('string' === typeof source) source = JSON.parse(source);
	        this.group = source["group"];
	        this.action = source["action"];
	        this.tunnel_node_id = source["tunnel_node_id"];
	        this.tunnel_label = source["tunnel_label"];
	        this.connected = source["connected"];
	        this.active_streams = source["active_streams"];
	        this.last_recv = source["last_recv"];
	        this.last_pong = source["last_pong"];
	        this.status = source["status"];
	    }
	}
	export class NetworkAssistantLogEntry {
	    time: string;
	    source: string;
	    category: string;
	    message: string;
	    line: string;
	
	    static createFrom(source: any = {}) {
	        return new NetworkAssistantLogEntry(source);
	    }
	
	    constructor(source: any = {}) {
	        if ('string' === typeof source) source = JSON.parse(source);
	        this.time = source["time"];
	        this.source = source["source"];
	        this.category = source["category"];
	        this.message = source["message"];
	        this.line = source["line"];
	    }
	}
	export class NetworkAssistantLogResponse {
	    lines: number;
	    content: string;
	    fetched_at: string;
	    entries: NetworkAssistantLogEntry[];
	
	    static createFrom(source: any = {}) {
	        return new NetworkAssistantLogResponse(source);
	    }
	
	    constructor(source: any = {}) {
	        if ('string' === typeof source) source = JSON.parse(source);
	        this.lines = source["lines"];
	        this.content = source["content"];
	        this.fetched_at = source["fetched_at"];
	        this.entries = this.convertValues(source["entries"], NetworkAssistantLogEntry);
	    }
	
		convertValues(a: any, classs: any, asMap: boolean = false): any {
		    if (!a) {
		        return a;
		    }
		    if (a.slice && a.map) {
		        return (a as any[]).map(elem => this.convertValues(elem, classs));
		    } else if ("object" === typeof a) {
		        if (asMap) {
		            for (const key of Object.keys(a)) {
		                a[key] = new classs(a[key]);
		            }
		            return a;
		        }
		        return new classs(a);
		    }
		    return a;
		}
	}
	export class NetworkAssistantRuleGroupConfig {
	    group: string;
	    action: string;
	    tunnel_node_id?: string;
	    tunnel_options: string[];
	    tunnel_option_labels?: Record<string, string>;
	    selected_label?: string;
	    runtime_action?: string;
	    runtime_tunnel_node_id?: string;
	    runtime_tunnel_label?: string;
	    runtime_connected: boolean;
	    runtime_status?: string;
	    runtime_last_recv?: string;
	    runtime_last_pong?: string;
	    runtime_active_streams?: number;
	
	    static createFrom(source: any = {}) {
	        return new NetworkAssistantRuleGroupConfig(source);
	    }
	
	    constructor(source: any = {}) {
	        if ('string' === typeof source) source = JSON.parse(source);
	        this.group = source["group"];
	        this.action = source["action"];
	        this.tunnel_node_id = source["tunnel_node_id"];
	        this.tunnel_options = source["tunnel_options"];
	        this.tunnel_option_labels = source["tunnel_option_labels"];
	        this.selected_label = source["selected_label"];
	        this.runtime_action = source["runtime_action"];
	        this.runtime_tunnel_node_id = source["runtime_tunnel_node_id"];
	        this.runtime_tunnel_label = source["runtime_tunnel_label"];
	        this.runtime_connected = source["runtime_connected"];
	        this.runtime_status = source["runtime_status"];
	        this.runtime_last_recv = source["runtime_last_recv"];
	        this.runtime_last_pong = source["runtime_last_pong"];
	        this.runtime_active_streams = source["runtime_active_streams"];
	    }
	}
	export class NetworkAssistantRuleConfig {
	    rule_file_path: string;
	    groups: NetworkAssistantRuleGroupConfig[];
	    fallback: NetworkAssistantRuleGroupConfig;
	
	    static createFrom(source: any = {}) {
	        return new NetworkAssistantRuleConfig(source);
	    }
	
	    constructor(source: any = {}) {
	        if ('string' === typeof source) source = JSON.parse(source);
	        this.rule_file_path = source["rule_file_path"];
	        this.groups = this.convertValues(source["groups"], NetworkAssistantRuleGroupConfig);
	        this.fallback = this.convertValues(source["fallback"], NetworkAssistantRuleGroupConfig);
	    }
	
		convertValues(a: any, classs: any, asMap: boolean = false): any {
		    if (!a) {
		        return a;
		    }
		    if (a.slice && a.map) {
		        return (a as any[]).map(elem => this.convertValues(elem, classs));
		    } else if ("object" === typeof a) {
		        if (asMap) {
		            for (const key of Object.keys(a)) {
		                a[key] = new classs(a[key]);
		            }
		            return a;
		        }
		        return new classs(a);
		    }
		    return a;
		}
	}
	
	export class NetworkAssistantStatus {
	    enabled: boolean;
	    mode: string;
	    node_id: string;
	    available_nodes: string[];
	    socks5_listen: string;
	    tunnel_route: string;
	    tunnel_status: string;
	    system_proxy_status: string;
	    last_error: string;
	    mux_connected: boolean;
	    mux_active_streams: number;
	    mux_reconnects: number;
	    mux_last_recv: string;
	    mux_last_pong: string;
	    group_keepalive: NetworkAssistantGroupKeepaliveItem[];
	    tun_supported: boolean;
	    tun_installed: boolean;
	    tun_enabled: boolean;
	    tun_library_path: string;
	    tun_status: string;
	
	    static createFrom(source: any = {}) {
	        return new NetworkAssistantStatus(source);
	    }
	
	    constructor(source: any = {}) {
	        if ('string' === typeof source) source = JSON.parse(source);
	        this.enabled = source["enabled"];
	        this.mode = source["mode"];
	        this.node_id = source["node_id"];
	        this.available_nodes = source["available_nodes"];
	        this.socks5_listen = source["socks5_listen"];
	        this.tunnel_route = source["tunnel_route"];
	        this.tunnel_status = source["tunnel_status"];
	        this.system_proxy_status = source["system_proxy_status"];
	        this.last_error = source["last_error"];
	        this.mux_connected = source["mux_connected"];
	        this.mux_active_streams = source["mux_active_streams"];
	        this.mux_reconnects = source["mux_reconnects"];
	        this.mux_last_recv = source["mux_last_recv"];
	        this.mux_last_pong = source["mux_last_pong"];
	        this.group_keepalive = this.convertValues(source["group_keepalive"], NetworkAssistantGroupKeepaliveItem);
	        this.tun_supported = source["tun_supported"];
	        this.tun_installed = source["tun_installed"];
	        this.tun_enabled = source["tun_enabled"];
	        this.tun_library_path = source["tun_library_path"];
	        this.tun_status = source["tun_status"];
	    }
	
		convertValues(a: any, classs: any, asMap: boolean = false): any {
		    if (!a) {
		        return a;
		    }
		    if (a.slice && a.map) {
		        return (a as any[]).map(elem => this.convertValues(elem, classs));
		    } else if ("object" === typeof a) {
		        if (asMap) {
		            for (const key of Object.keys(a)) {
		                a[key] = new classs(a[key]);
		            }
		            return a;
		        }
		        return new classs(a);
		    }
		    return a;
		}
	}
	export class NetworkProcessEvent {
	    kind: string;
	    timestamp: number;
	    process_name?: string;
	    domain?: string;
	    target_ip?: string;
	    target_port?: number;
	    direct: boolean;
	    node_id?: string;
	    group?: string;
	    resolved_ips?: string[];
	    count: number;
	
	    static createFrom(source: any = {}) {
	        return new NetworkProcessEvent(source);
	    }
	
	    constructor(source: any = {}) {
	        if ('string' === typeof source) source = JSON.parse(source);
	        this.kind = source["kind"];
	        this.timestamp = source["timestamp"];
	        this.process_name = source["process_name"];
	        this.domain = source["domain"];
	        this.target_ip = source["target_ip"];
	        this.target_port = source["target_port"];
	        this.direct = source["direct"];
	        this.node_id = source["node_id"];
	        this.group = source["group"];
	        this.resolved_ips = source["resolved_ips"];
	        this.count = source["count"];
	    }
	}
	export class NetworkProcessInfo {
	    pid: number;
	    name: string;
	    exe_path: string;
	
	    static createFrom(source: any = {}) {
	        return new NetworkProcessInfo(source);
	    }
	
	    constructor(source: any = {}) {
	        if ('string' === typeof source) source = JSON.parse(source);
	        this.pid = source["pid"];
	        this.name = source["name"];
	        this.exe_path = source["exe_path"];
	    }
	}
	export class PrivateKeyStatus {
	    found: boolean;
	    path: string;
	    message: string;
	
	    static createFrom(source: any = {}) {
	        return new PrivateKeyStatus(source);
	    }
	
	    constructor(source: any = {}) {
	        if ('string' === typeof source) source = JSON.parse(source);
	        this.found = source["found"];
	        this.path = source["path"];
	        this.message = source["message"];
	    }
	}
	export class ProbeChainPingResult {
	    ok: boolean;
	    chain_id: string;
	    entry_host: string;
	    entry_port: number;
	    link_layer: string;
	    duration_ms: number;
	    message: string;
	
	    static createFrom(source: any = {}) {
	        return new ProbeChainPingResult(source);
	    }
	
	    constructor(source: any = {}) {
	        if ('string' === typeof source) source = JSON.parse(source);
	        this.ok = source["ok"];
	        this.chain_id = source["chain_id"];
	        this.entry_host = source["entry_host"];
	        this.entry_port = source["entry_port"];
	        this.link_layer = source["link_layer"];
	        this.duration_ms = source["duration_ms"];
	        this.message = source["message"];
	    }
	}
	export class ProbeLinkChainCacheHopConfig {
	    node_no: number;
	    listen_host?: string;
	    listen_port?: number;
	    external_port?: number;
	    link_layer: string;
	    dial_mode?: string;
	    relay_host?: string;
	
	    static createFrom(source: any = {}) {
	        return new ProbeLinkChainCacheHopConfig(source);
	    }
	
	    constructor(source: any = {}) {
	        if ('string' === typeof source) source = JSON.parse(source);
	        this.node_no = source["node_no"];
	        this.listen_host = source["listen_host"];
	        this.listen_port = source["listen_port"];
	        this.external_port = source["external_port"];
	        this.link_layer = source["link_layer"];
	        this.dial_mode = source["dial_mode"];
	        this.relay_host = source["relay_host"];
	    }
	}
	export class ProbeLinkChainCachePortForward {
	    id?: string;
	    name?: string;
	    entry_side?: string;
	    listen_host: string;
	    listen_port: number;
	    target_host: string;
	    target_port: number;
	    network?: string;
	    enabled: boolean;
	
	    static createFrom(source: any = {}) {
	        return new ProbeLinkChainCachePortForward(source);
	    }
	
	    constructor(source: any = {}) {
	        if ('string' === typeof source) source = JSON.parse(source);
	        this.id = source["id"];
	        this.name = source["name"];
	        this.entry_side = source["entry_side"];
	        this.listen_host = source["listen_host"];
	        this.listen_port = source["listen_port"];
	        this.target_host = source["target_host"];
	        this.target_port = source["target_port"];
	        this.network = source["network"];
	        this.enabled = source["enabled"];
	    }
	}
	export class ProbeLinkChainCacheItem {
	    chain_id: string;
	    name: string;
	    user_id: string;
	    user_public_key: string;
	    secret: string;
	    entry_node_id: string;
	    exit_node_id: string;
	    cascade_node_ids: string[];
	    node_name_by_id?: Record<string, string>;
	    listen_host: string;
	    listen_port: number;
	    link_layer?: string;
	    hop_configs?: ProbeLinkChainCacheHopConfig[];
	    port_forwards?: ProbeLinkChainCachePortForward[];
	    egress_host: string;
	    egress_port: number;
	    created_at?: string;
	    updated_at?: string;
	
	    static createFrom(source: any = {}) {
	        return new ProbeLinkChainCacheItem(source);
	    }
	
	    constructor(source: any = {}) {
	        if ('string' === typeof source) source = JSON.parse(source);
	        this.chain_id = source["chain_id"];
	        this.name = source["name"];
	        this.user_id = source["user_id"];
	        this.user_public_key = source["user_public_key"];
	        this.secret = source["secret"];
	        this.entry_node_id = source["entry_node_id"];
	        this.exit_node_id = source["exit_node_id"];
	        this.cascade_node_ids = source["cascade_node_ids"];
	        this.node_name_by_id = source["node_name_by_id"];
	        this.listen_host = source["listen_host"];
	        this.listen_port = source["listen_port"];
	        this.link_layer = source["link_layer"];
	        this.hop_configs = this.convertValues(source["hop_configs"], ProbeLinkChainCacheHopConfig);
	        this.port_forwards = this.convertValues(source["port_forwards"], ProbeLinkChainCachePortForward);
	        this.egress_host = source["egress_host"];
	        this.egress_port = source["egress_port"];
	        this.created_at = source["created_at"];
	        this.updated_at = source["updated_at"];
	    }
	
		convertValues(a: any, classs: any, asMap: boolean = false): any {
		    if (!a) {
		        return a;
		    }
		    if (a.slice && a.map) {
		        return (a as any[]).map(elem => this.convertValues(elem, classs));
		    } else if ("object" === typeof a) {
		        if (asMap) {
		            for (const key of Object.keys(a)) {
		                a[key] = new classs(a[key]);
		            }
		            return a;
		        }
		        return new classs(a);
		    }
		    return a;
		}
	}
	
	export class ProbeLinkConnectResult {
	    ok: boolean;
	    node_id: string;
	    endpoint_type: string;
	    url: string;
	    status_code: number;
	    service: string;
	    version: string;
	    message: string;
	    connected_at: string;
	    duration_ms: number;
	
	    static createFrom(source: any = {}) {
	        return new ProbeLinkConnectResult(source);
	    }
	
	    constructor(source: any = {}) {
	        if ('string' === typeof source) source = JSON.parse(source);
	        this.ok = source["ok"];
	        this.node_id = source["node_id"];
	        this.endpoint_type = source["endpoint_type"];
	        this.url = source["url"];
	        this.status_code = source["status_code"];
	        this.service = source["service"];
	        this.version = source["version"];
	        this.message = source["message"];
	        this.connected_at = source["connected_at"];
	        this.duration_ms = source["duration_ms"];
	    }
	}
	export class ProbeNodeCloudflareDDNSRecord {
	    record_class: string;
	    record_name: string;
	
	    static createFrom(source: any = {}) {
	        return new ProbeNodeCloudflareDDNSRecord(source);
	    }
	
	    constructor(source: any = {}) {
	        if ('string' === typeof source) source = JSON.parse(source);
	        this.record_class = source["record_class"];
	        this.record_name = source["record_name"];
	    }
	}
	export class ProbeNode {
	    node_no: number;
	    node_name: string;
	    remark: string;
	    ddns: string;
	    cloudflare_ddns_records?: ProbeNodeCloudflareDDNSRecord[];
	    node_secret: string;
	    target_system: string;
	    direct_connect: boolean;
	    payment_cycle: string;
	    cost: string;
	    expire_at: string;
	    vendor_name: string;
	    vendor_url: string;
	    created_at: string;
	    updated_at: string;
	
	    static createFrom(source: any = {}) {
	        return new ProbeNode(source);
	    }
	
	    constructor(source: any = {}) {
	        if ('string' === typeof source) source = JSON.parse(source);
	        this.node_no = source["node_no"];
	        this.node_name = source["node_name"];
	        this.remark = source["remark"];
	        this.ddns = source["ddns"];
	        this.cloudflare_ddns_records = this.convertValues(source["cloudflare_ddns_records"], ProbeNodeCloudflareDDNSRecord);
	        this.node_secret = source["node_secret"];
	        this.target_system = source["target_system"];
	        this.direct_connect = source["direct_connect"];
	        this.payment_cycle = source["payment_cycle"];
	        this.cost = source["cost"];
	        this.expire_at = source["expire_at"];
	        this.vendor_name = source["vendor_name"];
	        this.vendor_url = source["vendor_url"];
	        this.created_at = source["created_at"];
	        this.updated_at = source["updated_at"];
	    }
	
		convertValues(a: any, classs: any, asMap: boolean = false): any {
		    if (!a) {
		        return a;
		    }
		    if (a.slice && a.map) {
		        return (a as any[]).map(elem => this.convertValues(elem, classs));
		    } else if ("object" === typeof a) {
		        if (asMap) {
		            for (const key of Object.keys(a)) {
		                a[key] = new classs(a[key]);
		            }
		            return a;
		        }
		        return new classs(a);
		    }
		    return a;
		}
	}
	
	export class ReleaseAsset {
	    name: string;
	    size: number;
	    download_url: string;
	
	    static createFrom(source: any = {}) {
	        return new ReleaseAsset(source);
	    }
	
	    constructor(source: any = {}) {
	        if ('string' === typeof source) source = JSON.parse(source);
	        this.name = source["name"];
	        this.size = source["size"];
	        this.download_url = source["download_url"];
	    }
	}
	export class ReleaseInfo {
	    repo: string;
	    tag_name: string;
	    release_name?: string;
	    html_url?: string;
	    published_at?: string;
	    assets: ReleaseAsset[];
	
	    static createFrom(source: any = {}) {
	        return new ReleaseInfo(source);
	    }
	
	    constructor(source: any = {}) {
	        if ('string' === typeof source) source = JSON.parse(source);
	        this.repo = source["repo"];
	        this.tag_name = source["tag_name"];
	        this.release_name = source["release_name"];
	        this.html_url = source["html_url"];
	        this.published_at = source["published_at"];
	        this.assets = this.convertValues(source["assets"], ReleaseAsset);
	    }
	
		convertValues(a: any, classs: any, asMap: boolean = false): any {
		    if (!a) {
		        return a;
		    }
		    if (a.slice && a.map) {
		        return (a as any[]).map(elem => this.convertValues(elem, classs));
		    } else if ("object" === typeof a) {
		        if (asMap) {
		            for (const key of Object.keys(a)) {
		                a[key] = new classs(a[key]);
		            }
		            return a;
		        }
		        return new classs(a);
		    }
		    return a;
		}
	}

}

