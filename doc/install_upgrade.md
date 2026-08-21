# 安装与升级

## Linux 旁路由与 Mihomo

新安装统一在主控 `/mng/probe` 创建 `linux_router`。Alpine 安装器支持 x86_64 和 arm64，并把 Router 与对应架构的官方 Mihomo 安装到 `/opt/cloudhelper/probe_router`。独立 Mihomo 出口产品、安装入口和在线兼容均已移除。

安装器从同一 Release 下载 `cloudhelper-probe-router-linux-${GOARCH}` 和 `${PROGRAM_ASSET}-manifest.json`，经主控代理取得清单指定的 Mihomo 压缩包，逐一校验 SHA-256 和程序类型后才替换。在线升级使用同一配对清单和事务回滚机制。Router 没有二次分流快照时继续提供原有直连出口；已有快照但 Mihomo 配置或健康检查失败时，代理出口失败关闭。
