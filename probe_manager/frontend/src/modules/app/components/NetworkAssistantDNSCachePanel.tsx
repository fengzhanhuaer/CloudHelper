import type { NetworkAssistantDNSCacheEntry } from "../types";

type NetworkAssistantDNSCachePanelProps = {
  dnsCacheEntries: NetworkAssistantDNSCacheEntry[];
  dnsCacheQuery: string;
  isDNSCacheLoading: boolean;
  dnsCacheStatus: string;
  onDNSCacheQueryChange: (value: string) => void;
  onQueryDNSCache: (query: string) => void;
};

export function NetworkAssistantDNSCachePanel(props: NetworkAssistantDNSCachePanelProps) {
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
      {props.dnsCacheEntries.length === 0 && !props.isDNSCacheLoading ? (
        <div className="status">暂无缓存记录</div>
      ) : (
        <table style={{ width: "100%", borderCollapse: "collapse", fontSize: 13, marginTop: 8 }}>
          <thead>
            <tr style={{ background: "#f0f0f0" }}>
              <th style={{ padding: "4px 8px", textAlign: "left", borderBottom: "1px solid #ddd" }}>域名</th>
              <th style={{ padding: "4px 8px", textAlign: "left", borderBottom: "1px solid #ddd" }}>IP</th>
              <th style={{ padding: "4px 8px", textAlign: "left", borderBottom: "1px solid #ddd" }}>FAKE IP</th>
              <th style={{ padding: "4px 8px", textAlign: "left", borderBottom: "1px solid #ddd" }}>类型</th>
              <th style={{ padding: "4px 8px", textAlign: "left", borderBottom: "1px solid #ddd" }}>来源</th>
              <th style={{ padding: "4px 8px", textAlign: "left", borderBottom: "1px solid #ddd" }}>组</th>
              <th style={{ padding: "4px 8px", textAlign: "left", borderBottom: "1px solid #ddd" }}>过期时间</th>
            </tr>
          </thead>
          <tbody>
            {props.dnsCacheEntries.map((entry, i) => (
              <tr key={i} style={{ borderBottom: "1px solid #eee" }}>
                <td style={{ padding: "4px 8px", fontFamily: "monospace" }}>{entry.domain || "-"}</td>
                <td style={{ padding: "4px 8px", fontFamily: "monospace" }}>{entry.ip || "-"}</td>
                <td style={{ padding: "4px 8px", fontFamily: "monospace" }}>{entry.fake_ip_value || "-"}</td>
                <td style={{ padding: "4px 8px", fontFamily: "monospace" }}>{entry.kind || "-"}</td>
                <td style={{ padding: "4px 8px", fontFamily: "monospace" }}>{entry.source || "-"}</td>
                <td style={{ padding: "4px 8px" }}>{entry.direct ? "直连" : (entry.group || "-")}</td>
                <td style={{ padding: "4px 8px", fontFamily: "monospace", whiteSpace: "nowrap" }}>{entry.expires_at || "-"}</td>
              </tr>
            ))}
          </tbody>
        </table>
      )}
    </>
  );
}
