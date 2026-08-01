# 上游结算 Connector 设计

## 1. 目标

E2M 面向下游用户销售账户余额。用户请求完成后，上游把权威用量和账单事件同步给 E2M，E2M 再从用户钱包扣款。

这个设计解决两个不同问题：

1. **最终结算**：谁在什么时间、通过哪把分配凭证、消费了多少。
2. **风险封顶**：用户余额不足、网络中断或大量并发时，上游还能放行多少消费。

单纯“响应结束后再上报”只能解决第一个问题。要限制并发透支，还必须增加请求前的短时额度授权。

## 2. 可信边界

结算 Connector 与现有部署在用户侧的 Connector 不是同一种身份：

- 用户侧 Connector 负责网关配置、健康观测和任务执行，不能作为扣款依据。
- 上游结算 Connector 代表 `source_id`，运行在真正经过请求并产生上游账务事实的一侧。
- Core 只保存结算方公钥和稳定的计量主体映射，不接触提示词、响应正文或原始上游 Key。

按量余额池只接入以下来源：

| 等级 | 来源能力 | 结算方式 |
|---|---|---|
| A | 来源自有计量入口经过每个请求，可同事务写账单与 outbox，并执行本地额度限制 | 可进入实时余额消费，MVP 首选 |
| B | 请求不经过来源入口，但模型厂商提供按独占 Key 的权威 usage/invoice API | 延迟结算，需要更高保证金，无法承诺实时停用 |
| C | 只有用户侧日志，或 OAuth 订阅没有权威逐请求账 | 只能收固定托管费，不能作为可信按量扣款来源 |

下发给用户的凭证必须只能经过 A 类计量入口。若用户拿到可绕过 Connector、直接调用模型厂商的原始 Key，则平台无法确保账单完整，该来源不能进入按量余额池。

## 3. 推荐业务流程

### 3.1 充值

支付成功后，钱包追加一条不可变的 `recharge_credit` 流水。余额是钱包流水的事务性结果，不能只修改一个孤立的 balance 数字。

### 3.2 额度租约

Core 从用户可用余额中原子预留一小笔额度，签发短时 `credit_lease`：

- `lease_id`
- `user_id`
- `source_id`
- `metering_subject_id`
- `currency`
- `max_amount_micros`
- `expires_at`

同一笔余额被多个上游使用时，每个租约都必须先在 Core 预留，避免不同来源重复消费同一余额。

### 3.3 上游本地授权

上游在请求进入模型厂商前，根据输入 token、模型价格和 `max_tokens` 估算最坏成本，并从本地租约原子预占：

- 额度不足时直接返回 402/429，不再请求 Core。
- 流式请求按最大输出预占，完成后释放差额。
- 上游与 Core 断网时，只能继续使用未消费的租约额度，不能自行扩大发放额度。

Core 因此仍不进入逐请求数据面。

### 3.4 响应完成与账单落库

上游业务事务同时写入：

1. 权威 `billing_record`；
2. 待投递的 `settlement_outbox`。

只有事务提交后，Connector 才能发送结算事件。不能先发送事件再补写上游账本。

### 3.5 Core 入账

Core 验证签名、来源身份、凭证分配关系、租约和价格版本，并在一个数据库事务中完成：

1. 保存原始结算事件及 body hash；
2. 追加用户钱包 `usage_debit` 或 `usage_reversal` 流水；
3. 更新租约已结算金额；
4. 生成 Core receipt。

事务提交后才能 ACK。Connector 收到 receipt 后才推进 outbox 游标。

如果有效账单超出已授权租约，不能删除账单或悄悄拒绝：已授权部分正常结算，超出部分进入平台风险敞口/争议应收，同时冻结该来源的新租约并告警。

## 4. 结算事件

首版事件名建议为 `usage.settled.v1`，金额使用整数 micros 或固定精度十进制字符串，禁止使用浮点数。

```json
{
  "schema_version": "usage.settled.v1",
  "event_id": "01J...",
  "issuer_id": "issuer-...",
  "signing_key_id": "key-2026-07",
  "stream_epoch": "epoch-...",
  "sequence": 1024,
  "previous_event_hash": "sha256:...",

  "source_id": "source-...",
  "metering_subject_id": "subject-...",
  "key_version": 3,
  "allocation_epoch": "allocation-...",

  "billing_record_id": "bill-...",
  "upstream_request_id": "req-...",
  "started_at": "2026-07-19T05:00:00Z",
  "completed_at": "2026-07-19T05:00:02Z",
  "model_requested": "model-a",
  "model_billed": "model-a-2026-06",
  "success": true,
  "billable": true,

  "usage": {
    "input_tokens": 1000,
    "output_tokens": 200,
    "cache_read_tokens": 0,
    "cache_write_tokens": 0,
    "reasoning_tokens": 0
  },
  "supplier_amount_micros": "120000",
  "currency": "CNY",
  "supplier_tariff_version": "supplier-price-2026-07",
  "retail_tariff_version": "retail-price-2026-07",

  "lease_id": "lease-...",
  "authorization_id": "authz-...",
  "reserved_amount_micros": "150000",

  "event_type": "usage",
  "original_event_id": "",
  "reason_code": ""
}
```

事件不能包含 prompt、completion、用户邮箱、原始 Key 或管理凭证。Core 不接受正文中的 `user_id` 作为扣款归属依据，而是通过签名身份、`source_id`、`metering_subject_id` 和 allocation 记录反查用户。

## 5. 可靠性与防重

- Connector 使用持久化 outbox，至少一次投递。
- 唯一约束至少包含 `(issuer_id, event_id)` 和 `(issuer_id, billing_record_id, event_type/revision)`。
- 同 ID、同 body hash 的重试返回原 receipt，不重复扣款。
- 同 ID、不同 body hash 返回 409，并冻结 issuer 等待调查。
- 事件允许乱序接收并在租约内立即结算；Core 返回 `highest_contiguous_sequence` 和 `missing_ranges`。
- 持续存在序号缺口时停止续发新租约，但不能因为缺一条而拒绝所有后续有效账单。
- 每日发送签名 manifest，包含序号范围、事件数、按主体/模型/币种汇总金额和 Merkle root，用于发现源头完全漏生成事件的情况。

## 6. 认证与签名

- 全程 TLS；生产环境叠加 mTLS。
- 事件使用 Ed25519 签名，Connector 私钥不离开上游，Core 只保存公钥。
- 签名覆盖 domain、HTTP method/path、issuer、key id、epoch、sequence、signed_at 和 body SHA-256。
- 支持签名密钥轮换；`stream_epoch` 只有在受控重建时变化，sequence 不能静默归零。

## 7. 对账

平台需要三层对账：

1. **传输账**：上游 outbox 序号与 Core receipt 对比，找缺口、重复和迟到。
2. **资金账**：Core 接受事件与 adjustment/reversal 的合计，对比上游权威账本、日结单和月发票。
3. **影子账**：用户侧健康观测只用于异常检测，例如上游收费请求明显多于用户网关观测；不能直接拿影子数据扣款。

只有完成资金对账的金额才能进入供应商结算。

## 8. 争议与冲正

- 历史事件不可修改。
- 上游错误通过签名的 `reversal` 或 `adjustment` 事件纠正。
- 钱包追加退款/补扣流水，保留原事件、Core receipt、租约授权、价格版本、allocation 和 manifest 证据。
- 签名只能证明“该上游声明了这笔账”，不能证明供应商永远诚实；供应商应付仍需额度上限、正式账单和合同责任约束。

## 9. MVP 范围

1. 只接一个 A 类可信上游、单币种和少量模型。
2. 每用户每来源一个专属 `metering_subject_id`。
3. 单事件 POST、Ed25519、sequence、事务 outbox、幂等 receipt。
4. 不可变钱包流水，以及 recharge、usage debit、reversal 三类资金事件。
5. 五分钟或小额 credit lease，上游按最坏成本本地预占，超额 fail-closed。
6. 每日签名 manifest 和人工对账页面。

## 10. 与智能路由的关系

四种路由预设只负责在已经通过平台安全准入的备份中排序，不直接参与钱包扣费。

当前“价格优先”使用平台维护的 `UpstreamChannel.cost_hint`。结算 Connector 上线后，应把它替换为版本化、按模型可追溯的零售价格簿；单次供应商账单金额不能直接成为下一次路由排序的价格依据。
