import type { NetworkAssistantDNSCacheEntry } from "../types";

type NetworkAssistantDNSCachePanelProps = {
  dnsCacheEntries: NetworkAssistantDNSCacheEntry[];
  dnsCacheQuery: string;
  isDNSCacheLoading: boolean;
  dnsCacheStatus: string;
  onDNSCacheQueryChange: (value: string) => void;
  onQueryDNSCache: (query: string) => void;
};

type AggregatedDNSCacheRow = {
  domain: string;
  ips: string[];
  fakeIPs: string[];
  dnsCount: number;
  ipConnectCount: number;
  totalCount: number;
  kinds: string[];
  sources: string[];
  groups: string[];
  expiresAt: string;
};

function appendUnique(list: string[], raw: string) {
  const value = raw.trim();
  if (!value || list.includes(value)) {
    return;
  }
  list.push(value);
}

function aggregateDNSCacheEntries(entries: NetworkAssistantDNSCacheEntry[]): AggregatedDNSCacheRow[] {
  const grouped = new Map<string, AggregatedDNSCacheRow>();

  for (const entry of entries) {
    const domain = entry.domain?.trim() || entry.ip?.trim() || "-";
    const key = domain.toLowerCase();
    let row = grouped.get(key);
    if (!row) {
      row = {
        domain,
        ips: [],
        fakeIPs: [],
        dnsCount: 0,
        ipConnectCount: 0,
        totalCount: 0,
        kinds: [],
        sources: [],
        groups: [],
        expiresAt: entry.expires_at || "-",
      };
      grouped.set(key, row);
    }

    const entryIP = (entry.ip || "").trim();
    const entryFakeIP = (entry.fake_ip_value || "").trim();
    if (entry.fake_ip) {
      appendUnique(row.fakeIPs, entryFakeIP || entryIP);
    } else {
      appendUnique(row.ips, entryIP);
    }
    appendUnique(row.kinds, entry.kind || "");
    appendUnique(row.sources, entry.source || "");
    appendUnique(row.groups, entry.direct ? "直连" : (entry.group || "-"));

    row.dnsCount += entry.dns_count ?? 0;
    row.ipConnectCount += entry.ip_connect_count ?? 0;
    row.totalCount += entry.total_count ?? 0;

    const expiresAt = (entry.expires_at || "").trim();
    if (expiresAt && expiresAt > row.expiresAt) {
      row.expiresAt = expiresAt;
    }
  }

  return Array.from(grouped.values()).sort((a, b) => a.domain.localeCompare(b.domain));
}

function renderMultiValueCell(values: string[], monospace = true) {
  if (values.length === 0) {
    return "-";
  }
  return (
    <div style={{ display: "flex", flexDirection: "column", gap: 2 }}>
      {values.map((value) => (
        <span key={value} style={monospace ? { fontFamily: "monospace" } : undefined}>
          {value}
        </span>
      ))}
    </div>
  );
}

export function NetworkAssistantDNSCachePanel(props: NetworkAssistantDNSCachePanelProps) {
  const rows = aggregateDNSCacheEntries(props.dnsCacheEntries);

  return (
    <>
      <div className="content-actions" style={{ gap: 8, alignItems: "center" }}>
        <input
          type="text"
          className="input"
          style={{ flex: 1, minWidth: 0 }}
          placeholder="输入 IP 或域名查询（留空查询全部）"
          value={props.dnsCacheQuery}
          onChange={(e) => props.onDNSCacheQueryChange(e.target.value)}
          onKeyDown={(e) => {
            if (e.key === "Enter") {
              props.onQueryDNSCache(props.dnsCacheQuery);
            }
          }}
        />
        <button
          className="btn"
          onClick={() => props.onQueryDNSCache(props.dnsCacheQuery)}
          disabled={props.isDNSCacheLoading}
        >
          {props.isDNSCacheLoading ? "查询中..." : "查询"}
        </button>
      </div>
      {props.dnsCacheStatus && <div className="status">{props.dnsCacheStatus}</div>}
      {rows.length === 0 && !props.isDNSCacheLoading ? (
        <div className="status">暂无缓存记录</div>
      ) : (
        <table style={{ width: "100%", borderCollapse: "collapse", fontSize: 13, marginTop: 8 }}>
          <thead>
            <tr style={{ background: "rgba(18, 28, 42, 0.92)", color: "#e8f1ff" }}>
              <th style={{ padding: "4px 8px", textAlign: "left", borderBottom: "1px solid rgba(255,255,255,0.18)" }}>域名</th>
              <th style={{ padding: "4px 8px", textAlign: "left", borderBottom: "1px solid rgba(255,255,255,0.18)" }}>IP</th>
              <th style={{ padding: "4px 8px", textAlign: "left", borderBottom: "1px solid rgba(255,255,255,0.18)" }}>FAKE IP</th>
              <th style={{ padding: "4px 8px", textAlign: "left", borderBottom: "1px solid rgba(255,255,255,0.18)" }}>DNS次数</th>
              <th style={{ padding: "4px 8px", textAlign: "left", borderBottom: "1px solid rgba(255,255,255,0.18)" }}>IP连接次数</th>
              <th style={{ padding: "4px 8px", textAlign: "left", borderBottom: "1px solid rgba(255,255,255,0.18)" }}>总次数</th>
              <th style={{ padding: "4px 8px", textAlign: "left", borderBottom: "1px solid rgba(255,255,255,0.18)" }}>类型</th>
              <th style={{ padding: "4px 8px", textAlign: "left", borderBottom: "1px solid rgba(255,255,255,0.18)" }}>来源</th>
              <th style={{ padding: "4px 8px", textAlign: "left", borderBottom: "1px solid rgba(255,255,255,0.18)" }}>组</th>
              <th style={{ padding: "4px 8px", textAlign: "left", borderBottom: "1px solid rgba(255,255,255,0.18)" }}>过期时间</th>
            </tr>
          </thead>
          <tbody>
            {rows.map((row) => (
              <tr key={row.domain} style={{ borderBottom: "1px solid #eee", verticalAlign: "top" }}>
                <td style={{ padding: "4px 8px", fontFamily: "monospace" }}>{row.domain}</td>
                <td style={{ padding: "4px 8px" }}>{renderMultiValueCell(row.ips)}</td>
                <td style={{ padding: "4px 8px" }}>{renderMultiValueCell(row.fakeIPs)}</td>
                <td style={{ padding: "4px 8px", fontFamily: "monospace" }}>{row.dnsCount}</td>
                <td style={{ padding: "4px 8px", fontFamily: "monospace" }}>{row.ipConnectCount}</td>
                <td style={{ padding: "4px 8px", fontFamily: "monospace" }}>{row.totalCount}</td>
                <td style={{ padding: "4px 8px" }}>{renderMultiValueCell(row.kinds)}</td>
                <td style={{ padding: "4px 8px" }}>{renderMultiValueCell(row.sources)}</td>
                <td style={{ padding: "4px 8px" }}>{renderMultiValueCell(row.groups, false)}</td>
                <td style={{ padding: "4px 8px", fontFamily: "monospace", whiteSpace: "nowrap" }}>{row.expiresAt || "-"}</td>
              </tr>
            ))}
          </tbody>
        </table>
      )}
    </>
  );
}
