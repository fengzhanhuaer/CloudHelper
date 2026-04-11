# 管理端代理升级稳定性改造（第一阶段：后端）实施计划

## 1. 范围与边界

### 1.1 本阶段范围（仅后端）
- manager：`probe_manager/backend/upgrade.go`
- controller：`probe_controller/internal/core/ws_admin.go`
- 测试：
  - `probe_manager/backend/probe_link_test.go`
  - 新增 `probe_controller/internal/core/ws_admin_proxy_download_test.go`

### 1.2 本阶段不做
- 前端文案分级映射（第二阶段再做）
- 非升级链路的下载组件重构

---

## 2. 目标

1. 代理升级下载在抖动网络下可自动重连并续传完成
2. manager/controller 下载超时参数可配置且默认值对齐
3. 错误返回包含阶段信息（握手/鉴权/流读取/落盘/完成）便于定位
4. 补齐 controller 侧流式下载测试覆盖

---

## 3. 详细改造项

### A. manager（`probe_manager/backend/upgrade.go`）

#### A1. 超时与重连参数配置化
- 将以下参数做“默认值 + 环境变量覆盖”策略：
  - 总下载超时
  - WS 读空闲超时
  - 最大重连次数
  - 退避基础/上限
- 保证未配置时行为与当前兼容。

#### A2. 错误分层包装
- 在以下阶段分别添加错误前缀：
  - websocket handshake
  - auth 写/读
  - stream 请求写
  - chunk decode/write
  - done 收尾
  - 文件 rename
- 最终错误保留上下文：`offset`、`reconnect_attempts`、`status`。

#### A3. 续传边界一致性
- 保持每次读取前刷新 `ReadDeadline`（滑动读超时）。
- 明确不可重试错误直接失败；可重试错误进入指数退避重连。
- 对 stalled 场景返回明确错误，避免死循环。

### B. controller（`probe_controller/internal/core/ws_admin.go`）

#### B1. 下载超时配置化
- `adminWSProxyDownloadTimeout` 改为可配置加载（默认与 manager 对齐）。

#### B2. 流式返回语义稳定化
- 保持 `200/206/416` 语义稳定。
- `proxy.download.chunk` 与最终 done 响应均保持：
  - `downloaded`
  - `total`
  - `status`
- 错误信息中包含 request_id 和阶段信息。

### C. 测试补齐

#### C1. manager 侧回归（扩展现有测试）
- 断线后重连并按 offset 续传。
- `416` 场景正确完成并落盘。
- 不可重试错误直接失败。

#### C2. controller 新增测试
- 文件：`probe_controller/internal/core/ws_admin_proxy_download_test.go`
- 覆盖：
  - 正常 chunk 推送
  - 偏移续传（206）
  - range 不满足（416）
  - 上游非 2xx 错误透传

---

## 4. 实施顺序

1. 先改 manager 参数加载与错误分层（低风险，收益高）
2. 再改 controller 参数与语义收敛
3. 补齐 controller 新测试 + manager 回归测试
4. 运行后端测试并修正

---

## 5. 验收标准

- 代理升级在模拟断链后可自动续传直至完成
- 错误信息可直接定位失败阶段与关键上下文
- manager/controller 超时参数可配置并有默认值
- 新增及现有相关测试通过

---

## 6. 回滚策略

- 所有变更局限于升级代理链路代码路径
- 若出现回归，可回滚以下文件：
  - `probe_manager/backend/upgrade.go`
  - `probe_controller/internal/core/ws_admin.go`
  - `probe_controller/internal/core/ws_admin_proxy_download_test.go`
  - `probe_manager/backend/probe_link_test.go`
