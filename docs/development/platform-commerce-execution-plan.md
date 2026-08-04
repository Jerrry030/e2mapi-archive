# 平台模式商业化执行计划（Sub2API 能力复刻）

更新：2026-08-04

状态：已批准执行。第 9 节三个决策点已于 2026-08-04 由项目负责人确认（不接 OAuth 订阅型上游；首发纯余额按量、暂不做套餐；基准货币 CNY）。本文对 Sub2API 的全部引用固定为 2026-08-03 审查时的上游 `main`（commit `825ca7b`）；后续比较必须固定 commit，避免用移动目标衡量进度。

实施进度：

- 2026-08-04：`PM-00` 完成——支付路由（管理端配置/渠道/订单、用户端充值下单、Stripe webhook）注册进 `Routes()`，`withBusinessFeatureGates` 安装为认证前中间件，`main.go` 从 `E2M_ENABLE_*` 环境变量读入六个业务开关（默认全关，fail-closed）；同步修复日限额只统计首页 100 单的缺陷（改为分页累加、超限 fail-closed）。门禁语义随决策 D2 简化：webhook 与充值下单不再要求已退役的 `E2M_ENABLE_HYBRID_SUPPLY`，整个充值闭环仅由 `E2M_ENABLE_PAYMENTS` 控制。新增 `payment_routes_test.go` 六个 httpapi 测试：开关关闭全路径 404 fail-closed、仅 Payments 开关即可放行、管理端 403 权限、下单返回 checkout URL（本地假 Stripe）、webhook 验签失败 400 / 确认入账 / 重复投递恰好一次、认证孤儿回调留痕。
- 2026-08-04：**四域复刻收官批次**——①低余额提醒：`internal/walletalert` worker（`E2M_PLATFORM_BALANCE_THRESHOLD` 元，未配置即禁用；`ListWalletsBelow` 双后端；边沿触发防刷屏，恢复后重新武装；经通知路由分发 + 审计 `platform.wallet.balance_low`）。②平台 Key 有效期：后端本已支持 `expires_at`，控制台建 Key 表单补 DatePicker。③上游账号健康增强（账号域）：数据面支持渠道标签 `e2m.model_mapping`（转发时改写请求体 model，快照仍记请求模型）与 `e2m.error_cooldown_rules`（错误码+关键词 → 临时禁调度，冷却渠道在下一请求的首次预扣即被排除；进程内状态，持久熔断留给暗启动 quality-circuit 子系统）。测试：walletalert 边沿触发、数据面模型映射改写、429 规则冷却+后续请求排除。core 全量与控制台 63/214、lint、build 全绿。
- 2026-08-04：邀请码注册门槛（PM-05 余量）+ `PM-08` 用户限流完成——①邀请门槛：`AuthSystemSettings.invitation_required` 全链路（env `E2M_AUTH_INVITATION_REQUIRED` / 系统设置 / 公开配置 / 注册页表单），注册流程先预检后原子消费（`ConsumeInvitationCode`，双后端），码被并发抢占时禁用误建账号 fail-closed；`HashRedeemCode` 上移 contracts 统一散列。测试覆盖无码/错码/正码/复用四路径与公开配置暴露。②用户限流：`users` 表新增 `platform_concurrency`/`platform_rpm`（migration 0080，0 不限），预扣事务内强制执行（活跃预留计数 + 60 秒滚动窗口计数，幂等重放豁免），新增 `ErrRateLimited` 哨兵、数据面 429 映射；管理端用户表单双字段。store 级测试覆盖并发上限/重放豁免/RPM 窗口。core+contracts 全量、控制台 63 文件 214 测试、lint、build 全绿。余量：低余额提醒 worker、Key 有效期表单、上游账号健康增强（下一批次）。
- 2026-08-04：`PM-07` 主体完成——自助闭环页面补齐：新端点 `GET /api/v1/platform/model-market`（客户可见分组×模型×当前最优售价；测试断言最优价选取且不泄漏上游密钥/base_url/供应商成本）；控制台新页 `PlatformModelMarket`（`/model-market` 常开路由，旧暗启动情报市场页 `ModelMarket.tsx` 原样保留不挂载，菜单守卫测试同步更新说明该路径已由原生页接管）；客户总览新增"平台消费"卡片（余额/今日结算/快捷入口，充值兑换入口随支付开关显隐）；管理端分组表单新增售价倍率字段（bps 标签往返换算）。自助五件套至此齐备：总览仪表卡、Key/用量（平台分发页客户视角）、充值、兑换、模型市场。余量：Key 有效期/配额表单增强并入 PM-08 批次。控制台 63 文件 214 测试、lint、build 全绿。
- 2026-08-04：`PM-06` 后端完成——新增 `internal/pricing`（LiteLLM 格式解析、内置基准价快照 + `E2M_PRICE_TABLE_PATH` 文件覆盖、保守模型名归一：仅前缀/日期/latest 剥离、`E2M_USD_TO_CNY_RATE` 未配置则整体禁用 fail-closed）。分组 `rate_multiplier`（bps 标签承载，创建/更新 API 校验 0.0001–100）；上游创建缺省价格时按"基准价 × 汇率 × 倍率"物化到端点（显式价优先；未收录模型或多模型异价 fail-closed，尊重 V1 单价约束）；管理端 `GET /api/v1/platform/pricing/preview`。结算热路径与 reserve 快照语义零改动。测试：pricing 包三组 + httpapi 倍率解析/往返、建组带倍率、预览、自动填价、显式价优先、异价拒绝、未配置 404；core 全量绿。余量：控制台分组表单倍率字段与报价展示并入 PM-07；远程价目表同步维持延期。
- 2026-08-04：`PM-05` 主体完成——兑换码域落地：contracts `redeem.go`、migration 0079（`redeem_codes` 表 + wallet_journals 增加 `redeem` kind）、store 双后端（批量创建/按哈希查询/列表/停用/删除/`RedeemBalanceCode` 原子兑换：FOR UPDATE 单胜出、过期即标记、余额入账走平账 journal）、httpapi 六端点（管理端生成/列表/停用/删除/`create-and-redeem` 幂等 API + 用户兑换端点，失败限速 20 次/小时，门禁挂 Payments 开关）。安全纪律：明文只在生成响应出现一次，库存哈希+前缀，列表不回明文/哈希。控制台：用户"兑换码"页 + 管理端"兑换码管理"页（生成弹窗、一次性明文展示与复制、筛选、停用/删除）。测试：httpapi 三组（全生命周期、门禁 fail-closed、create-and-redeem 幂等/异载荷 409）+ 控制台端点与页面测试；core 全量、contracts、控制台 63 文件 213 测试、lint、build 全绿。余量：邀请码注册门槛（与注册流程/系统设置联动）移入下一批次。
- 2026-08-04：`PM-03` 完成——新增 `internal/payment/easypay.go` EasyPay 适配器：`submit.php` MD5 签名托管页下单（`out_trade_no` 兼作 provider order id）、notify 回调验签（常量时间比较、`trade_status=TRADE_SUCCESS` 校验、`easypay:<trade_no>` 作稳定事件 ID）、`api.php` 商户查单、金额 0–2 位小数宽容解析；`ExpireCheckout` 为协议性 no-op（无上游会话，迟到 notify 由孤儿回调留痕兜底）。httpapi 侧：`chooseRechargeProvider` 通用化（Stripe/EasyPay 按渠道与类型选择，EasyPay 仅 CNY + alipay/wxpay）、共享 `confirmVerifiedPayment` 提取、新增 GET/POST easypay webhook 路由（应答纯文本 `success`）；清扫器扩展 EasyPay 查单补单/本地过期分支。充值页增加支付方式选择（Stripe/支付宝/微信）。测试：适配器四组（签名下单/验签/查单/金额解析）、httpapi 端到端（篡改 400、恰好一次入账、重放不重复）、清扫器 EasyPay 已支付补单+未支付过期。`gofmt`/`go build`/`go vet`/core 全量测试与控制台测试/ESLint/构建全绿。备注：qrcode（mapi.php）模式与 checkout-info 可用方式端点留待 PM-07。
- 2026-08-04：`PM-02` 完成，**阶段 0 全部落地**——控制台在 `VITE_E2M_ENABLE_PAYMENTS` 下新增 `/recharge` 用户充值页（钱包余额展示、预设/自定义金额、创建订单后跳转服务商托管支付页）、`/payment/success` 与 `/payment/cancelled` 回跳结果页（明确"到账以回调验签为准"），并把既有 `PaymentOrders` 管理页挂进路由与菜单（admin）；新增 `createRechargeOrder` API 封装与 `useCreateRechargeOrder` hook，zh/en 语言包同步。测试：endpoints 路径断言 + Recharge 页面"下单→跳转"交互测试；控制台 62 文件 211 测试、ESLint、生产构建全绿。决策 D1/D2/D3 已由负责人确认并记入第 9 节。下一步：阶段 1（PM-03 EasyPay 适配器、PM-04 主动对账、PM-05 兑换码域）。
- 2026-08-04：`PM-01` 完成——新增 `internal/paymentexpiry` 到期清扫 worker（仅 `E2M_ENABLE_PAYMENTS=true` 时启动，间隔 `E2M_PAYMENT_EXPIRY_INTERVAL` 默认 60s）：到期 PENDING 订单先向上游查单，已支付则以确定性事件 ID 走 `ConfirmRechargePayment` 恰好一次补单并审计 `payment.order.recover`；未支付则先关闭上游会话再本地转 EXPIRED（审计 `payment.order.expire`）；查询失败保守跳过等待下轮，webhook 竞态永远获胜。Store 新增 `ListExpiredPendingPaymentOrders` / `ExpirePaymentOrder`（memory/postgres 双实现，镜像 Cancel 的条件事务+审计模式），Stripe 适配器新增 `QueryCheckout`。四个 sweeper 测试覆盖过期关单、恰好一次补单、无上游会话本地过期、上游不可达保守跳过。`go build`/`go vet`/e2m-core 全量测试通过（`internal/vault` 两例为与本计划无关的存量失败，已另立独立任务）。CHANGELOG、`.env.example`、feature-flags.md 已同步。

## 1. 决策摘要

平台分发链路（分组 → 加密上游 → 钱包 → 下游 Key → `/v1/chat/completions` 转发计量）已在 0.2.0/0.3.0 打通，但缺少商业化闭环：下游用户无法自助充值，运营者缺少发卡、定价和自助控制台工具。经对 Sub2API（LGPL-3.0）逐域深读与本仓库暗启动代码评估，确定以下路线：

1. **高程度复刻 Sub2API 的商业化产品能力，E2M 原生实现。** 这是 `platform-boundaries.md` Sub2API Learning Policy 允许的路径：复刻功能、领域模型与交互；不部署 Sub2API 本体；不逐行搬运源码（任何直接源码复用需单独 LGPL-3.0 审查）。
2. **支付域以"接线"而非"重写"起步。** 仓库内暗启动的支付骨架（Stripe 适配器、13 态订单机、exactly-once 回调入账、双分录钱包账本 + DB 触发器平账）质量高于典型首版商业代码，且入账目标就是现行生产 `wallet_*` 表。唯一断点是路由从未注册（`httpapi/server.go` 路由表中支付相关注册数为 0，而 `business_feature_gates.go` 已预设全部路径）。
3. **裁剪线明确。** Sub2API 体量庞大；本计划只复刻商业化必需域，明确跳过内容风控、TLS 指纹伪装、批量图像、推广返利、7 家第三方登录、CRS 同步、备份自更新等（见第 4 节）。

## 2. 成功定义

### 2.1 商业闭环 MVP 场景（阶段 0+1 验收点）

在最小验收栈（`e2m-core-real-gateways.compose.yml` 派生）上：

- 下游用户（client 角色）登录控制台，自助创建充值订单，完成支付（Stripe 测试模式或 EasyPay 沙箱），回调验签通过后钱包余额入账；
- 同一回调重复投递任意次，余额只入账一次（`payment_callback_events` 幂等 + 账本平账触发器）；
- 超时未支付订单被清扫器转为 EXPIRED，且清扫前主动查单：上游实际已支付的订单触发补单入账而非过期；
- 用户以入账后的余额创建下游 Key 并成功调用 `/v1/chat/completions`，用量台账与钱包流水对得上；
- 管理员批量生成 balance 型兑换码，用户兑换后余额增加；同一码重复兑换被拒绝；`create-and-redeem` 管理 API 在同一 `Idempotency-Key` 下重放返回原结果。

### 2.2 核心 KPI

| 指标 | 目标 |
| --- | --- |
| 重复回调导致的重复入账 | 0（测试含并发双投递） |
| 钱包账本不平（触发器拒绝） | 0 |
| 支付明文密钥进入数据库或 API 响应 | 0（仅 Vault 引用） |
| 订单从支付完成到余额可用 | ≤ 1 个回调处理周期；补单路径 ≤ 1 个清扫周期 |
| 兑换码兑换幂等冲突错误 | 0 |
| 既有平台分发回归（`bootstrap-real-gateways.ps1`） | 全绿 |

## 3. 不可破坏的架构边界

1. 单一产品边界不变：身份、余额、Key、审计全部沿用 E2M 现有体系（users/sessions/RBAC/Vault/wallet/OperationAudit），不引入第二套登录、余额或 Key 命名空间。
2. Connector 边界不变：支付、兑换、定价全部是 Core 原生能力，不经过 Connector；Connector 仍只承担站长自有号池五类运维任务。
3. 金额纪律不变：订单与账本延续 integer micros / NUMERIC，禁止浮点参与结算；新增配置金额一并收敛为整数最小单位。
4. 密钥纪律不变：支付渠道密钥只进 Vault，管理 API 永不回显明文；下游 Key 明文取值仍走专门端点 + 审计。
5. 许可纪律：Sub2API 仅作行为参照。实现文件不得含其源码片段；如确需借用具体实现，先补 LGPL-3.0 架构与分发审查记录。
6. 入账唯一路径：钱包余额变更只允许经由 `wallet_journals` 平账事务（充值、兑换、管理员调整、消费结算），不得旁路直改余额字段。

## 4. 范围与明确延期

### 4.1 本计划复刻（按域）

- **支付**：Stripe（接线既有）+ EasyPay 适配器；订单状态机；回调验签与幂等入账；到期清扫与主动补单；用户充值页与管理端订单页；订单 `provider_snapshot` 商户身份锚定。
- **兑换码**：balance 型生成/批量（≤1000）/列表/禁用/过期/删除/CSV 导出；用户兑换端点（限速 + 分布式防重）；`create-and-redeem` 幂等管理 API（外部发卡系统对接口）。
- **定价模型**：基准价目表（LiteLLM `model_prices_and_context_window.json` 格式：本地回退表 + 可选远程同步）+ 分组倍率（`rate_multiplier`）；结算价 = 基准价 × 分组倍率，保留现行"上游显式价"作为覆盖优先级最高层。
- **用户域增量**：邀请码注册门槛（复用兑换码 invitation 型）；用户级并发与 RPM 限制；低余额邮件提醒。
- **下游自助控制台**：仪表盘（余额/今日用量/趋势）、Key 管理（建 Key、绑分组、配额/限速/有效期）、用量明细、充值页、兑换页、模型广场（报价页，展示分组倍率后的到手价）。
- **上游账号健康增量**（轻量吸收）：按错误码 + 关键词的临时禁调度规则；账号级模型映射改名。

### 4.2 明确延期（决策点解锁或后续排期）

订阅套餐与窗口限额（D2）；OAuth 订阅型上游接入及其连带的粘性会话、5h 窗口成本、后台令牌刷新、代理管理（D1）；`/v1/messages` Anthropic 协议与协议桥（D1 连带）；支付宝/微信官方直连、Airwallex、退款执行、订单负载均衡策略；峰时倍率、利润控制门、组合分组、阶梯定价。

### 4.3 明确不做

内容风控/Prompt 审计、TLS 指纹伪装、批量图像/视频/搜索计费、推广返利（affiliate）、优惠码（promo）、7 家第三方登录、Passkey、自定义用户属性（CRM 字段）、公告系统、CRS 同步、S3 备份与系统自更新、公开 Key 查询器、新手引导 tour。历史 `internal/billing/` 托管月账单轴与本计划无关，维持暗启动。

## 5. 既有暗启动代码的处置

| 资产 | 处置 |
| --- | --- |
| `internal/payment/`（Stripe 适配器 + 测试） | 直接接线复用 |
| `httpapi/payment.go` / `payment_checkout.go` / `payment_order.go` | 注册路由复用；补 httpapi 层测试（当前为 0） |
| `store` 支付/钱包事务（`ConfirmRechargePayment` 等，migrations 0045/0046/0075/0078） | 不改 schema 直接复用 |
| `business_feature_gates.go` | 安装进 `Routes()`，`main.go` 读环境变量启用 |
| 控制台 `PaymentOrders.tsx` / `PaymentSettings` | 挂入路由复用 |
| 已知缺陷 | `payment_checkout.go` 日限额仅统计首页 100 单——改为 SQL SUM；配置金额 `float64` 与 micros 混算——收敛为整数 |

## 6. 可执行工作包

规模为粗估人日（pd）。同阶段内可并行。

### 阶段 0 — 支付骨架接线（合计 ~5 pd）

- **PM-00 支付路由与门禁接线（2 pd）**：在 `Routes()` 注册管理端支付配置/渠道/订单与用户端充值订单、webhook 路由（路径以 `business_feature_gates.go` 预设为准）；安装 `withBusinessFeatureGates`；`main.go` 调 `SetBusinessFeatureFlags`（默认关闭，环境变量开启）；补齐 `handleCreateRechargeOrder`/`handleStripeWebhook` 的 httpapi 测试（成功、验签失败、重复投递、金额不符）。
- **PM-01 订单到期清扫器（1 pd）**：新增 core worker（复用 `buildCoreWorkers` 模式 + leader 语义）：PENDING 超时 → 先查上游（已支付则走补单入账）→ 否则转 EXPIRED 并过期 Stripe session；修复日限额 SUM 缺陷。
- **PM-02 控制台支付最小闭环（2 pd）**：路由器挂入 `PaymentOrders`（管理端）与新建用户充值页（金额选择 → 创建订单 → 跳转支付 → 结果页轮询订单状态）；`SystemSettings` 支付 tab 随 flag 显示。

### 阶段 1 — 国内收款与发卡（合计 ~8 pd）

- **PM-03 EasyPay 适配器（3 pd）**：实现 `Provider` 接口（MD5 参数签名、mapi/submit 两种模式、GET/POST webhook、金额精度校验）；接入既有 provider CRUD 校验规格；沙箱联调用例。
- **PM-04 主动对账（1 pd）**：用户触发的 verify 端点（按 out_trade_no 主动查单并补单）；清扫周期内对近期 PENDING 订单的主动重查。
- **PM-05 兑换码域（4 pd）**：`redeem_codes` 表 + store（memory/postgres 双实现）；类型先做 balance 与 invitation；管理端生成/批量/列表/禁用/过期/删除/导出；用户兑换端点（每用户失败限速、单码防重、事务内入账走 wallet journal）；`create-and-redeem` 幂等管理 API；注册流程接入 invitation 门槛（系统设置开关）。

### 阶段 2 — 定价模型与自助控制台（合计 ~10 pd）

- **PM-06 基准价目表 + 分组倍率（3 pd）**：LiteLLM 格式解析、内置回退表、可选远程同步（哈希比对 + 手动强刷）；`platform groups` 增加 `rate_multiplier`；结算优先级：上游显式价 > 分组倍率 × 基准价；快照语义不变（reserve 时定价冻结）。
- **PM-07 下游自助控制台（5 pd）**：client 角色五件套页面：仪表盘、Key 管理（含配额/有效期字段扩展）、用量明细、充值页（PM-02 扩展）、兑换页；模型广场报价页（分组 × 模型 × 到手价）。
- **PM-08 用户限额与提醒（2 pd）**：用户级并发/RPM 字段与数据面执行点；低余额阈值邮件提醒（复用通知管道，SMTP 配置进系统设置）。

### 阶段 3 — 决策后排期（不在本批承诺）

订阅套餐与窗口限额；OAuth 订阅上游 + 粘性会话 + 5h 窗口 + 令牌刷新；`/v1/messages` 协议桥；更多支付渠道与退款执行。确认 D1–D3 后另立工作包。

## 7. 分阶段 Definition of Done

- **阶段 0**：`go test ./...`（core/agent/contracts）、`npm test && npm run build` 全绿；httpapi 支付测试覆盖成功/验签失败/重复投递/金额不符；本地 Stripe 测试模式完成"创建订单 → 支付 → 回调入账 → 余额可查"手工闭环并留存用量/流水证据；feature flag 关闭时所有支付路由 404/403，`bootstrap-real-gateways.ps1` 回归全绿。
- **阶段 1**：EasyPay 沙箱完成同一闭环；并发双回调测试 0 重复入账；兑换码并发兑换测试单胜出；`create-and-redeem` 重放语义测试（同用户 200 重放/异用户 409）通过。
- **阶段 2**：同一分组内"倍率定价"与"显式定价"上游混存时结算价符合优先级；自助控制台五件套在 client 角色下完成注册 → 充值 → 建 Key → 调用 → 看账全流程演练；管理端视角回归不破坏。

## 8. 测试与验收基线

沿用仓库现有门禁：`make ci`、`gitleaks`、`govulncheck`、控制台 lint/format/build。新增：支付域 httpapi 测试、store 幂等/并发测试（双后端）、清扫器时序测试（可控时钟）、兑换码并发测试。终验脚本建议在 `bootstrap-real-gateways.ps1` 基础上派生 `bootstrap-commerce.ps1`，把 2.1 场景固化为可复现验收。

## 9. 决策点（已确认，2026-08-04）

- **D1 — OAuth 订阅型上游：不做。** E2M 平台上游保持 OpenAI 兼容 base_url + API key 型；粘性会话、5h 窗口成本、令牌刷新、代理域全部移出范围（视同并入第 4.3 节"明确不做"）。阶段 3 相应删去 OAuth 相关条目与 `/v1/messages` 协议桥的订阅号动因（协议桥本身是否做另行评估）。
- **D2 — 纯余额按量，暂不做套餐。** 订阅套餐与窗口限额保持延期（第 4.2 节）；PM-06 不再为 `subscription_type` 预留字段，若未来重启套餐另行评审。
- **D3 — 基准货币 CNY。** 钱包账本与订单以 CNY micros 为基准（与现行 wallet/供给报价一致）；导入 USD 基准价目表（LiteLLM 格式）时按可配置汇率换算为 CNY，并在结算价快照中记录所用汇率。

## 10. 开工顺序与第一批任务

第一批（可立即开工，不依赖决策点）：PM-00 → PM-01 → PM-02 串行为主（同一批文件），PM-03 可并行起步。完成阶段 0 即恢复"下游用户可自助向钱包充值"这一最大业务断点，后续每个阶段结束都有独立可验收的业务增量。
