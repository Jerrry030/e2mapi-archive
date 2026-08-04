# 上游情报与安全优化闭环执行计划

> **该子系统当前未挂载。** 截至 2026-08-04，本文描述的 HTTP 端点未在 `internal/httpapi/server.go` 的 `Routes()` 中注册，处于暗启动状态：代码与测试存在，但通过当前 E2M API 不可达。本文保留为设计与验收参考，不代表当前可用能力。现状见 [current-state.md](current-state.md)。

更新：2026-07-27

状态：UI-00..17 已完成本地/disposable 实现与可复现终验。2026-07-27 的冻结候选在同一 849-file canonical build-input SHA-256 `ee8eba1066b55a578e938ffb27d23dc3228545e49738d4fd99955d489474281b` 上通过现行 schema-4 三来源情报、schema-6 NewAPI 加权闭环、PostgreSQL、Linux race、安全、性能、Prometheus、双语/等价表格/移动布局及生产资产浏览器门禁，UI-17 正式签收。这里的签收限定于当前工作树和隔离夹具，不表示公开发布、生产部署或真实模型流量验收

目标：在不改变 E2M 控制面边界的前提下，形成“上游情报 → 成本/质量决策 → 安全执行 → 结果验证”的可信闭环，全面超越仅提供倍率探测与看板的同类项目。

实施进度：

- 2026-07-24：第一批 `UI-00`、`UI-01`、`UI-03` 开工；本批只交付事实契约、Core 持久化基础和 Connector 本地多来源配置，不宣称已支持真实倍率采集。
- 2026-07-24：`UI-00..06` 已实现：decimal/证据契约、Core 原子 ingest 与两轮 complete-only 变化检测、Connector 本地多来源 Sub2API 只读采集、独立调度与 durable outbox、typed manual collect 和 opaque source 映射。
- 2026-07-24：`UI-02` 留存收口：90 天 owner-scoped bounded retention worker 已接入 Core；当前读模型和变化事件引用的证据受保护。无 `E2M_TEST_POSTGRES_DSN`，PostgreSQL 仅完成 SQL/静态验证，不宣称实库通过。
- 2026-07-24：`UI-07` 管理端一致读 API 已签收：七个 admin-only、owner-scoped 端点，HMAC-SHA256 cursor v2、固定 reference time、15 分钟 TTL、四密钥轮换、严格 JSON 与敏感字段防泄漏测试通过；`UI-08/09` 看板、钱包、倍率、变化和证据下钻已完成。
- 2026-07-24：`UI-10` 显式 source/channel 映射、管理员专用 Link 管理 API（GET/POST/PUT，仅修改 Core 映射，不触发上游操作）、owner 安全质量连接与真实 Pareto 已实现；固定使用 5 分钟质量窗口，unknown/样本不足/陈旧/未映射分别阻断，多来源（三个可比较候选，另含阻断样例）MemoryStore HTTP 集成验收通过。source identity 已统一收紧为短 opaque ID，并在 HTTP、MemoryStore、PostgreSQL Store 三层拒绝 URL/token/cookie/header/credential/raw-response 形态。无 `E2M_TEST_POSTGRES_DSN`，PostgreSQL 仅完成 `0060` migration SQL 与 PG 实现的源码级静态回归，不宣称 migration 已实际应用或实库通过。
- 2026-07-26：`UI-11..15` 的时间化成本账本、覆盖率毛利护栏、可解释建议、shadow/dry-run、执行策略与授权桥已完成本地实现和定向回归；`UI-16` 已接通 10% → 25% → 50% → 100% 灰度、阶段观测、完整权重 baseline、generation/lease fence、崩溃恢复、全量 read-back 和精确回滚。这里的“完成”仅指本地实现与可复现实证，不包含真实来源或 disposable 网关签收。
- 2026-07-26：`UI-17` 已完成本地 MemoryStore 100 来源/5,000 facts 语义与诊断性能检查、响应安全检查、低基数 metrics/alerts、Prometheus 规则容器验证、failure drills、运维手册和控制台回归；全新 disposable PostgreSQL 16 空库已实跑 owner-scoped 并发 ingest 配额、窗口/事实限额、幂等免费重放、429 精确 Retry-After、过期容量账本有界清理、90 天分批 retention/current/reference 保护/fact version，以及 100 来源/5,000 当前事实/400 天 40,000 change events 的双 P95/EXPLAIN。保留的权威日志直接记录 current-read P95 79.4694 ms、rollup P95 60.1667 ms，均通过 2s 本机诊断门槛和 EXPLAIN；对应 Linux race 也通过。
- 2026-07-27：Connector 写协议冻结为 v3。route-plan fenced mutation 必须把精确 live lease 兑换为 Core 持久化的 `executing` permit 后才能触发网关副作用；`executing` 不按旧 lease deadline 自动超时或重试，并阻断 plan generation 变更/删除。远端结果不确定时保持冻结，由平台管理员在停机和对账后通过三态人工恢复终结。存量 protocol-v2 Connector token/身份仍可认证和读取，但不能 lease/execute；只有真实 v3 runtime handshake 持久化升级后才可取任务。该增量的 contracts/Agent/Core 全包测试与 vet、fresh migration 74 PostgreSQL 全 Store、0074 live migration、PostgreSQL execution/resolve 并发门禁及固定 Linux Go Alpine 容器 race 均已通过；Core Store/HTTP/rollout race 最慢包 33.0s，Agent Connector/Sub2API race 最慢包 2.651s。全包测试为缓存命中，但 race 为本次 v3 选择器的实际执行证据。
- 2026-07-27：生产资产浏览器终验通过：390×844 视口的 document client width 为 375，中英文页面级横向溢出均为 0，100 来源/5,000 facts 均完整；宽表只在明确 ARIA 区域内滚动，两种语言都有图表等价 `<table><caption>`。原生“查看证据”按钮支持 Enter 打开 Drawer、焦点进入，Escape 关闭并恢复到同一触发按钮；XSS sentinel 为 null、console errors 为 0。控制台最终门禁为 58/58 files、201/201 tests，加 lint、format-check、production build 和两类 npm audit 0 漏洞；嵌入资产与刚构建 dist 哈希一致。
- 2026-07-27：现行正式 release 全部通过。三来源 Intelligence 项目 `e2m-ui17-intel-29204-cabbadca3aea` 的 schema-4 证据确认两 external + 一 owned、bearer-only 采集、durable outbox replay、失败隔离/stale/recovery、两次 complete snapshot 删除确认、浏览器回读与敏感边界，证据 SHA-256 为 `7016982ae6eb2eb5c95661c108c8158a1c17e0cb08c23a5c3c2c6248747de63b`。NewAPI 项目 `e2m-ui17-newapi-20260727164048-f95a6076` 的 schema-6 证据确认 10%→25%→50%→100%、阶段观测、精确 baseline read-back、质量回归自动 rollback、running/stale lease fence、protocol-v3 execute-conflict 零远端副作用与人工 resolution，证据 SHA-256 为 `56ee92f60406fd454f54e62ff7a12eddbcdea01175ca7c93f835e73f40bc0abf`。两项均为 `release + SourceFrozen`、`release_pass=true`，runner/Compose/build inputs 全程不变，精确清理后 containers/volumes/networks 均为 0，runtime 删除、环境恢复且受保护栈 ID 不变；无跳过的 UI-17 外部门禁。
- 2026-07-28：机器可读最终签收入口为 `.tmp/ui17-evidence/ui17-final-signoff-20260727/manifest.json`，SHA-256 `3fbc011f5013f457d1e10dfefc06987e35596b65d6e40a8c3a4303b27a1f1d3c`；它只汇入现行 schema release 与独立通过的专项证据，明确排除 test-only、历史 schema、失败、跳过和 release-ineligible artifact，并记录非生产边界与已知局限。

## 1. 决策摘要

本计划只聚焦上游倍率探测、价格/余额情报、看板、成本洞察和安全决策闭环。公开 `main`、正式发布和版本迭代节奏不在本计划内；当前项目初稿未定，不以发布压力改变领域设计。

六个阶段按以下顺序推进：

1. 建立带来源、时间、覆盖度和证据状态的事实模型。
2. 在 Connector 内实现 Sub2API 多来源只读探测。
3. 交付独立的“上游情报”看板，完成 Intelligence MVP。
4. 建立时间化成本账本和毛利护栏。
5. 生成可解释建议，并以 shadow / dry-run 验证。
6. 复用现有围栏、审计、回滚和分段恢复能力形成自动闭环。

阶段 3 是首个产品验收点：此时体验和信息可信度应已超过 AIZZZWatch 一类看板；阶段 6 是结构性领先点：竞品展示信息，E2M 能把信息转成有证据、可审计、可验证、可恢复的执行。

竞品基线固定为审查时的 AIZZZWatch commit `b13d459`。借鉴其来源钱包、有效倍率、倍率变化历史和写入前确认，不复制 Electron + 本地 JSON 的产品架构；后续比较也必须固定 commit/版本，避免用移动目标证明“超过”。

## 2. 成功定义

### 2.1 Intelligence MVP 场景

用固定验收夹具部署两个外部 Sub2API 来源和一个自有 Sub2API 站点：

- 新配置来源在 10 分钟内显示余额、分组倍率、充值兑换率、模型价格和证据状态；
- 价格、倍率、分组或余额变化在两个轮询周期内被发现；
- 一个来源超时、限流、认证失败或返回部分数据时，其他来源仍正常更新；
- 失败后保留最近一次成功快照并标记过期，不清零、不伪造；
- 缺少倍率、币种、计价单位或价格时明确显示“未知/不可比较”；
- 可以产生一条基于确定性规则的只读机会提示（含公式、证据和阻断原因），但不会生成 dry-run 或自动执行；
- Core 的请求、数据库、日志、审计和浏览器响应中均不存在来源 URL、管理凭证、token、cookie 或原始响应。

### 2.2 最终闭环场景

在证据成熟且质量门槛满足后，一条成本优化建议能够依次完成 shadow、dry-run、人工批准或策略授权、10% → 25% → 50% → 100% 灰度、阶段观测和最终验证；任何质量或容量回归均触发停止扩大或回滚，并留下完整的前后证据和审计链。

灰度百分比表示“从来源账户原始 baseline 迁出的比例”，不是把整个计划重写成只有两个账户。启动时捕获该 plan/instance 的完整 managed-binding 权重集：账户唯一、每个权重已知且在 0..100 内、总和精确为 100。来源账户 baseline 必须大于 0；目标账户可以从 0 或既有非零权重开始；所有无关账户允许非零，并在每一级灰度和回滚中原样保留。整数权重按确定性的 round-half-up 计算，100% 强制来源归零并把其原始 baseline 全部转给目标。

### 2.3 核心 KPI

| 指标                         | 目标                                |
| ---------------------------- | ----------------------------------- |
| 首次证据时间                 | 中位数 ≤ 10 分钟                    |
| 变化发现延迟                 | ≤ 2 个轮询周期                      |
| 部分页导致的错误删除事件     | 0                                   |
| 情报采集对请求数据面的影响   | 0；E2M 仍不代理请求                 |
| 凭证进入 Core 的事件         | 0                                   |
| 毛利结论前的可归因成本覆盖率 | ≥ 90%                               |
| 自动动作证据完整率           | 100% 具有 before / decision / after |
| 优化效果                     | 成本下降，成功率与当前验收夹具的 TTFT 门槛无回归；生产 SLO 另行验收 |

## 3. 不可破坏的架构边界

1. E2M 是控制面旁路系统，不进入请求数据面。
2. Core 不接收或保存上游 URL、认证方式、管理凭证、token、cookie 或未经清洗的原始响应。
3. 所有来源凭证只保存在 Connector 私有目录，并继续采用 loopback、本地 token、`0600` 文件权限和不回显策略。
4. Core 只保存 owner 作用域内的 opaque source identity、展示元数据、规范化事实、能力摘要、覆盖度和证据。
5. Core 只发送封闭 typed task，不发送任意 URL、method、header 或 body。
6. 旧 Connector 未声明新 capability 时，Core 不得下发刷新任务。
7. `ChannelHealthSnapshot` 继续表示服务质量；`UpstreamChannel.SourceID` 继续表示稳定供给来源；价格历史不得塞入二者。
8. `UpstreamChannel.CostHint` 暂作兼容的人工提示值，不能作为新账本的事实来源，也不得静默由探测结果覆盖。
9. 采购价、余额和倍率初版仅管理员可见；client 和 supplier API 必须服务端拒绝，不能只靠前端隐藏。

## 4. MVP 范围与明确延期

### 4.1 MVP 包含

- `owned` 和 `external` 两种 Sub2API 来源；
- Connector 本地多来源配置、连接测试、启停和立即采集；
- 余额、分组、分组倍率、充值兑换率、模型输入/输出价格；
- 完整/部分/不可用采集、最近成功快照、变化事件和证据下钻；
- 有效倍率和有效单位成本的规范化计算；
- 来源钱包、倍率/价格排行、24h/7d 变化、余额风险、成本—质量 Pareto 和只读机会提示；
- 管理员只读查看和手动刷新；阶段 5 以前不执行建议。

### 4.2 延期项

- NewAPI、CPA 和任意其他站点的价格采集；
- 通用 URL/method/body 适配器或任意远端写映射；
- 自动充值、支付、平台钱包、结算和供应市场；
- 无可信汇率证据时的跨币种汇总；
- Electron、悬浮窗、桌面通知；
- 请求数据面代理和完整逐请求财务核算；
- AI 自由文本建议、价格预测和自定义 BI；
- 证据成熟前的“自动选择最便宜来源”。

## 5. 目标架构与数据流

```text
Connector local UI
  └─ sources.local.json (URL/credential, never uploaded)
       ├─ owned: reuse managed gateway config
       └─ external: independent read-only Sub2API config
              ↓ bounded poll / refresh task
       Sub2API intelligence collector
              ↓ normalize + validate + redact
       durable local snapshot/outbox
              ↓ authenticated sanitized batch ingest
Core upstream-intelligence domain
  ├─ runs + observations + change events
  ├─ current/history read models
  ├─ quality join + recommendation engine
  └─ temporal cost ledger
              ↓ admin-only API
Console /upstream-intelligence
              ↓ Stage 5 shadow/dry-run
existing AutoSwitchDecision + reconcile + audit
              ↓ Stage 6 guarded execution
fence → canary → observe → expand/rollback → verify
```

采集是 Connector 主动出站。定时采集由 Connector 本地调度器负责，即使 Core 暂时不可用也写入本地 outbox；“立即刷新”可通过一个新的 L0 typed task 触发。任务完成结果只携带 run summary，较大的规范化 observations 通过独立 ingest 接口批量上传。

## 6. 领域模型和证据语义

所有新情报业务实体都带 `user_id`，所有查询和唯一键都必须包含 owner 作用域。MVP 中平台自有采购来源归属平台管理员 owner；若将来需要跨 owner 共享，必须另立授权/投影模型，不能省略归属键。金额、价格、倍率和汇率在 PostgreSQL 使用 `NUMERIC(38,18)`，在 JSON 使用规范十进制字符串；禁止用 `float64` 作为资金事实。输入最多 38 位有效数字和 18 位小数，不接受指数、`NaN`、无穷、前导正号或负零。Core 使用同一个 decimal 实现完成校验和运算，按 half-even 保留 18 位，前端只做展示格式化。时间统一 RFC3339 UTC。

### 6.1 `UpstreamIntelligenceSource`

| 字段                                             | 约束/含义                                                   |
| ------------------------------------------------ | ----------------------------------------------------------- |
| `id`, `user_id`                                  | Core 生成的 opaque ID 和资源归属                            |
| `connector_id`, `instance_id`                    | 必须属于同一 owner；external 仍绑定一个负责采集的 Connector |
| `local_ref`                                      | Connector 生成的不透明引用；不是 URL 或凭证引用             |
| `mode`                                           | `owned \| external`                                         |
| `provider`                                       | MVP 固定 `sub2api`                                          |
| `display_name`                                   | 经长度和字符集校验的展示名                                  |
| `currency`                                       | ISO 4217；未知则为空，不猜测                                |
| `poll_interval_seconds`                          | MVP 默认 300，允许 60..3600                                 |
| `status`                                         | `active \| paused \| disconnected`；物理删除不属于 MVP      |
| `capabilities`                                   | `balance, groups, rates, prices` 的布尔摘要                 |
| `last_run_at`, `last_success_at`, `next_poll_at` | Core 展示时间，不代表凭证可用性                             |
| `last_coverage`, `last_error_code`               | 只允许规范化枚举，不收原始消息                              |

来源在 Core 的创建采用“Connector 本地先保存 → 上传 sanitized registration”的方向。Core 页面只展示注册结果；编辑 URL/凭证必须回到 Connector 本地 UI。

### 6.2 `UpstreamCollectionRun`

| 字段                                                       | 约束/含义                                   |
| ---------------------------------------------------------- | ------------------------------------------- |
| `id`, `source_id`, `connector_id`                          | run ID 在重试间保持稳定                     |
| `trigger`                                                  | `scheduled \| manual \| task`               |
| `status`                                                   | `running \| succeeded \| partial \| failed` |
| `coverage`                                                 | `complete \| partial \| unavailable`        |
| `started_at`, `observed_at`, `received_at`, `completed_at` | 区分上游观测、Core 接收和完成时间           |
| `snapshot_hash`                                            | 规范化内容哈希，用于幂等和无变化识别        |
| `fact_count`, `page_count`                                 | 有界非负整数                                |
| `error_code`, `retryable`                                  | 白名单错误码；不含原始响应                  |

### 6.3 `UpstreamWalletObservation`

一条 wallet observation 是某次 run 的 source-level 余额事实：`id`, `run_id`, `source_id`, `balance_amount`, `unit_kind`, `currency`, `observed_at`, `received_at`, `fresh_until`, `accuracy` 和 `missing_fields`。`unit_kind` 为 `fiat | credit | unknown`；只有 `fiat` 才允许 ISO 4217 `currency`，credit 不得伪装成法币。负余额仅在上游明确返回且 adapter capability 声明支持欠款语义时接受。

### 6.4 `UpstreamOfferObservation`

一条 observation 表示某来源、分组、模型、计价方向在一个有效时间点的不可变事实。至少包含：

- identity：`id`, `run_id`, `source_id`, `group_key`, `model_key`, `price_dimension`；
- published facts：`settlement_currency`, `group_multiplier`, `recharge_yield`, `published_unit_price`；
- unit：`input \| output \| cached_input \| request` 和 `per_tokens`（通常 1,000,000）；
- derived facts：`effective_multiplier`, `effective_unit_cost`, `formula_version`；
- evidence：`accuracy`, `coverage`, `observed_at`, `effective_at`, `received_at`, `fresh_until`, `missing_fields`；
- versioning：`adapter_schema_version`, `source_revision`（若上游提供）。

`effective_at` 优先使用上游明确提供且可验证的生效时间，否则使用 `observed_at` 并把时间精度记入 evidence；禁止把首次发现的价格回填到更早历史。下一次完整快照发现变化时关闭上一事实区间。钱包余额不复制到每个模型行；读模型再组合 wallet 和 offer facts。

### 6.5 `UpstreamIntelligenceLink`

情报来源与现有供给/质量来源必须通过显式映射连接，不能用展示名或相似字符串猜测。link 至少包含 `id`, `user_id`, `intelligence_source_id`, `link_scope`, `upstream_source_identity`, `channel_id`, `status`, `verified_at`；`link_scope` 为 `source_identity | channel`，二者必须且只能填一类目标。目标 owner/scope 必须在服务端验证，一条 channel 在同一价格维度只能有一个 active link。未映射的价格仍可展示，但不能进入 Pareto、成本归因或切换建议。

### 6.6 正交证据维度

证据不能只用一个互斥状态表示。至少分开保存：

- accuracy：`exact \| derived \| estimated \| unknown \| unattributed`；
- coverage：`complete \| partial \| unavailable`；
- freshness：由 `observed_at/fresh_until` 动态计算为 `current \| stale \| expired`；
- confidence：仅用于 derived/estimated，范围 `0..1`，exact 不靠高置信度伪装。

`unknown` 必须携带 `missing_fields` 或稳定 reason code。`unattributed` 表示已有成本但不能可靠映射来源，必须单列，不能平均分配。

### 6.7 规范化公式 v1

```text
effective_multiplier = group_multiplier / recharge_yield
effective_unit_cost = published_unit_price × effective_multiplier
```

前提是三者使用可比较的货币、单位和有效期；`recharge_yield <= 0` 或任一输入缺失时结果为 unknown。FX、充值手续费、固定成本分摊、重试成本必须作为独立且带版本的 evidence 输入；未实现时不得隐含进公式。Core 是公式唯一实现方，Connector 上报原始规范化输入，前端只展示 Core 结果和 `formula_version`。

### 6.8 `UpstreamChangeEvent`

事件类型：`balance_low`, `balance_recovered`, `group_added`, `group_changed`, `group_removed`, `model_added`, `price_increased`, `price_decreased`, `model_removed`, `source_stale`, `source_recovered`。字段包含 before/after 的 observation ID、变化绝对值/百分比、首次发现时间、确认时间、严重度和影响范围。

删除检测必须满足全部不变量：

1. 只有 `coverage=complete` 的成功快照可以产生 absence；
2. 连续两个完整快照均缺失才确认 removed；
3. partial/failed run 不增加 absence 计数；
4. 分页游标循环、页数/响应体超限一律降为 partial/failed；
5. 同一 `run_id + observation identity` 和 `snapshot_hash` 重放不产生重复事件；
6. 失败时保留最近成功快照，只推进 stale/expired 状态。

### 6.9 Core 表与关键约束

| 表                              | v1 关键键/索引                                                                              | 写入语义                                  |
| ------------------------------- | ------------------------------------------------------------------------------------------- | ----------------------------------------- |
| `upstream_intelligence_sources` | unique `(user_id, connector_id, local_ref)`；index `(user_id,status)`                       | Connector registration upsert；不物理删除 |
| `upstream_collection_runs`      | unique `(user_id,source_id,id)`；index `(source_id,observed_at desc)`                       | run 状态单向推进                          |
| `upstream_ingest_batches`       | unique `(run_id,batch_no)`                                                                  | payload hash 相同可重放，不同则 conflict  |
| `upstream_wallet_observations`  | unique `(run_id,id)`；index `(source_id,observed_at desc)`                                  | append-only                               |
| `upstream_offer_observations`   | unique `(run_id,group_key,model_key,price_dimension)`；index current/history comparison key | append-only                               |
| `upstream_snapshot_absences`    | unique `(source_id,comparison_key)`                                                         | 仅完整快照递增/清零                       |
| `upstream_change_events`        | unique `(source_id,event_fingerprint)`；index `(user_id,confirmed_at desc)`                 | append-only、幂等                         |
| `upstream_intelligence_links`   | partial unique active target                                                                | 显式管理员映射、可停用                    |

run finalization 必须在一个数据库事务内验证 batch manifest、写 facts、更新 source current pointer、推进 absence、生成 change events；任一步失败不得留下“半个完整快照”。current 由可重建的 pointer/view 得出，不另存一份会漂移的价格值。

### 6.10 保留策略

- collection runs 与原始规范化 observations：默认 90 天；
- 变化事件、日级价格区间和成本账本：至少 400 天；
- current snapshot：永久保留至来源删除后的 90 天；
- 原始 HTTP body：不上传 Core；Connector 默认不落盘，调试也只允许结构化脱敏摘要；
- 清理 worker 必须按 owner 分批、可重试，并保留仍被 change/recommendation/decision 引用的证据。

## 7. Connector 实施设计

### 7.1 本地多来源存储和 UI

现有 `gateway-config.json` 只描述一个托管网关，不扩成混合结构。新增独立 `upstream-intelligence-sources.json` 和原子写 store：

- `owned` 仅引用当前 managed gateway 配置，不复制凭证；
- `external` 保存 provider、display name、URL、凭证、币种、轮询间隔和启用状态；
- 公共 DTO 只返回 `has_credentials` 等布尔字段，绝不回显凭证；
- 更新时采用 patch + explicit clear，保存前连接测试；
- 限制每 Connector 来源数、URL 长度、响应体、分页数、模型数和并发数；
- 本地 API 仍受 loopback/private-peer guard 和 `X-E2M-Local-Token` 保护。

建议本地路由：

```text
GET    /api/local/upstream-intelligence/sources
POST   /api/local/upstream-intelligence/sources
PATCH  /api/local/upstream-intelligence/sources/{local_ref}
DELETE /api/local/upstream-intelligence/sources/{local_ref}
POST   /api/local/upstream-intelligence/sources/{local_ref}/test
POST   /api/local/upstream-intelligence/sources/{local_ref}/collect
GET    /api/local/upstream-intelligence/runs
```

MVP 不提供物理删除：本地“移除”只会暂停采集、撤销本地凭证并上传 tombstone；已生成的 outbox 仍须完成上传或明确作废。Core 将对应 source 标为 disconnected，不级联删除历史事实。未来若提供不可恢复删除，必须单列审批和 retention 设计。

### 7.2 Sub2API collector

新增独立只读 intelligence adapter，不能复用会执行生命周期写入的接口。Collector 顺序采集版本/能力、钱包、分组/倍率、模型价格，统一完成后才判定 coverage。每个 endpoint 设独立 timeout 和大小上限；认证失败不重试，429/5xx/网络错误按带 jitter 的指数退避。

兼容性使用 fixture 驱动：支持已知 schema 映射，未知字段忽略，已知字段类型变化则返回 `schema_unsupported` 而不是猜值。Adapter 输出严格 DTO，任何 host、header、cookie、credential 或 raw payload 字段在序列化前即不存在。

### 7.3 本地调度、快照和 outbox

- 每个来源独立调度，带随机抖动，单站失败不阻塞其他来源；
- 同一来源只允许一个进行中的 run，手动刷新与定时任务合并；
- 先原子持久化 run/snapshot/outbox，再尝试上传；
- outbox 以 `run_id + batch_no` 幂等重放，成功确认后再清理；
- Core 离线期间继续保留最近成功快照和有界 outbox；达到容量上限时优先保留最新完整快照和未确认变化，不静默丢弃；
- 日志只记录 source opaque ref、run ID、状态、计数、错误码和耗时。

建议涉及文件：

```text
app/e2m-agent/internal/intelligence/
app/e2m-agent/internal/adapters/sub2api/intelligence.go
app/e2m-agent/internal/connector/intelligence_store.go
app/e2m-agent/internal/connector/intelligence_local_api.go
app/e2m-agent/internal/connector/localui_assets.go
app/e2m-agent/cmd/e2m-agent/main.go
```

## 8. 协议和 API

### 8.1 新 capability 与 typed task

在封闭列表增加 L0 `upstream.intelligence.collect`。Connector 只有在本地至少存在一个启用来源且版本支持时才声明 capability。

当前 Connector enrollment/lease 协议版本为 v3。`upstream.intelligence.collect` 仍通过 optional capability 和 task `schema_version=1` 演进；protocol major 的提升来自 route-plan fenced 远端写所需的 durable execution permit，而不是情报采集字段本身。

滚动兼容边界是 fail-closed 的：

- migration 0074 保留存量 protocol-v2 Connector 的 token 和版本，Core 可认证/读取该身份但不得向它 lease 或授予 execution permit；v2 lease 请求返回 426 且零任务，direct Store lease/execute 同样拒绝；
- 真实 v3 Connector 使用 protocol-3 runtime state 和合法版本完成 `RecordConnectorSeen` handshake，原子持久化升级 Connector/runtime protocol 后，后续 lease 才可成功；读取时不得把存量 v2 的 nested runtime 伪投影为 v3；
- 旧 Connector + 新 Core 必须停留在“可认证、不可取任务”，不能以 lease expiry 推断其旧写入安全；新 Connector + 旧 Core 遇到不支持的 protocol/route 必须停止写入；
- 滚动升级顺序为先停用新 auto-apply、完成 migration 0074 preflight、升级 Core/schema，再启动真实 v3 Connector 并确认 handshake，最后才恢复自动优化。preflight 必须先确认所有 v2 进程已停止，再对每个 leased fenced mutation 完成权威远端对账；只有已确认的远端结果才能按事实终态化，只有确认未应用且当前仍需要的 intent 才能回到 pending，结果不确定必须中止迁移。兼容矩阵必须覆盖 v2 read/auth-but-no-lease、v3 handshake upgrade、mixed-version 拒绝和回退拒绝。

```json
// task input
{
  "schema_version": 1,
  "source_id": "uisrc_opaque",
  "reason": "manual_refresh"
}

// task result: summary only
{
  "run_id": "uirun_opaque",
  "status": "succeeded",
  "coverage": "complete",
  "fact_count": 42,
  "observed_at": "2026-07-24T08:00:00Z"
}
```

输入不得包含 local ref、URL、provider endpoint 或凭证。错误沿用 allowlist 机制，新增 `source_not_found`, `source_paused`, `auth_failed`, `rate_limited`, `schema_unsupported`, `response_too_large`, `upstream_unavailable`, `local_store_failed`。

### 8.2 Connector ingest

```text
POST /api/v1/connectors/upstream-intelligence/snapshots
```

请求由 Connector bearer token 认证。Core 从 token 推导 `connector_id/user_id/instance_id`，拒绝请求自行声明其他 owner。单批有条数和字节上限，包含 registration summary、run、source-level wallet facts 和 offer observations；响应返回 accepted/duplicate/rejected counts 和稳定错误码。首批携带 `batch_count` 和 `manifest_hash`，每批携带 `batch_no/payload_hash`；只有 `0..batch_count-1` 全部到齐且 manifest 校验成功后，Core 才在单事务内 finalise run 并执行 change detection。重复批 payload hash 相同返回 duplicate，不同返回 409；乱序允许，finalization 幂等。

必须测试：跨 owner 注入、伪造 connector/source、批内重复、跨批重放、乱序 final、过大 body、未来时间、非法 decimal、未知字段、raw response 字段和 secret-like value 拒绝。

### 8.3 管理员读/动作 API

```text
GET  /api/v1/upstream-intelligence/overview
GET  /api/v1/upstream-intelligence/sources
GET  /api/v1/upstream-intelligence/sources/{id}
GET  /api/v1/upstream-intelligence/rates
GET  /api/v1/upstream-intelligence/changes
GET  /api/v1/upstream-intelligence/frontier
GET  /api/v1/upstream-intelligence/recommendations
GET  /api/v1/upstream-intelligence/evidence/{id}
GET  /api/v1/upstream-intelligence/links
POST /api/v1/upstream-intelligence/links
PUT  /api/v1/upstream-intelligence/links/{id}
POST /api/v1/upstream-intelligence/sources/{id}/refresh
```

所有列表提供有界分页和显式过滤器。`overview` 返回首屏所需的一致聚合；其余下钻响应返回同一类 `fact_version`，客户端在版本不一致时重新拉取而不是混用。`fact_version` 是 owner 情报数据每次成功 finalization 后递增的单调版本（或等价一致性 token），不是时间字符串。refresh 仅在 capability 在线时创建 L0 task，并做单来源频率限制与审计；不支持时返回可解释的 409，不回退为 Core 直连。

建议涉及文件：

```text
packages/e2m-contracts/upstream_intelligence.go
packages/e2m-contracts/connector.go
packages/e2m-contracts/connector_validation.go
app/e2m-core/internal/httpapi/upstream_intelligence.go
app/e2m-core/internal/store/upstream_intelligence_store.go
app/e2m-core/internal/store/memory_upstream_intelligence.go
app/e2m-core/internal/store/postgres_upstream_intelligence.go
app/e2m-core/internal/store/migrations/0058_upstream_intelligence.*.sql
```

迁移编号以实施时仓库最新版本为准；`0058` 只是当前基线后的建议占位，开发前必须重新检查，禁止抢占并行迁移号。

## 9. 看板产品方案

新增管理员一级页面 `/upstream-intelligence`，菜单名“上游情报”，放在“上游资源与交付”分组首位。不要继续扩大接近 2000 行、同时承担编排写操作的 `Upstream.tsx`。

### 9.1 页面结构

MVP 四个 Tab：总览、价格与倍率、变化记录、机会提示。阶段 4 增加“成本账本与毛利护栏”；阶段 5 才把机会提示升级为带生命周期、shadow 和 dry-run 的正式建议。

首屏从上到下：

1. 数据状态条：生成时间、下一采集时间、失败/过期来源；
2. 来源、模型、分组、provider、币种、窗口和证据过滤器；
3. 新鲜价格覆盖率、余额风险来源数、24h 变化数、可比较来源数、待处理建议数；
4. 来源钱包、有效倍率/价格榜、最高优先级机会提示；
5. 成本—质量 Pareto；
6. 24h/7d 价格变化时间线。

URL 保存 `tab/source_id/model/group/provider/currency/window/evidence/recommendation_id`，使下钻可分享和返回。所有金额行可打开 Evidence Drawer 查看原始规范化字段、公式、观测时间、覆盖度、缺失字段及相关 run；不展示 raw HTTP。

### 9.2 状态规则

- loading 使用 Skeleton，不能先显示 0；
- empty 解释如何在 Connector 本地添加来源；
- partial 保留成功来源并逐项标失败；
- stale/expired 保留最近成功值并显示时间，但不参与建议和节省计算；
- unknown/unattributed 显示“未知/未归因”，排序置底；
- 币种、模型或计价单位不一致且无可靠转换时显示“不可比较”；
- 没有可靠消耗速率时不显示预计可用天数；
- 禁止跨币种直接加总钱包余额；
- full error 展示最近成功快照、错误状态和重试入口，不白屏。

### 9.3 前端拆分

```text
web/console/src/pages/UpstreamIntelligence.tsx
web/console/src/pages/upstreamIntelligenceLocation.ts
web/console/src/components/intelligence/EvidenceBadge.tsx
web/console/src/components/intelligence/DataQualityBanner.tsx
web/console/src/components/intelligence/SourceWalletPanel.tsx
web/console/src/components/intelligence/EffectiveRateLeaderboard.tsx
web/console/src/components/intelligence/CostQualityFrontier.tsx
web/console/src/components/intelligence/RecommendationInbox.tsx
web/console/src/components/intelligence/*Drawer.tsx
web/console/src/api/upstreamIntelligence.ts
web/console/src/api/upstreamIntelligenceHooks.ts
web/console/src/router.tsx
web/console/src/layouts/consoleMenu.ts
web/console/src/i18n/locales/{zh,en}.ts
```

`EvidenceBadge` 抽成共享组件供 Operations Center 使用。Overview 只增加余额风险、24h 变化和待处理建议三个摘要链接；Operations Center 只增加“查看成本情报”和影响摘要；客户健康页不得泄露采购价。

## 10. 阶段 4：时间化成本账本与毛利护栏

### 10.1 账本模型

新增不可变 `UpstreamCostFact`，至少包含 `source_id/channel_id/model`, token/request quantity、价格 observation ID、价格有效区间、计算版本、amount/currency、attribution 状态和 `occurred_at`。事实按事件发生时的有效价格计算；绝不能用当前倍率倒算历史。

与现有 `ChannelObservation` 的连接必须显式完成：只有能从 published binding/channel 映射到 source 且 token/unit 完整时才为 exact/derived；否则进入 estimated 或 unattributed。现有 float `EstimatedCost` 只作兼容输入，迁移后不能成为财务事实主键。

### 10.2 毛利读模型

- 采购成本、零售价/收入、毛利额和毛利率分开显示；
- exact、estimated、unknown、unattributed、expired 分栏；
- 可归因覆盖率低于 90% 时禁止声称真实毛利，只显示估算区间和缺口；
- FX、手续费、重试放大和固定成本均需独立 versioned evidence；
- 价格变更的影响按受影响模型、计划、客户和未来 24h/7d 消耗区间解释。

阶段验收：选择任意历史窗口均能按当时价格重放；同一批输入重复计算结果一致；改动当前价格不会改变已封存历史事实；未归因成本不会被摊到任意来源。

## 11. 阶段 5：建议与策略实验室

### 11.1 建议对象

`UpstreamRecommendation` 至少包含：

- `id/status/severity/created_at/expires_at`；
- `from_source/to_source/model/group`；
- `affected_plan_ids/affected_downstreams`；
- 预计节省百分比及上下界；
- success rate、TTFT、稳定性、容量余量的 before/candidate 差异；
- passed/blocked/unknown constraints；
- 完整 evidence IDs 和 formula/strategy version；
- `shadow_result`, `dry_run_id`, `decision_id`（按阶段出现）。

状态采用 `open → shadowing → ready_for_dry_run → dry_run_passed/blocked → dismissed/expired`。来源事实变化后旧建议立即过期，不允许沿用旧证据执行。

### 11.2 硬门槛

- 成本证据必须 current、complete 且非 unknown/unattributed；
- 候选必须满足不可协商的成功率、TTFT、硬故障、容量和维护状态门槛；
- 不同模型/单位/币种不可比较时阻断；
- 余额不足、认证失败、隔离、退休或证据样本不足时阻断；
- “最便宜”永远不能越过质量 floor；
- 每条建议必须解释为什么建议、预计节省、质量风险、阻断项和证据有效期。

shadow 只用实时事实模拟候选排序，不改变 desired state；dry-run 必须走现有 reconcile 规划层，输出准确 diff、fence scope、影响面和回滚方案，不创建远端写任务。

## 12. 阶段 6：安全自动闭环

不另建执行引擎。把已验证的建议转换为现有 `AutoSwitchDecision` / reconcile 生命周期，并复用：

- owner/plan/channel 作用域和候选质量门槛；
- scheduling generation fence；
- rollout operation 的 durable lease/CAS，以及 Connector task 的精确 lease → durable `executing` permit；
- 幂等键、完整执行身份校验和 generation 冲突检测；
- `OperationAudit` 和 reconcile run；
- 调用性验证、质量观察、隔离和回滚；
- 10% → 25% → 50% → 100% guarded recovery。

初始默认 `auto_apply=false`。启用自动执行必须是显式、可撤销、按 scope 的策略，且设每日动作上限、冷却时间、最小节省阈值和 kill switch。全局开关 `E2M_UPSTREAM_OPTIMIZATION_AUTO_APPLY` 和 scope policy kill switch 只关闭前向 `start` / `advance`；`list` / `get` 与 exact-baseline `rollback` 是恢复面，必须继续可用，后台 Worker 和 recovery Runner 也保持运行。

每个 route-plan fenced 远端写必须严格经过：任务与 typed fence 校验 → Connector 本地 scheduling-fence lock → 本地 write-receipt lock → `POST /api/v1/connectors/tasks/{id}/execute` → Core 在 plan → task 锁序下原子执行 `leased → executing` → 网关副作用/receipt → typed Complete。permit 响应必须与 lease 的 `user_id`、`instance_id`、`connector_id`、type、schema、input bytes、idempotency key、plan/generation 和 lease nonce 全部一致；任一漂移、409 或响应不确定都必须零网关调用、零 Complete。fenced task 只有 `executing + exact nonce + typed result` 才能完成，retryable completion 不能释放 permit。

`executing` 表示远端结果可能已经发生，永不按原 lease deadline 自动过期、回到 pending 或被另一 Worker 重试，并在终结前阻断所属 route plan 的 generation 变更和删除。网关 timeout/unreachable/invalid response 等无法证明结果的情形不得 Complete。平台管理员必须先停住相关 Connector/auto-apply并完成远端对账，再通过 session-authenticated `POST /api/v1/connector-tasks/{id}/resolve-execution` 使用精确 nonce、非敏感 evidence note 和闭集 resolution：`confirmed_applied`（必须有类型正确且与原任务一致的 result，转 `succeeded`）、`confirmed_not_applied`（转 `failed/execution_abandoned`）、`connector_revoked_unverifiable`（仅 Connector 已 revoked，转 `failed/execution_outcome_unknown`）。恢复永不回到 pending；task 终态与 L3 critical audit 同事务提交，audit 只保存 `lease_nonce_sha256=sha256:<hex>`，不得保存原始 nonce。

每一级扩大前从单一可信快照重新生成和校验：价格证据仍 current/complete、目标余额和容量充足、成功率/TTFT 不劣于门槛、无新硬故障、当前 generation 仍有效。阶段写入完成后必须等到 `ObserveUntil`，再使用观测窗口后新生成的 5 分钟质量证据评估两个参与 channel；只允许全局 intelligence/link fact version、质量约束 evidence IDs 及其派生 fingerprint/时间刷新，mapping、成本账本、offer/cost/wallet/link/binding evidence、计划、维度、节省、策略和公式必须与启动事实精确一致。任何条件失败即 hold；质量回归即按现有机制回滚并使建议/决策失效。100% 后仍需完成观察和 after evidence，未验证不得标记 complete。

回滚必须在同一原子 Store 操作中 supersede 当前 pending/running 的前向 operation、清除旧 operation lease 并提升 operation version/plan generation；旧 Worker 后续 renew/complete 因 CAS、rollout generation 或 plan generation 不匹配而失权。这里不能静默 supersede `executing` Connector task：generation bump 必须冲突并保持计划冻结，直到该 permit 正常 Complete 或按上述三态人工恢复。Worker 写回完整 baseline 后，必须读取全部账户并逐项相等才标记 rolled-back；rollback proof 使用唯一的完整权重集摘要 `weight-set-sha256:<baseline fingerprint>`。该 read-back 只证明权重恢复，rollback 的 callability/quality 保持 unknown，不能伪装成健康证据；HTTP 中 `rollback_verified=true` 只表示 rollback 专用的状态、推荐/基线/generation、fresh window 和唯一摘要不变量全部匹配，`last_after_verified` 对 StageNone/回滚始终为 false。

## 13. 可执行工作包

估算为人天（pd），包含代码、单元/契约测试和必要文档，不含等待真实上游授权的日历时间。建议团队：1 Core、1 Connector、1 前端，QA/产品共享；当前逐项合计为 49–70 pd，计划预算按 50–70 pd 管理，并另留 10% 集成缓冲。

| ID    | 阶段 | 工作包                       | 主要交付/文件                                                                                      | 依赖                   | 估算 |
| ----- | ---- | ---------------------------- | -------------------------------------------------------------------------------------------------- | ---------------------- | ---: |
| UI-00 | 1    | 冻结事实词典与安全红线       | contracts 草案、公式 v1、decimal 规则、枚举、golden JSON、威胁清单                                 | 无                     |  2–3 |
| UI-01 | 1    | Core 领域与迁移              | 新 contracts、NUMERIC 表、索引、约束、memory/PG store                                              | UI-00                  |  3–4 |
| UI-02 | 1    | Ingest 与变化检测            | authenticated batch ingest、manifest/finalize 事务、幂等、complete-only deletion、retention worker | UI-01                  |  3–4 |
| UI-03 | 2    | Connector 多来源本地存储/API | owned/external store、patch/clear、loopback UI API、redaction                                      | UI-00                  |  3–4 |
| UI-04 | 2    | Sub2API intelligence adapter | wallet/group/rate/price fixtures、bounds、schema/errors                                            | UI-03                  |  3–5 |
| UI-05 | 2    | 调度器、快照与 outbox        | independent polling、jitter/backoff、durable replay、capacity policy                               | UI-03, UI-04           |  3–4 |
| UI-06 | 2    | Capability 与手动刷新        | L0 typed task、validation、summary completion、audit                                               | UI-01, UI-05           |  2–3 |
| UI-07 | 3    | Core 一致读模型/API          | overview/source/rates/changes/evidence/frontier                                                    | UI-02                  |  3–4 |
| UI-08 | 3    | 独立看板骨架                 | route/menu/API/hooks/URL state、证据状态组件、RBAC                                                 | UI-07                  |  2–3 |
| UI-09 | 3    | 钱包/倍率/变化/证据下钻      | wallet、leaderboard、timeline、drawers、partial/stale states                                       | UI-08                  |  3–4 |
| UI-10 | 3    | 质量连接与 Pareto/MVP 验收   | 显式 source/channel mapping、quality join、Pareto、3-source fixture E2E                            | UI-07, UI-09           |  3–4 |
| UI-11 | 4    | 时间化成本账本               | effective intervals、usage attribution、replay、retention                                          | UI-02, UI-10           |  4–6 |
| UI-12 | 4    | 毛利护栏与页面               | coverage gate、margin ranges、impact views、unknown/unattributed                                   | UI-11                  |  2–3 |
| UI-13 | 5    | 建议引擎                     | constraints、savings interval、expiry/fingerprint、evidence links                                  | UI-10, UI-12           |  3–4 |
| UI-14 | 5    | shadow / dry-run 实验室      | shadow evaluator、reconcile dry-run bridge、UI explanation                                         | UI-13                  |  3–4 |
| UI-15 | 6    | 决策桥和授权策略             | map to AutoSwitchDecision、scope opt-in、limits、kill switch                                       | UI-14                  |  2–3 |
| UI-16 | 6    | 灰度执行与验证闭环           | fence、10/25/50/100 gates、after evidence、rollback tests                                          | UI-15                  |  3–4 |
| UI-17 | 全程 | 安全/性能/运维加固           | secret scans、load test、metrics/alerts、runbook、failure drills                                   | UI-10 起；UI-16 后终验 |  2–4 |

截至 2026-07-26 的真实进度：

| 工作包    | 状态     | 已验证/剩余                                                                                                 |
| --------- | -------- | ----------------------------------------------------------------------------------------------------------- |
| UI-00..06 | 完成     | Contracts、Core、Connector 默认 Go 测试集与 vet 通过；历史阶段未执行的 PostgreSQL/race 已在 UI-17 disposable PG 与 Linux 容器门禁补齐，真实上游授权环境 E2E 仍待执行 |
| UI-07     | 完成     | DTO、一致读 Store、retention、七个 admin-only API、HMAC cursor v2、owner/finalized/removal/deep-copy 与敏感字段测试通过 |
| UI-08..09 | 首版完成 | 管理员 route/menu、URL state、钱包/倍率/变化/证据/blocked frontier 页面已通过 Web build/lint/test           |
| UI-10     | 完成，阶段 3 已签收 | 显式 mapping、管理员 Link API、5m 质量 join、精确 decimal Pareto 与多来源 HTTP 集成验收通过；PostgreSQL、100 来源/5,000 facts、现行 schema-4 两 external + 一 owned release，以及中英文、等价表格、移动布局、键盘/焦点/XSS 均已终验 |
| UI-11..12 | 完成 | 时间化成本事实、归因 worker、历史重放、coverage gate、毛利区间与 unknown/unattributed/expired 护栏已完成定向回归；相关 Store migration/实库路径随 disposable PostgreSQL 全包验证 |
| UI-13..15 | 完成 | 确定性建议、过期/fingerprint、shadow/dry-run、无远端写副作用、scope policy/授权/kill switch 和决策协调完成定向回归，并由现行三来源 release 提供情报输入实证 |
| UI-16     | 完成 | 完整 baseline、无关权重保留、10/25/50/100、阶段后质量证据刷新、operation lease/CAS、route-plan generation fence、Connector v3 `leased → executing` permit、崩溃恢复、rollback 原子抢占和全量 read-back proof 已通过 schema-6 disposable NewAPI 正式 release；`executing` 与 generation bump 冲突且只能 Complete/三态人工恢复 |
| UI-17     | 正式签收 | 冻结候选的 contracts/Agent/Core fresh 全包测试与 vet、fresh migration 74 PostgreSQL 全 Store和实库性能、固定 Linux race、13 条 Prometheus rules、failure drills、漏洞/secret 边界、58/58 files 与 201/201 console tests、双语/等价表格/移动/键盘/XSS、schema-4 三来源 Intelligence 与 schema-6 NewAPI 正式 release 全部通过；两项 release 共享 `ee8eba…` build-input，清理/环境恢复/受保护栈不变均通过。签收不等于生产部署或公开发布 |

### 13.1 关键路径和并行关系

```text
UI-00 → UI-01 → UI-02 → UI-07 → UI-08/09 → UI-10 (Intelligence MVP)
           └──────────────┐
UI-00 → UI-03 → UI-04 → UI-05 → UI-06 ┘

UI-10 → UI-11 → UI-12 → UI-13 → UI-14 → UI-15 → UI-16
```

UI-01/02 与 UI-03/04/05 可由 Core 和 Connector 并行；UI-08 可在 UI-07 契约冻结后用 fixtures 并行。不要为了“前后端并行”让前端自行推导公式。

### 13.2 建议日历排期

假设 3 名主力工程师并行、QA/产品共享：

| 周期        | 目标                          | 退出门槛                                      |
| ----------- | ----------------------------- | --------------------------------------------- |
| 第 1 周     | UI-00、UI-01 起步、UI-03 起步 | 词典/公式/安全红线和 golden fixtures 评审通过 |
| 第 2–4 周   | UI-01..07、UI-04..06          | 三来源 10 分钟出数据，失败隔离，变化两轮发现  |
| 第 5–6 周   | UI-08..10                     | Intelligence MVP 签收；看板体验超过竞品       |
| 第 7–9 周   | UI-11..12                     | 历史成本可重放，覆盖率护栏生效                |
| 第 10–11 周 | UI-13..14                     | 建议可解释、可过期，shadow/dry-run 无副作用   |
| 第 12–14 周 | UI-15..17                     | 分段执行、验证、回滚和故障演练通过            |

若只有 1 名全栈工程师，应按 50–70 个有效开发日串行估算并另加集成缓冲，不应承诺 14 个自然周内全部完成。

## 14. 分阶段 Definition of Done

### 阶段 1

- 所有字段有单位、nullable 语义、owner scope 和有效时间；
- exact/derived/estimated/unknown/unattributed、complete/partial/unavailable、freshness 有 golden tests；
- decimal round-trip 不丢精度；
- source/channel 映射只有显式验证后才可参与质量/成本连接；
- 架构测试证明 DTO 不包含 URL/credential/raw response；
- migration up/down、MemoryStore 和 PostgreSQL 行为一致。

### 阶段 2

- 两外部 + 一 owned 来源首次证据 ≤ 10 分钟；
- auth/429/timeout/schema drift/partial pagination/oversize fixtures 全覆盖；
- 本地凭证不回显，文件权限测试通过；
- outbox 在 Core 离线和重启后幂等补传；
- batch 缺失、乱序、重复和 hash 冲突不会 finalise 半个 run；
- 未声明 capability 的 Connector 永不收到 collect task。

### 阶段 3（Intelligence MVP）

- 钱包、倍率/价格、变化和 Pareto 数字均可下钻到 evidence；
- partial/stale/unknown/unattributed/不可比较均有组件测试；
- 两轮删除确认且 partial pagination 零误删；
- admin 可见，client/supplier API 返回 403；
- 100 来源、5,000 offer facts 下首屏 P95 < 2s、过滤响应 < 300ms；
- 中英文、键盘访问、图表等价表格和移动布局通过。

### 阶段 4

- 任意历史点按当时价格重放；修改当前价格不改变历史结果；
- 可归因覆盖率 < 90% 时毛利声明被阻断；
- unknown/unattributed/expired 不进入精确毛利；
- 跨币种无 FX evidence 时不聚合。

### 阶段 5

- 每条建议包含收益区间、质量/容量约束、阻断项、证据和过期时间；
- 事实变化使旧建议失效；
- shadow 和 dry-run 不创建任何远端写 task；
- stale/partial/unknown 成本或质量 floor 不满足时无法进入 executable 状态。

### 阶段 6

- auto-apply 默认关闭，scope opt-in 和 kill switch 有集成测试；关闭前向执行时 list/get/rollback 与恢复 Worker/Runner 仍可用；
- 启动捕获全部 managed-binding baseline；目标可以已有权重、无关账户允许非零并原样保留；10/25/50/100 只迁移来源原始 baseline；
- 每级灰度都有 generation fence、operation lease/CAS、audit、before/after evidence，并在 `ObserveUntil` 后刷新质量证据；每个 fenced Connector write 在网关副作用前获得 durable `executing` permit；
- 10/25/50/100 任一级质量回归均能自动停止/回滚；
- rollback 原子 supersede pending/running 前向 operation，旧 operation lease/generation 无法再写；若同 plan 已有 `executing` Connector task，则 generation bump 必须冲突，禁止静默 supersede，待正常 Complete 或三态人工恢复后再继续；完整 baseline 全量 read-back 相等后才标记成功；
- `executing` 不自动超时/重试；不确定网关结果保持冻结，人工恢复只允许 `confirmed_applied`、`confirmed_not_applied`、`connector_revoked_unverifiable`，并原子写入无原始 nonce 的 L3 critical audit；
- 重复执行、worker 崩溃、迟到 task、Connector 离线和并发决策演练无重复副作用；
- 100% 后只有完成观察和调用性验证才标记成功；rollback read-back 的健康门状态保持 unknown，不把权重相等伪装为健康通过。

## 15. 测试矩阵

### 15.1 单元与契约

- decimal、倍率公式、单位换算、freshness 边界和置信度；
- snapshot canonicalization/hash、幂等、连续 absence；
- pagination cursor 循环、重复页、空页、字段缺失/改名；
- RBAC、owner 派生、跨 owner/source/connector 注入；
- API nullable、分页、时间边界、统一 `generated_at/fact_version`；
- URL 状态、unknown 排序、币种不可比较、证据组合标签。
- protocol v3 permit 顺序、完整不可变身份比较、409/漂移零网关、typed receipt、retryable/不确定结果不释放 `executing`；
- protocol-v2 token 可认证读取但 lease/execute 拒绝，真实 v3 handshake 后原子升级；三态 resolve 的 RBAC、strict JSON、typed result、revoked-only、audit 原子性和 nonce hash。

### 15.2 集成与 E2E

建立 `testdata/upstream-intelligence/sub2api/` 固定夹具和可变 mock：

1. 两个 external + 一个 owned 首次采集；
2. 余额下降/恢复、倍率涨跌、新增/删除模型；
3. partial page 后下一次 complete，确认不误删；
4. 单站认证失败、限流、超时、恢复；
5. Core 离线、Connector 重启、outbox 补传；
6. 建议生成、事实变化后过期、dry-run；
7. 灰度扩大、质量回归、回滚和 after verification。

### 15.3 安全与性能

- 对 Core 数据库、HTTP fixtures、日志和浏览器 payload 做 secret-like/URL 扫描；
- 验证 DOM 注入、超大响应、压缩炸弹、慢响应和恶意 decimal；
- 100 来源/5,000 当前价格/400 天 change rollup 的读写压测；
- 批量 ingest 限流、每 owner 配额、retention worker 和索引计划检查；
- race test 覆盖同源 refresh、outbox replay、decision/rollout 并发。

每个工作包合并前至少通过相关 Go tests、web tests、lint、format-check 和 build；阶段验收再运行完整 `make ci` 与真实/fixture E2E。

## 16. 上线与回退策略

尽管本计划不安排公开发布，功能仍应以内部 feature flags 分层接通：

1. `upstream_intelligence_ingest`：只接收和存储，不展示；
2. `upstream_intelligence_dashboard`：管理员只读；
3. `upstream_cost_ledger`：账本和护栏；
4. `upstream_recommendations`：仅建议/shadow；
5. `E2M_UPSTREAM_OPTIMIZATION_AUTO_APPLY`：默认关闭，按 scope 显式启用；只控制 rollout `start` / `advance`，不关闭 `list` / `get` / `rollback`；人工 `resolve-execution` 是独立的 platform-admin 恢复面。

回退顺序是先关闭 auto-apply，再关闭建议生成，再关闭 UI；ingest 可继续保留以避免证据断档。schema 采用 additive migration；protocol-v2 Connector 不是“自然降级”执行器，只能保留 token/身份用于认证和真实 v3 handshake，不能 lease/execute。0074 down 在存在任何 `executing` task 或 protocol-v3 Connector row 时必须失败；先停机、远端对账、Complete/三态人工恢复所有 `executing` task，再按批准流程移除/重新 enrollment v3 身份，禁止把 v3 行直接改标为 v2。任何回退都不删除历史 evidence、change、decision 或 audit。

推荐灰度运行参数的安全默认值为：`E2M_RECOMMENDATION_ROLLOUT_OBSERVATION=5m`、`E2M_RECOMMENDATION_ROLLOUT_WORKER_INTERVAL=1s`、`E2M_RECOMMENDATION_ROLLOUT_WORKER_LEASE=2m`、`E2M_RECOMMENDATION_ROLLOUT_RUNNER_INTERVAL=1s`。生产环境通常保留默认值；缩短观测窗口或 lease 只能用于受控测试，不能将测试参数作为发布配置。

## 17. 观测与运行手册

至少暴露以下不含敏感标签的指标：

- active/stale/failed sources；
- run duration、coverage、facts/run、last-success age；
- ingest accepted/duplicate/rejected；
- outbox depth/oldest age；
- change events/type；
- fresh comparable price coverage；
- recommendation open/blocked/expired；
- dry-run/apply/rollback counts 和各灰度阶段停留时间。

告警优先级：凭证泄漏检测或跨 owner 拒绝异常为最高；其次是全局 ingest 失败、outbox oldest age 超阈值、误删不变量失败；单来源失败只产生来源级提醒，不升级为全局故障。

## 18. 开工顺序与第一批任务

第一批只开启 UI-00、UI-01 和 UI-03，完成以下产物后再让 adapter、API 和页面全面并行：

1. `upstream_intelligence.go` 的字段/枚举草案；
2. formula v1 与 10 组 decimal golden fixtures；
3. sanitized ingest 示例和“禁止字段”测试；
4. owned/external 本地配置 JSON schema；
5. 三来源验收夹具清单；
6. API/read-model 页面线框评审。

首次评审的 go/no-go 条件：信任边界无倒退、金额不用 float、证据维度正交、partial 不产生删除、旧 Connector 可安全降级。任何一项未满足，不进入 UI-04 真实上游适配。

## 19. 竞争领先检查

每个里程碑都用四层能力检查，而不是只数页面：

| 层级 | 必须回答的问题                   | E2M 的领先点                                         |
| ---- | -------------------------------- | ---------------------------------------------------- |
| 看见 | 钱还够多久、哪里变价？           | 多来源、证据新鲜度、完整度、历史变化                 |
| 相信 | 这个数从哪里来、能否比较？       | 精确单位/币种/公式、unknown honesty、可下钻 evidence |
| 决策 | 该不该换、能省多少、风险是什么？ | 成本—质量—容量联合约束、收益区间、shadow/dry-run     |
| 执行 | 如何安全生效、失败怎么办？       | typed task、generation fence、审计、灰度、验证、回滚 |

只有四层全部贯通，才算“全面超越”；单独复制一个漂亮倍率榜不算完成目标。
