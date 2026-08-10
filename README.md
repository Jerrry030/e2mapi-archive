# E2M API（已退役 · 归档仓库）

> **本仓库于 2026-08-09 正式退役，不再接受功能迭代。** 产品基座已整体迁移到
> `e2m-platform`（sub2api 深度 fork），理由是本仓与 fork 各自维护着一套数据面，
> 而竞争发生在网关层——fork 的多协议转发、号池调度、粘性与故障转移在每个维度
> 都更强，对标 a6api 的能力建设也全部在那里进行。
>
> 搬家清单两项均已落地 fork：**Connector 控制面**（管理客户自有网关的五动作面，
> 一期已真机验收）与**密钥级智能路由**（本仓 d527bd9/3884098/01f215f/b9bcef9
> 四个 PR 的产品形态与验收方法论，在 fork 上以 WP-P5 重做，闸门 22 段全绿）。
> **商业化闭环（Stripe/EasyPay 充值、兑换码、基准价目表）留在本仓未迁移**——
> 日后 fork 需要变现能力时，这里是移植参考源。
>
> 退役时状态：暗启动，无真实客户数据（下游密钥 0、非零钱包 0、用量 0、支付订单 0）。
> 生产与验收两套 Compose 栈已停机，数据卷保留未删除。代码可读、可运行、可回溯，
> 但任何新需求都应当去 `e2m-platform` 提。

E2M 是面向站长的统一 AI 资源管理与分发平台。当前产品只保留两条边界清晰的链路，但用户、身份、余额、平台资源和平台流量都统一归属 E2M，不存在需要单独登录或运维的第二套分发平台。

```text
站长自有号池 -> 站长网关 -> 模型上游
                    ^
                    | 管理 API（只做运维）
               Connector -> E2M Core

下游 API Key -> E2M Core /v1/* -> E2M 平台上游号池
                     ^
                     |
        E2M Console / E2M 管理 API
```

- Connector 只托管站长自有号池：健康检查、账号读取、启停调度、账号切换和调度屏障。它不代理业务流量，也不接收平台上游密钥。
- e2m-core 原生管理平台分组、上游账号与密钥、下游 Key、统一余额、请求计量、调度、故障转移和协议转发。
- 下游只登录 E2M、在 E2M 储值、使用 E2M 签发的 Key，并请求 E2M 域名。
- Sub2API 的成熟思路可以吸收到 E2M 原生实现中，但生产运行时不部署 Sub2API。
- 首期只做分组内的基础流量转发与错误转移，不做自有号池/低价池/稳定池的比例分发。

## 当前产品边界

### 站长自有号池

一个 Connector 对应一个站长网关实例，并且只声明五类运维能力：

1. `gateway.health`：网关健康检查；
2. `gateway.accounts.list`：读取账号列表；
3. `gateway.schedulable.set`：修改账号是否参与调度；
4. `gateway.switch`：切换账号；
5. `gateway.scheduling.barrier`：应用调度屏障。

网关管理地址和管理凭证只保存在 Connector 私有数据目录。站长自有业务请求始终由站长网关直连模型上游；E2M 或平台流量链路故障不会把这部分请求拖入平台数据面。

### E2M 平台分发

平台分发由 e2m-core 原生承担：

- 平台管理员通过 E2M 管理上游账号及真实凭证；
- 分组定义可购买、可访问的模型产品边界；
- 下游用户共享 E2M 身份与统一余额，获得 E2M 签发的受限 API Key；
- `/v1/chat/completions` 校验 E2M Key、余额和分组权限，随后选择同组可用上游；
- 请求与响应在协议兼容范围内透传，仅在可重试错误发生时转移到其他兼容上游；
- 用量、扣费和请求结果统一记录在 E2M，可从 E2M 管理 API 和控制台查询。

首期平台管理契约：

```text
GET/POST       /api/v1/platform/groups
GET/PUT/DELETE /api/v1/platform/groups/{id}
GET/POST       /api/v1/platform/upstreams
GET/PUT/DELETE /api/v1/platform/upstreams/{id}
POST           /api/v1/platform/upstreams/{id}/test
GET/POST       /api/v1/platform/keys          # 别名 /api/v1/platform/api-keys
GET/PUT/DELETE /api/v1/platform/keys/{id}
GET            /api/v1/platform/keys/{id}/value
GET            /api/v1/platform/wallet
GET            /api/v1/platform/wallet/journals
POST           /api/v1/platform/wallet-adjustments
GET            /api/v1/platform/usage
GET            /api/v1/platform/pricing/preview
GET            /api/v1/platform/model-market
GET            /v1/models
POST           /v1/chat/completions
POST           /v1/messages
```

E2M owns the whole `/v1/` 子树：未实现的端点返回 JSON `404 unknown_endpoint`，
方法不匹配返回带 `Allow` 头的 `405`，都不会落到控制台 SPA。`GET /v1/models`
按调度器同一套候选谓词计算，因此列出的模型就是能调用的模型；它不预扣，也不看
钱包余额（余额为零也能先看清能买什么）。

商业化契约（受 `E2M_ENABLE_PAYMENTS` 门禁，关闭时返回 `404 feature_disabled`）：

```text
GET/PUT        /api/v1/admin/payment/config
GET/POST       /api/v1/admin/payment/providers
PUT/DELETE     /api/v1/admin/payment/providers/{id}
GET            /api/v1/admin/payment/orders
GET            /api/v1/admin/payment/orders/{id}
POST           /api/v1/admin/payment/orders/{id}/cancel
POST           /api/v1/owner/hybrid-supply/recharge-orders
POST           /api/v1/payment/webhooks/stripe/{providerId}
GET/POST       /api/v1/payment/webhooks/easypay/{providerId}
GET/POST       /api/v1/admin/redeem-codes
POST           /api/v1/admin/redeem-codes/create-and-redeem
POST           /api/v1/admin/redeem-codes/{id}/disable
DELETE         /api/v1/admin/redeem-codes/{id}
POST           /api/v1/redeem
GET/PUT        /api/v1/admin/settings/commerce
```

控制台把分组与上游拆为「分组管理」「上游账号」两个管理员页面，支持创建、
编辑、启停与安全退役，并可通过服务端 Vault 凭证测试连接、发现模型；上游的
模型映射与错误冷却规则以表单方式配置。上游下线与分组删除均保留历史账务和
审计记录，不执行破坏性硬删除；有关联路由的分组由 Core 后台持续排空并完成
退役。

“对下游保证成功交付”应落实为可测量的高成功率目标，而不是无条件承诺。系统可以通过健康筛选、同组重试和错误转移提高成功率，但无法在所有兼容上游均不可用、余额耗尽、模型不受支持或请求非法时伪造成功响应。

### 本期明确不做

- 自有号池、低价池和稳定池之间的比例分发；
- 路由偏好覆盖、动态 Offer 和供应商动态承接比例；
- Connector 管理平台上游或承载平台请求；
- 独立 Sub2API 服务、管理端口、数据库、Redis、用户或 Key；
- 同一能力在 E2M 与其他平台之间的双写、镜像余额或二次 Key 交付；
- 供应商自动结算、应付与提现、内容审查、Cyber 风控和 MaiBot；
- 订阅套餐与配额窗口、OAuth 订阅型上游账号、`/v1/messages` 协议桥（2026-08-04 决策）。

平台商业化闭环（2026-08-04）已实现并默认关闭，由 `E2M_ENABLE_PAYMENTS` 统一门禁：

- 自助充值：Stripe 与 EasyPay（易支付）下单、回调验签恰好一次入账、订单到期清扫与主动补单；
- 兑换码：余额码与邀请码、批量生成、面向外部发卡系统的 `create-and-redeem` 幂等接口；
- 注册邀请码门槛（系统设置开关，原子消费）；
- 基准价目表定价（LiteLLM 格式）与分组售价倍率；客户可见的模型市场报价；
- 用户级平台并发与 RPM 限流、平台钱包低余额提醒、平台 Key 有效期；
- 统一设置模块：商务运行参数存库并热生效，环境变量降级为首启种子。

## 仓库结构

```text
app/e2m-core                  E2M API、平台数据面与内嵌控制台
app/e2m-agent                 客户侧 Connector
packages/e2m-contracts        Core / Connector 共享契约
web/console                   React + TypeScript 控制台
deployments/templates         Docker Compose 与部署示例
scripts                       本地启动、验收和工程命令
docs/development              当前状态与产品边界
```

## 本地验证平台基础转发

Windows PowerShell：

```powershell
.\scripts\bootstrap-real-gateways.ps1
```

脚本只启动三个服务：

- E2M PostgreSQL；
- mock OpenAI 上游（仅 Compose 内网可见）；
- e2m-core。

随后脚本只调用 E2M：登录 E2M，创建平台分组和 mock 上游，为当前测试用户增加余额，创建 E2M 下游 Key，并通过 E2M `/v1/chat/completions` 验证普通 JSON 与流式 SSE 请求，最后从 E2M 查询用量。脚本不登录、不启动也不访问 Sub2API。

本地地址与产物：

- E2M 产品入口：`http://127.0.0.1:18080`
- 验收生成的下游 Key：`deployments/runtime/platform-forwarding/downstream.key`

栈已运行时可跳过构建和启动：

```powershell
.\scripts\bootstrap-real-gateways.ps1 -SkipComposeUp
```

Compose 使用本地默认密码和 mock 上游，只用于开发验收，不是生产模板。脚本不会执行 `down -v`，不会删除数据库迁移、Volume 或历史数据。

## 接入站长自有号池

在 E2M 创建托管实例并生成一次性 Connector 安装信息。Connector 主动向 Core 轮询，不要求暴露站长网关的管理端口。

每个实例使用独立数据卷。首次启动后，从数据卷读取 `local-ui.token`，打开：

```text
http://127.0.0.1:18081/#token=e2m_local_...
```

在本地页面填写站长网关类型、管理地址和管理凭证，完成连通性测试后保存。详见 [Connector 开发与部署说明](docs/development/e2m-agent.md)。

## 开发与验证

```powershell
cd app/e2m-core
go test ./...

cd ../e2m-agent
go test ./...

cd ../../web/console
npm test
npm run build
```

验证最小 Compose：

```powershell
docker compose -f deployments/templates/compose/e2m-core-real-gateways.compose.yml config
```

## 当前文档

- [当前实现状态](docs/development/current-state.md)
- [平台职责边界](docs/development/platform-boundaries.md)
- [平台商业化执行计划](docs/development/platform-commerce-execution-plan.md)
- [功能开关与运行参数](docs/development/feature-flags.md)
- [Connector 开发与部署](docs/development/e2m-agent.md)
- [工程化基线](docs/development/engineering.md)

旧 roadmap、progress 和早期平台蓝图仅保留历史决策过程，不代表当前产品范围。带
「历史文档」或「该子系统当前未挂载」横幅的文档同理：其中的能力描述不等于当前
可达能力。
