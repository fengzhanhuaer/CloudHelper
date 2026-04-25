# proxy_group 与 proxy_state JSON 示例

说明
- 行为语义对齐 `probe_manager` 的规则执行方式。
- `proxy_group.json` 由用户维护分组匹配静态规则。
- `rules` 使用字符串数组，每个元素一条规则表达式。
- 推荐规则键: `domain_suffix` `domain_keyword` `domain_prefix` `domain`。
- `action` 与 `tunnel_node_id` 仅作为动态运行态写入 `proxy_state.json`。
- 动态数据用于回显当前命中组的执行状态与通道状态。
- 前缀匹配示例: `domain_prefix:api.` 表示匹配以 `api.` 开头的域名。
- 静态配置不包含日期字段。
- `proxy_group.json` 顶层支持全局 DNS 配置字段 `dns_servers` `dot_servers` `doh_servers` `doh_proxy_servers`。
- `doh_proxy_servers` 专用于代理组域名解析上游。
- 远方 DNS 来源优先级: `doh_proxy_servers` > `doh_servers` > `dot_servers` > `dns_servers`。
- `fallback` 为内置组，不需要在 `proxy_group.json` 中显式配置。
- 当 `proxy_group.json` 或 `proxy_state.json` 或 `proxy_host.txt` 文件不存在时，系统会自动生成默认配置文件。
- DNS 缓存 TTL 固定为 15 天。

`proxy_group.json` 静态配置示例
```json
{
  "version": 1,
  "dns_servers": [
    "223.5.5.5",
    "119.29.29.29"
  ],
  "dot_servers": [
    "dns.alidns.com:853",
    "dot.pub:853"
  ],
  "doh_servers": [
    "https://dns.alidns.com/dns-query",
    "https://doh.pub/dns-query"
  ],
  "doh_proxy_servers": [
    "https://cloudflare-dns.com/dns-query",
    "https://dns.google/dns-query"
  ],
  "groups": [
    {
      "group": "default",
      "rules": [
        "domain_suffix:example.com",
        "domain_prefix:api.",
        "cidr:10.10.0.0/16"
      ]
    },
    {
      "group": "media",
      "rules": [
        "domain_suffix:video.example",
        "domain_keyword:stream"
      ]
    },
    {
      "group": "blocked",
      "rules": [
        "domain_suffix:blocked.example"
      ]
    }
  ],
  "note": "fallback is built in"
}
```

`proxy_host.txt` 静态路由映射示例
```txt
# dns,ip
api.internal.example,10.20.30.40
cdn.edge.example,203.0.113.20

# 支持注释与空行
```

`proxy_state.json` 动态状态示例
```json
{
  "version": 1,
  "updated_at": "2026-04-24T02:18:00Z",
  "groups": [
    {
      "group": "default",
      "action": "tunnel",
      "tunnel_node_id": "chain:chain-proxy-001",
      "runtime_status": "在线"
    },
    {
      "group": "media",
      "action": "tunnel",
      "tunnel_node_id": "chain:chain-proxy-007",
      "runtime_status": "离线"
    }
  ],
  "backup": {
    "last_uploaded_at": "",
    "last_upload_status": "idle",
    "last_upload_error": ""
  }
}
```
