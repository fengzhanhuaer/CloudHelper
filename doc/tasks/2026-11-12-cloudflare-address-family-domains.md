# Cloudflare IPv4 / IPv6 独立域名

- 状态：已完成；创建/更新：2026-11-12 Asia/Singapore。
- 用户原始需求：主控 Cloudflare 模块为 v4、v6 申请不同域名。最新指令：可以（授权实现、验证和发布）。
- 启用方式：明确跨 DNS 同步、持久化、路由候选页面和发布的长任务条件。

## 一、需求定义

IPv4 保留现有名称，IPv6 独立命名；涵盖业务、公网 DDNS、局域网记录。多地址序号各自延续，不混合 A/AAAA。路由选择页可选两种域名并下发原值。保留认证/证书策略，不修改现有路由规则，不操作生产 Cloudflare 或部署。

| 需求 | 验收 | 单元/接口 | 任务/测试 | 状态 |
|---|---|---|---|---|
| REQ-001 | A 保留原域名，AAAA 使用 api6.codex./ddns6_go_/local6_go_；多地址和仅 IPv6 节点正确 | UNIT-001 / IF-001 | TASK-001 / TEST-001 | 已完成 |
| REQ-002 | 手动与自动更新迁移旧 AAAA，失败保留可用记录和存储，重试可恢复；识别新前缀 | UNIT-001 / IF-001 | TASK-001 / TEST-002 | 已完成 |
| REQ-003 | Cloudflare 页面说明命名，路由候选区分 IPv4/IPv6，保存下发域名不改写；无客户端协议变更 | UNIT-002 / IF-002 | TASK-002 / TEST-003 | 已完成 |
| REQ-004 | 主控回归与浏览器验证通过，按现行十资产工作流发布 | UNIT-001/002 | TASK-003 / TEST-004 | 已完成 |

## 二、总体架构

现有 `buildCloudflareDesiredRecords` 为 A/AAAA 共用 base，手动 apply 和自动 runtime DDNS 更新都调用它。记录存入各节点 `cloudflare_ddns_records`；路由页面通过 `/mng/api/cloudflare/ddns/records` 动态生成选项，以普通字符串经拓扑接口下发。证书仍用原 `api.codex.` 业务域名，属于既有身份契约，本次不修改。

目标：生成时按类型转换 base，记录 JSON 字段/节点配置键不变；同步时成功创建新 AAAA 才移除严格匹配的旧记录，失败保留原记录；清理识别两套前缀。页面显示地址族，候选与下发均接受普通域名。

| 接口 | 契约与位置 | 兼容与验证 |
|---|---|---|
| IF-001 DDNS 记录 | `cloudflare_assistant.go`，原字段/类型/序号，AAAA 新名称 | IPv4 名不变；TEST-001/002 |
| IF-002 路由候选/拓扑 | `mng_pages/route.html`、既有拓扑接口，选项值原域名，显示 A/AAAA 对应地址族 | 不改客户端/现有已存路由；TEST-003 |

## 三、单元设计

- UNIT-001 `cloudflare_assistant.go`：生成 IPv6 base、新前缀分类/清理；迁移严格核对旧记录所有权，重试幂等，错误不撤销已用记录；表驱动/fake Cloudflare API 验证。
- UNIT-002 `mng_pages/cloudflare.html`、`route.html`：提示命名、候选携带地址族标签；既有证书名保持；浏览器实际选项及保存 payload 验证。

## 四、执行任务

| 任务 | 内容 | 状态 |
|---|---|---|
| TASK-001 | 域名生成、迁移/同步及回归 | 已完成 |
| TASK-002 | 页面候选、说明和实际浏览器验证 | 已完成 |
| TASK-003 | 主控完整 tests/vet，发布与十资产核验 | 已完成 |

当前交接：基线 a1a2ce39d；无关 `doc/tasks/2026-10-28-upgrade-source-resume.md` 保持原样。六文件功能提交 `9f824dec5d90deb907bf9d60b10af27931ffeab1` 已推送 `origin/mapledev`，匹配 [Actions 43227590417](https://github.com/fengzhanhuaer/CloudHelper/actions/runs/43227590417) 九项任务全部成功。工作流版本提交/标签 `859ee2ba8`（父提交为本功能提交）生成 [v0.4.118](https://github.com/fengzhanhuaer/CloudHelper/releases/tag/v0.4.118)，非草稿/非预发布，十资产精确匹配且大小非零。实现、验证、发布已完成；未升级生产或修改真实 DNS。下一步唯一动作：无。

## 五、测试与验证

- TEST-001：三类×双地址族、多 IP 序号、自定义 DDNS、仅 IPv6；分类/清理前缀。
- TEST-002：fake Cloudflare API 验证同 IP 迁移实际写入、旧 AAAA 清理、创建/删除失败保留旧记录、重试，无真实云调用。
- TEST-003：真实管理 HTML 在本机模拟 API 上，通过 Chrome 验证候选、IPv6 选择/保存载荷、桌面/手机与控制台；API 验证域名下发。
- TEST-004：`go -C probe_controller test ./... -count=1`、`vet ./...`、现行 workflow/Actions/十资产。未改 Probe/插件，不增加五组长互通测试。
- TEST-001/002 已完成：`go -C probe_controller test ./internal/core -run 'TestCloudflare(AddressFamily|Copilot|Is)' -count=1` 通过（2.514s）；新增生成测试及六种手动/自动×成功/创建失败/删除失败场景，验证 API 创建先于删除、同 IP 新名称写入、失败保留旧 DNS/持久化状态、重试与幂等。假 API 拦截全部出站请求，无真实云写入。
- TEST-003 已完成：Browser plugin not available，使用已安装 Playwright/Chrome；`%TEMP%/cloudhelper-cf-family-qa.cjs` 在 `http://127.0.0.1:57990` 模拟接口上运行真实 HTML，1440/390px；DDNS 申请载荷、说明/表格、路由节点过滤、IPv4/IPv6 选项、IPv6 保存/PATCH 原值与重载、IPv4 切回保存均通过，控制台错误为空。证据 `%TEMP%/ch-cf-family-20261112/`。手机 DDNS 表格使用局部横向滚动，地址族标签前置，避免长域名截断后无法辨别类型。主控管理 API 原保存/GET/落盘测试同时验证 api6 域名不改写。
- TEST-004 已完成：主控完整 tests（core 8.126s、tests 3.711s）/vet 通过；现行预检通过，workflow SHA256 `0cfd7f3cd28ef86665c8a47a73bd1554d42cc7c3cdd5ac25fd52a2e341457143`，十项产物不变，无 Docker/BE86U/ARM64 router 资产，预检旧 routerArtifacts 硬编码不作为事实。最终页面微调重跑浏览器通过；Actions/十资产最终核验见当前交接。
- 未执行：生产 DNS/升级未授权，不以本地模拟冒充线上生效；只改主控，不重跑 Probe/插件五组长互通测试。客户端 TLS 源码确认已有 SPKI 身份验证分支，不修改证书名或降低验证。

## 六、决策、冲突与缺陷

- DEC-001：IPv4 保持兼容，IPv6 新前缀单独命名；不添加任意自定义名称，使用用户确认的自动生成方案。
- DEC-002：不自动改写已选拓扑域名；新 IPv6 候选由用户选择。证书身份不因承载地址族而改变。
- 无规则冲突；DEF-001 已修复：apply 单项失败保留旧记录并加入清理保护集合，手动/自动六场景回归通过。

## 七、回滚、门禁与风险

回滚代码后重新执行 DDNS 申请即可重建旧 AAAA；新域名可能仍在 DNS 中，按实际迁移状态手动核对，不能盲删。迁移会让旧域名仅保留 A，原 IPv6 用户需选新域名；同步失败要保留旧记录，DNS TTL 60 秒仍会有客户端缓存。

- 准备门禁：通过（范围、接口、追踪、回滚与验证已记录）。
- 完成门禁：通过（REQ-001 至 REQ-004 实现/测试/证据闭合，生产应用独立授权，未冒充线上生效）。
