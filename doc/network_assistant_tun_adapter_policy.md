# 网络助手 TUN 网卡命名与安装策略

## 1. 目标

统一管理端 TUN 网卡的对外展示信息，并保证安装行为幂等。

## 2. 网卡命名规范

- 网卡对外展示名称（Friendly Name）：`Maple`
- 网卡描述（Description）：`Maple Virtual Network Adapter`

## 3. 安装策略

- 安装前必须先检查本机是否已存在名称为 `Maple` 的目标网卡。
- 若已存在，则判定为“已安装”，不再重复安装驱动或重复创建网卡。
- 若不存在，才执行安装流程。

## 4. 状态与日志建议

- 已存在场景记录：`TUN adapter already exists, skip install: Maple`
- 新装成功记录：`TUN adapter installed: Maple`
