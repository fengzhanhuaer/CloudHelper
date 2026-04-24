# proxy_group 与 proxy_state JSON 示例

说明
- 行为语义对齐 `probe_manager` 的规则执行方式。
- `proxy_group.json` 由用户维护分组匹配静态规则。
- `rules_text` 使用行式规则文本 每行一条规则 不使用数组字段。
- `rules_text` 的行内容不需要 JSON 逗号分隔 每行直接写规则表达式。
- `action` 与 `tunnel_node_id` 仅作为动态运行态写入 `proxy_state.json`。
- 动态数据用于回显当前命中组的执行状态与通道状态。
- 前缀匹配示例: `domain_prefix:api.` 表示匹配以 `api.` 开头的域名。
- 静态配置不包含日期字段。
- `fallback` 为内置组，不需要在 `proxy_group.json` 中显式配置。
- 当 `proxy_group.json` 或 `proxy_state.json` 或 `proxy_host.txt` 文件不存在时，系统会自动生成默认配置文件。

`proxy_group.json` 静态配置示例
```json
{
  "version": 1,
  "groups": [
    {
      "group": "default",
      "rules_text": "domain_suffix:example.com\r\ndomain_prefix:api.\r\ncidr:10.10.0.0/16"
    },
    {
      "group": "media",
      "rules_text": "domain_suffix:video.example\r\ndomain_keyword:stream"
    },
    {
      "group": "blocked",
      "rules_text": "domain_suffix:blocked.example"
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
