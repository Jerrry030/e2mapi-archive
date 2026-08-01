# 当前闭环、Core / Connector 边界与本地数据源

更新时间：2026-07-15

## 当前结论

当前第一主路径是平台维护全平台共享上游目录，并把每个来源的独占 Key 分配、发布到用户网关；代用户轮换网关中已有账号是次要路径。资源直接归属 `users.id`，每个 Instance 运行且只绑定一个出站 Connector。Core 保存账号归属、实例元数据、Connector 身份、任务、平台交付 Key 的独立 Vault 引用和非敏感运行摘要；Connector 在用户侧访问网关。Core 不接收、保存或使用网关管理地址、认证方式和管理凭证，也不提供任何由 Core 直接连接用户网关的兼容路径。

已具备前端入口的闭环包括注册 / 登录、实例登记、Connector 安装与本地配置、账号读取、调度启停与切换、健康与自动切换、通知、供给、发布 / 回滚、审批、审计和账单。这里的“已具备入口”不等于所有 typed action 都已由 Connector 执行，当前动作支持矩阵见下文。

## 资源归属边界

- `users.id` 是账号和资源隔离主键，业务表使用数值 `user_id`（供给侧对应 `supplier_user_id`）。普通用户请求由当前登录账号确定归属；平台管理员才可跨账号查看或操作。
- Instance 只保存 `id`、`user_id`、名称、网关类型、状态和 `connector_id`。创建 / 更新接口采用字段白名单，拒绝 version、labels、网关地址、认证方式、管理凭证及其他未知字段；在线时间和运行版本只来自 Connector，Instance 不保留重复字段。
- Connector enrollment 在生成时已绑定确切的 `user_id + instance_id + connector_id`。Connector 不能自行声明所属账号或改绑到别的 Instance。
- 一个 Instance 对应一个 Connector。数据库对 `connectors.instance_id` 唯一约束，未使用的 enrollment 也按 `instance_id` 唯一；重新安装复用已绑定的 Connector 身份，而不是创建第二个运行时。

## Core / Connector 边界

| Core 负责 | Connector 负责 |
| --- | --- |
| 登录、RBAC 和按 `users.id` 的资源授权 | 在用户侧、靠近网关运行 |
| Instance / Connector 身份和一对一绑定 | 保存网关管理地址、认证方式和管理凭证 |
| 生成一次性、精确绑定的 enrollment | 将 typed action 映射为 sub2api / new-api / CPA 原生 HTTP 调用 |
| 创建和持久化 typed task，校验租约、结果 DTO、风险级别和幂等键 | 轮询 Core、校验协议 / schema / Instance 身份、执行并回传 typed result 或结构化错误 |
| 保存非敏感运行摘要：配置是否完整、网关类型、测试状态、能力、严格语义化版本和 `last_seen_at` | 过滤原生响应，只回传协议定义的字段；不回传主机名、URL、凭证或原始响应 |
| Core Vault 保存通知 Webhook、平台交付 Key 等确实由 Core 使用的非网关业务凭证；交付 Key 只用于受控展示 | `gateway-config.json` 保存网关本地配置，write-only binding store 保存生命周期任务使用的凭证/代理绑定，`connector.token` 保存运行 token，`local-ui.token` 保护本地 UI |

硬边界：Core 的 Instance API、typed task 和运行摘要里都没有网关 URL、HTTP method/header、认证方式或管理凭证明文。Core Vault 不是网关管理凭证库。Connector 只主动向 Core 发起 enrollment、task lease、completion 和脱敏 observation 上报请求；没有 Core 直接访问用户网关的路径。

## 当前安装与本地配置

1. 用户在控制台登记 Instance，只填写实例名称和类型（`sub2api`、`newapi` 或 `cpa`）。创建成功后 Core 返回该 Instance 的 Connector 安装引导；也可从实例或 Connector 页面重新生成。
2. 控制台只显示一次 enrollment token。先将其写入私有文件，再运行页面生成的 Compose 或 Docker 命令。命令只携带 Core URL、`connector_id`、`instance_id` 和 token 文件路径，不携带网关地址或管理凭证。
3. Connector 从 `E2M_CONNECTOR_ENROLL_TOKEN_FILE` 读取一次性 enrollment token。enrollment 成功后，Core 只保存 token hash；Connector 将新运行 token 原子写入 `E2M_CONNECTOR_TOKEN_FILE`（默认持久卷内的 `connector.token`）。两类 token 均为 file-only，不通过命令行参数或环境变量传值。
4. Connector 在本地数据卷生成 `local-ui.token`。从该文件读取值，用 `http://127.0.0.1:18081/#token=<value>` 打开本地 UI；远程机器先建立 SSH loopback tunnel。fragment 不会发送到 HTTP 服务，页面随后用 `X-E2M-Local-Token` 调用本地 API。容器模板只为 Docker 网桥显式放行私网 peer，同时仍强制宿主 loopback 端口、loopback Host、same-origin 和 token；裸机默认只接受 loopback peer。
5. 用户在本地 UI 填写并测试网关类型、管理地址和凭证：sub2api 使用 `x-api-key`，new-api 使用 user ID + token，CPA 使用 bearer token。配置写入 Connector 私有数据目录的 `gateway-config.json`，文件权限收敛为 `0600`，凭证不会由本地 API 回显，也不会上传 Core。
6. 平台生命周期渠道使用的 `credential_binding_id` / `proxy_binding_id` 只作为不透明 ID 出现在 Core。实际值通过 Connector loopback-only 的 write-only binding API 写入其私有目录并以 `0600` 保存；接口只返回保存数量，不回显值。Connector 仅在构造原生 create/update 请求时解析绑定。

如果用户在一次性 token 显示后、Connector enrollment 前中断，再次生成该 Instance 的安装指南会原子替换其未使用 enrollment；旧 token 立即失效，新 token 可直接继续安装。已使用的 Connector 身份不会被这条恢复路径替换，重装仍复用原 `connector_id`。

运行 token 轮换后，旧 token 立即失效，必须把新值写回该 Connector 的 `connector.token` 文件；吊销会解除 Instance 绑定。`local-ui.token` 是第三个独立的本地访问 token，不是 enrollment token 或 Connector 运行 token。

## Typed Protocol v2

协议版本常量为 `ConnectorProtocolVersion = 2`，当前 task `schema_version = 1`。Core 与 Connector 在 enrollment 和 lease 时双向校验协议版本；不匹配返回升级错误。task 还绑定 `user_id + instance_id + connector_id`，Connector 在访问网关前再次校验 `connector_id`、`instance_id` 和 schema。调度与生命周期写入携带单调 generation fence；晚到旧任务会在访问网关前被拒绝。

Core 发送封闭的 domain action 和严格 DTO，而不是任意 URL、method、header 或 body。Connector 成功结果也必须匹配 typed result；账号字段经过长度、字符集和敏感值校验，未知字段、旧式 raw response、成功但无 result、失败同时带 result 都会被 Core 拒绝。错误上报只有白名单 code 与 retryable，不接收原始 message。写动作带幂等键；Connector 会重放同一键 / 同一输入的成功结果，并拒绝同键不同输入。

| Typed action | 风险 | 当前状态 |
| --- | --- | --- |
| `gateway.health.get` | L0 | 支持。Connector 通过对应网关的账号列表管理接口验证可达性和认证，返回 `status`、`checked_at`。 |
| `gateway.accounts.list` | L0 | 支持 sub2api / new-api / CPA，返回统一 `GatewayAccount[]`。 |
| `gateway.account.schedulable.set` | L1 | 支持三类网关，映射为账号、channel 或 auth-file 的启停。 |
| `gateway.account.switch` | L1 | 支持。先停用问题账号，再按需启用备用账号；当前由两次 schedulable 写入组合完成。 |
| `gateway.scheduling.barrier` | L1 | 支持。只推进 Connector 本地持久化 fence，不调用网关，用于使旧 generation 的调度/删除任务失效。 |
| `gateway.account.quality.probe` | L0 | Sub2API 在本地显式开启且预算/间隔有效时支持账号级 SSE 探测；NewAPI / CPA 明确为人工恢复，不广告自动探测能力。 |
| `gateway.binding.proof` | L0 | 按当前网关类型从 Connector 本地 binding 提取实际 API Key，以随机 challenge 返回 HMAC；不回传 Key，也不宣称远端已读回。 |
| `gateway.account.create` | L2 | 三类网关均支持平台账号的幂等创建/接管；用户自有账号禁止创建。 |
| `gateway.account.update` | L2 | 三类网关均支持，平台账号和用户自有账号都可更新。 |
| `gateway.account.delete` | L2 | 只允许平台账号。发布引擎先停用，持久任务在 30 分钟后执行删除；用户自有账号禁止删除。 |

Protocol v2 仍是封闭动作协议，不接受任意 URL、method、header 或 body。生命周期 Apply 会先对完整 diff 校验 Connector 能力和不可变 `account_ownership`；任何不支持或越权动作都在网关写入和永久分配前整体拒绝。平台账号允许 create/update/delete，用户自有账号只允许 update。生命周期动作只携带 Connector 本地 binding ID，不携带真实凭证。交付 Key 的本地随机挑战匹配与远端发布回执分开记录：只有当前 `key_version` 在对应 Instance 上收到 create/update 成功回执后才视为已部署；离线、错配、旧版本或写入失败均禁止明文查看。该回执不等同于远端凭证读回。

Connector 的 task lease 同时承担在线观测：空闲时按 Core 返回的 `next_poll_after`（当前默认 `5s`）轮询；状态无变化时 `last_seen_at` 最多每 `15s` 持久化一次，超过 `60s` 未见则控制台显示 offline。当前没有独立的存活上报协议；在线状态来自 typed task 轮询本身。

网关健康策略由 Core 按 Instance 保存：监控开关、`30/60/300s` 检查间隔、连续失败次数、自动切换、冷却时间和漂移检测均可在控制台调整，并支持立即检查。Connector 本地只管理网关请求超时、日志级别及脱敏连接诊断，不向普通用户开放 lease、并发、重试或协议参数。

## 当前用户闭环

| 环节 | 当前实现 |
| --- | --- |
| 注册 / 登录 | 邮箱唯一登录；公开注册由平台管理员控制。新账号直接成为资源边界。 |
| 登记 Instance | 普通用户只填名称和网关类型，资源自动归属当前 `users.id`。 |
| 安装 Connector | Instance 创建后生成一对一安装引导；enrollment 精确绑定且单次使用。 |
| 配置网关 | 在 Connector 本地 UI 填写 URL、认证和管理凭证，测试后写入私有持久卷。 |
| 读取与处置 | 账号列表、单账号调度启停、一键切换走 protocol v2 typed task；批量处置可进入审批。 |
| 健康与自动切换 | 控制台可查看健康快照、事件和自动切换决策；Sub2API 可在显式开关和预算内主动探测，NewAPI / CPA 显示需人工恢复；恢复按 `10% -> 25% -> 50% -> 100%` 回归。 |
| 通知 / 供给 / 审计 / 账单 | 已有控制台和 API；这些 Core 业务凭证与网关管理凭证是不同边界。 |
| 发布 / 回滚 | 规划、预检、审计、调度变更和 Connector v2 生命周期动作可用。平台账号可创建、更新、延迟删除，用户自有账号只更新；回滚停用但不删除远端账号。 |

## 本地数据源与访问入口

| 环境 | 控制台 | Core 数据源 | 网关数据源 | 用途 |
| --- | --- | --- | --- | --- |
| Mock 开发栈 | `http://localhost:8080` | Compose 项目下的 `e2m-pgdata`（常见完整名 `compose_e2m-pgdata`） | `mock-sub2api`、`mock-newapi`、`mock-cpa` 进程内数据 | 快速开发和适配器 / CI 验证 |
| 真实网关集成栈 | `http://localhost:18080` | `e2m-real-gateways_e2m-real-pgdata` | sub2api、new-api、CPA 各自的独立数据库 / 数据卷 | 当前真实能力验收 |

两套 Compose 使用不同项目名 / 数据卷，打开 `:8080` 看到 Mock 数据不表示 `:18080` 的真实栈数据丢失。Core PostgreSQL 只保存控制面数据；网关账号、channel 和 auth-file 的事实来源仍是各网关，Core 经每个 Instance 的 Connector 按需读取统一视图。

真实栈固定入口：

| 服务 | 地址 |
| --- | --- |
| E2M Core / Console | `http://localhost:18080` |
| sub2api | `http://localhost:18090` |
| new-api | `http://localhost:13000` |
| CPA | `http://localhost:18317` |

启动真实集成栈并初始化每实例 Connector：

```powershell
docker compose -f deployments\templates\compose\e2m-core-real-gateways.compose.yml up --build -d
.\scripts\bootstrap-real-gateways.ps1 -SkipComposeUp
.\scripts\seed-real-gateways.ps1
```

`bootstrap-real-gateways.ps1` 是本地测试夹具：它为每个真实 Instance 生成 / 复用独立 Connector，写入各自 enrollment token 文件，并把网关配置直接初始化到各自私有 Connector 卷。可用 `-Gateway sub2api`、`newapi` 或 `cpa` 只初始化单个网关，避免无关网关的凭证轮换或配置写入；默认 `all`。它没有把网关管理地址或凭证写入 Core。不要使用 `down -v`，除非明确要删除真实集成数据。

## 2026-07-10 Protocol v1 历史验收基线

以下记录是 2026-07-10 当时的真实运行验收事实，用于保留升级前证据，不代表当前能力。当前协议已升级为 v2，动作约束、生命周期、本地 binding 和主动探测以本文前述现状为准。

- 使用空 PostgreSQL 初始化到 migration `20`，`dirty=false`。新库没有 `tenants`、`adapter_capabilities`、`agent_heartbeats`，Instance 没有 `tenant_id`、`project_id`、`endpoint_ref`、`credential_ref`、`version`、`labels`、`agent_id`、`last_seen_at`，Connector 没有 `hostname`。
- `connector_tasks.type` 的数据库约束只允许 protocol v1 四项动作；`connectors.instance_id`、`instances.connector_id` 和未使用 enrollment 的 `instance_id` 均保持一对一约束。
- 真实 sub2api Connector `conn-2f7db2205cb407fb` 已绑定 Instance `inst-c7a816572bd9bc65` 并在线，运行摘要只含网关类型、配置状态、协议版本和四项能力。
- 真实读取返回账号 `2`、`1`。账号 `2` 已实际执行 `true -> false -> true -> false -> true` 往返写入，sub2api 访问日志确认每次均命中原生 schedulable API，最终恢复原状态。
- Core 环境、日志及数据库均未发现 `http://sub2api:8080` 或 sub2api 管理 key；Connector 环境也不含二者。URL 与凭证只存在 Connector 的 `gateway-config.json`，该文件以及 `connector.token`、`local-ui.token` 权限均为 `0600`；一次性 enrollment 文件已清空。
- 为落到最终 schema，只重置了 E2M Core PostgreSQL 卷与 sub2api Connector 私有卷；sub2api、new-api、CPA 的数据库、Redis、data、auth、log 卷均未删除或重置。重置前的 Core 控制面备份保存在 `deployments/runtime/real-gateways/e2m-core-pre-final-schema-reset-20260710.dump`。
