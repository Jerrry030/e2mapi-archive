# E2M 当前事实基线

> **历史文档（2026-07-19 快照）。** 本文所述事实已被 2026-08-04 平台商业化批次全面取代：支付下单与回调、钱包充值、兑换码、基准价目表定价、用户级限流、统一设置模块均已实现（默认由 `E2M_ENABLE_PAYMENTS` 关闭），控制台信息架构也已重构为「平台管理 / 通用功能」两区。本文**不再对任何其他文档具有优先权**；现状以 [current-state.md](current-state.md) 与 [platform-commerce-execution-plan.md](platform-commerce-execution-plan.md) 为准。

更新时间：2026-07-19

## 文档地位

本文只记录当前工作区代码已经具备的能力、部分实现和明确缺口，同时记录已经确认但尚未实现的产品方向。

（原文此处声明本文在支付订单、本地 Binding、自动接入、账户余额和路由策略上具有优先权，该声明已于 2026-08-04 撤销。）

当前工作区包含大量未提交改动，因此“已实现”表示当前工作区代码事实，不等于已经形成正式发布版本。

## 当前产品与系统边界

E2M 当前实现为 AI 网关旁路控制面，不转发真实模型请求，也不进入请求数据面。

- Core 负责用户、会话、权限、实例、Connector 身份、任务、发布计划、健康策略、自动切换、审批、审计、通知和控制面业务状态。
- 每个 Instance 最多绑定一个出站 Connector。Connector 在用户侧、靠近 sub2api、new-api 或 CPA 运行，主动轮询 Core。
- 网关管理地址、认证方式和管理凭证只保存在 Connector 本地；Core 不保存，也不存在 Core 直接连接用户网关的兼容路径。
- 平台交付的上游 Key 由 Core Vault 保存交付副本，通过加密 Binding 安装任务写入 Connector 本地，再由 Connector 调用网关原生管理 API 发布。
- 用户资源直接归属于 `users.id` / `user_id`，不是组织、租户或工作区模型。

## 当前第一业务主路径

当前主要闭环是：

```text
平台维护共享上游池和渠道
→ 为用户分配不同来源的独占 Key
→ 自动安装本地 Binding
→ 发布到用户自己的 AI 网关
→ 收集脱敏健康证据
→ 故障时在该用户已经分配的不同来源 Key 之间切换
→ 主动探测和分阶段恢复
```

自动 onboarding 会为符合条件的托管实例和每个 active 上游池建立持久工作流，依次执行 Connector 检查、计划创建、Key 分配、Binding 安装与证明、发布和远端回执验证。

当前默认资格仍是“启用且具有 client 能力的用户实例 × 所有 active 池”。尚无订阅、套餐、余额权益或 PoolAccess 门禁。

## 已实现能力

### 账号与权限

- 邮箱密码登录、Bearer 会话、公开注册开关、邮箱后缀限制和 Turnstile 配置。
- `admin`、`client`、`supplier` 三类账号能力。
- 用户、实例、Connector、凭证、通知、审计、账单等资源按当前登录用户隔离；管理员可跨用户管理。

### Connector 与三类网关

- 一次性、精确绑定 `user_id + instance_id + connector_id` 的 enrollment。
- Connector 运行 token 轮换和吊销；本地 UI 使用独立 token。
- sub2api、new-api、CPA 三类适配器。
- Protocol v2 封闭 typed task：健康检查、账号列表、启停、切换、调度 fence、Binding 安装/证明、平台账号创建/更新/删除。
- 平台账号允许 create/update/delete；用户自有账号只允许 update，不允许平台创建或删除。
- 写任务具备幂等回执、租约校验、代次 fence 和延迟删除。
- 本地 write-only Binding store 已实现；真实 Binding 值不进入 Core 的任务 DTO，也不由本地 API 回显。

### 上游交付与运维

- UpstreamPool、UpstreamChannel、RoutePlan、PublishedBinding、ReconcileRun 和永久 Key allocation。
- dry-run、apply、灰度/批次发布、rollback 和平台账号退役后的 30 分钟延迟删除。
- 随机 challenge HMAC Binding 证明、Key 版本和每实例部署回执。
- 用户 Key 默认脱敏；重新验证密码、Binding 证明和当前版本部署回执全部通过后，可短时查看明文。
- 健康观测、1 分钟/5 分钟聚合、成功率、TTFT、总耗时、错误归因和质量 circuit。
- 当前自动摘除采用统一质量扣分：上游责任错误最多扣 55 分，TTFT 最多扣 25 分，总耗时最多扣 20 分。
- 软故障按稳定 cohort 扩大；认证或上游余额硬故障只处理有实例级证据的 Binding。
- 自动恢复要求主动证据，并按 `10% -> 25% -> 50% -> 100%` 分阶段回归。
- Sub2API 支持受控的账号级 SSE 主动质量探测；NewAPI 和 CPA 当前仍是人工恢复。
- SSE 实时事件、飞书、QQ OneBot 11、通用 Webhook、审批和审计链路。

### 供给、账单与收款管理

- SupplyOffer 登记、编辑、撤销、分配、回收和 SupplyLedger 台账。
- 月度 BillingStatement 计算：托管实例数月费 + 已接受账号处置次数费用。
- EasyPay、支付宝官方、微信支付官方、Stripe、Airwallex 收款渠道配置，敏感值写入 Vault。
- PaymentOrder PostgreSQL/Memory 持久化模型和状态枚举。
- 管理员支付订单列表、筛选、详情、审计记录，以及仅对“无上游交易号的本地 `PENDING` 订单”执行安全取消。

## 部分实现，不能对外表述为完整闭环

### 供给分配

供给分配当前只写入或撤销 SupplyLedger，并更新 Offer 生命周期。它不会自动：

- 创建 UpstreamChannel；
- 建立版本化交付 Key；
- 安装 Connector Binding；
- 创建或更新远端网关账号；
- 触发专属 onboarding。

因此“供给登记/分配”和“共享上游自动发布”目前是两条平行链路。

### 支付订单

订单管理骨架已经存在，但完整支付执行没有实现。当前缺少：

- 普通用户创建订单和 checkout；
- 支付服务商下单；
- Webhook 验签和事件去重；
- 支付状态驱动的幂等履约；
- 钱包充值；
- BillingStatement 结清；
- 对账和退款执行。

### 真实生产验收

- 三类真实网关已有本地集成栈和既有验收记录。
- 当前真实栈主动探测仍明确关闭。
- 尚缺可销毁的真实流量故障注入环境，用于持续验证进程崩溃、旧租约、部分写成功、调度 fence、延迟删除、回滚和渐进恢复。

## 已确认产品方向，但尚未实现

### 平台账户余额

已确认后续商业方向为：用户在平台统一充值到个人账户余额，并从余额消费。

当前仓库尚无以下资金域能力：

- WalletAccount；
- append-only WalletLedger；
- 现金余额、赠送余额或冻结余额区分；
- 充值入账；
- 预扣、冻结、释放、扣款和退款；
- 版本化 PriceBook；
- 可信 UsageEvent；
- 余额权益门禁；
- 余额不足时的 suspend/resume；
- 财务对账。

`BillingStatement` 当前只是实时计算的月度展示结果，不是可结清应收单。PaymentOrder 也不能为用户钱包入账。

### 严格防透支的架构限制

当前旁路架构不能保证“按请求或 token 实时扣费且绝不透支”，原因是：

1. Core 不在请求链路中，无法在每次请求前冻结余额。
2. Connector 运行在用户控制的环境，不能作为资金账本的绝对可信来源。
3. 已分配 Key 在满足重新验证和部署证明后可以向所属用户短时展示；用户可以绕过 Connector 直接调用上游。
4. 当前 Connector observation 只上报 token 数、成功结果、错误和时延，不上报可信金额。
5. Core observation ingest 不根据版本化价格表计算资金金额。
6. NewAPI 和 CPA 当前没有与 Sub2API 等价的完整被动请求事实采集链路。

因此，现有 observation、网关自报用量、网关余额、`cost_hint` 或 SupplyOffer `unit_price` 都不能直接作为钱包扣款依据。

若产品承诺严格预付费，必须至少满足以下一种条件：

- 请求经过平台或供应商控制的可信计量网关，并使用按用户/来源隔离的子 Key；或
- 上游提供平台可控制的独立 Key、权威累计用量 API、硬额度限制和可靠对账能力。

否则系统只能做到异步对账、低余额预警、安全垫和“有界超用”，不能承诺零透支。

用户平台钱包不足属于商业权益状态，不能复用上游渠道 `insufficient_balance` 健康故障。后续应使用独立 entitlement / commercial suspension 状态，禁止新分配和新发布，并通过幂等 suspend/resume 调度现有付费 Binding；不得污染 SLA、质量分和 circuit。

### 下游用户路由偏好

已确认托管用户后续可以选择四种预设：

| 用户文案 | 现有内部类型 | 当前事实 |
| --- | --- | --- |
| 智能自动 | `balanced` | 有固定权重纯算法和管理员配置；不是动态学习，未接入生产替代排序 |
| 价格优先 | `cost_first` | 有纯算法骨架；可信价格与金额数据链未闭合，未接入生产替代排序 |
| 速度优先 | `latency_first` | 有 TTFT / 总耗时指标与纯算法骨架；未接入生产替代排序 |
| 成功率优先 | `stability_first` | 有成功率和稳定性指标与纯算法骨架；生产链仍使用统一质量扣分 |

现有 RouteStrategy 已支持：

- plan / pool / user 三种 scope；
- 生效优先级 `plan > pool > user > built-in default`；
- success、TTFT、duration、stability、cost 五项权重；
- 质量阈值、自动执行、强制审批、冷却、恢复观察和小时切换上限；
- 管理员 CRUD 和前端配置表单；
- 一个未进入生产自动切换链路的纯加权 `Rank` 实现。

真实自动切换当前调用统一 `RankByPenalty`，不读取策略权重、CostScore 或 StabilityScore。四种 type 当前主要影响配置和展示，不会按截图语义选出不同的替代渠道。

此外，当前策略 CRUD 和 `/upstream` 页面只允许平台管理员访问。普通托管用户没有查看或修改策略的 API 和页面。

后续普通用户入口应采用 owner-safe RoutePreference，只允许选择四种预设；不能直接开放 RouteStrategy 全字段。平台必须继续控制硬质量门槛、维护/退休/认证/余额 gate、审批、自动执行、冷却和切换频率。用户偏好只在安全且已有归属的候选集合中决定排序。

“下游用户”当前按 E2M 托管账号或其 Instance/RoutePlan 理解。系统没有站长网关内部每个终端 API 用户的身份、token 到策略映射或观测维度；终端客户级策略完全未实现。

## 当前明确未实现的生产项

- 钱包、可信计量、价格版本、余额权益和支付履约闭环。
- 普通用户路由偏好入口，以及四类偏好对生产健康候选的差异化排序。
- SSO/OIDC。
- 外部共享密管和 Vault Key 轮换。
- Connector mTLS、任务签名和更强设备身份。
- Connector 管理凭证加密密管；当前 `gateway-config.json` 依赖本地文件权限。
- OpenAPI 与生成式 TypeScript 客户端。
- River、sqlc、Gin、Redis runtime state。
- 飞书审批卡片回调。
- Web 控制台路由拆包；当前生产主 bundle 超过默认告警阈值。
- 面向公开生产的上游 ToS、许可证和商业合规复核。

## 对外表述约束

在上述能力真正实现和验收之前，对外不得声称：

- 已支持统一账户余额消费；
- 已能按 token 准确实时扣款；
- 可以保证余额绝不透支；
- 用户已经可以选择四种智能路由；
- 价格优先会选择用户实际结算价最低的渠道；
- 四类策略已经在真实自动切换链路中差异化生效；
- 供给资源分配后会自动注入用户网关；
- 已完成支付下单、Webhook、充值、账单结清或退款。

当前可以准确表述为：E2M 已具备 AI 网关旁路上游交付、健康运维和故障切换闭环；平台余额与用户路由偏好已确认产品方向，正在进入资金域、可信计量和用户安全策略入口的设计与实现阶段。
