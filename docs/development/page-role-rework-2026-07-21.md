# E2M 页面与角色首轮改造（2026-07-21）

本文记录 `page-role-baseline-2026-07-21.md` 评估之后已执行的首轮改造。目标是先消除错误引导与错误业务承诺，再把已经存在的托管交付能力表达成可验收的用户路径。

## 已完成

### 角色导航

- `client`：服务总览、接入管理、服务质量与路由、已交付资源、通知设置、高级与安全。
- `admin`：平台总览、运维中心、客户接入、上游资源与交付、平台治理、高级工具。
- `supplier`：供给概览、资源连接与凭证、供给审计。
- 原 URL 保留，避免已有链接立即失效；旧账号健康、自有账号审批和网关能力降入高级工具。

### 不完整业务模块

账单、支付与供给尚未形成正式闭环，默认不进入导航。仅在显式构建开关启用时展示：

```dotenv
VITE_E2M_ENABLE_BILLING=true
VITE_E2M_ENABLE_PAYMENTS=true
VITE_E2M_ENABLE_SUPPLY=true
```

支付开关关闭时，系统设置不展示支付 Tab，直接访问 `?view=payment` 也会回退到注册与安全。

### 自动接入

- 新增 owner 自助状态接口，按当前登录 `client` 的实例聚合 Connector 就绪、工作流阶段、安全阻塞码、Key 交付与证明、发布 Binding 和服务状态。
- 响应不暴露 Pool、Plan、Channel、远端账号、凭证、Lease、指纹或原始内部错误。
- 接入向导改为真实状态页，不再要求任意凭证、不再把 Connector 写成可选，也不再读取 admin-only RoutePlan。
- 首轮仅提供只读状态；可重试失败继续遵守后台自动退避，普通用户不能绕过并发与退避控制手工强制执行。

### Key 交付边界

- client 页面改名“已交付资源”，展示掩码标识、Key 版本、本地 Binding 证明和时间，不再提供密码验证、明文查看或复制。
- 后端明文接口默认关闭，只有运维明确设置 `E2M_OWNER_KEY_REVEAL=true` 才开启兼容能力；开启时打印绕过统一余额门禁的警告。

### 表单权限

- client 凭证仅允许 notification。
- supplier 凭证仅允许 upstream/proxy。
- admin 凭证目标账号根据用途过滤：notification 只选 enabled client，upstream/proxy 只选 enabled supplier。
- admin 创建实例只允许选择 enabled client。
- 网关能力读 API 与前端一致，收紧为 admin-only。

### Admin 运维中心

- 增加客户、实例和接入状态筛选。
- 自动接入、事故和时间线使用同一作用域筛选。
- 接入实例和发布计划增加定位入口。
- 仍未加入“立即重试”和“人工恢复”写操作；这些动作需要后端幂等、租约和审计接口，不能只做前端按钮。

## 仍未完成

1. 统一余额、可信用量、价格版本、预授权/冻结、扣款、退款/冲正、上游成本对账和停服门禁。
2. Supplier Offer 到 Channel/DeliveryKey/部署/消耗/结算的真实履约链。
3. 通知测试投递、投递状态和静默/升级执行。
4. admin 上游编排按资源目录、策略、发布部署拆页。
5. client 首页进一步使用自动接入与服务质量数据替代旧实例 `status` 快照。
