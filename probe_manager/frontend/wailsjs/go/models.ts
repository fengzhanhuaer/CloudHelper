export namespace backend {
	
	export class LogViewResponse {
	    source: string;
	    file_path: string;
	    lines: number;
	    content: string;
	    fetched_at: string;
	
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
	
	    static createFrom(source: any = {}) {
	        return new NetworkAssistantRuleGroupConfig(source);
	    }
	
	    constructor(source: any = {}) {
	        if ('string' === typeof source) source = JSON.parse(source);
	        this.group = source["group"];
	        this.action = source["action"];
	        this.tunnel_node_id = source["tunnel_node_id"];
	        this.tunnel_options = source["tunnel_options"];
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
	        this.tun_supported = source["tun_supported"];
	        this.tun_installed = source["tun_installed"];
	        this.tun_enabled = source["tun_enabled"];
	        this.tun_library_path = source["tun_library_path"];
	        this.tun_status = source["tun_status"];
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
	export class ProbeNode {
	    node_no: number;
	    node_name: string;
	    node_secret: string;
	    target_system: string;
	    direct_connect: boolean;
	    created_at: string;
	    updated_at: string;
	
	    static createFrom(source: any = {}) {
	        return new ProbeNode(source);
	    }
	
	    constructor(source: any = {}) {
	        if ('string' === typeof source) source = JSON.parse(source);
	        this.node_no = source["node_no"];
	        this.node_name = source["node_name"];
	        this.node_secret = source["node_secret"];
	        this.target_system = source["target_system"];
	        this.direct_connect = source["direct_connect"];
	        this.created_at = source["created_at"];
	        this.updated_at = source["updated_at"];
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
