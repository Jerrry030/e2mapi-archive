# E2M 排期计划表（旁路插件版 MVP）

> 历史排期，已停止执行。2026-07-31 的当前边界与下一步验收以
> [current-state.md](current-state.md) 和
> [platform-boundaries.md](platform-boundaries.md) 为准；本文不得用于恢复供给、比例、
> 计费或 Connector 密钥下发功能。

更新：2026-07-02
对应架构：[e2m-sidecar-architecture.md](../architecture/e2m-sidecar-architecture.md) · 决策：[ADR-0003](../decisions/0003-sidecar-plugin-repositioning.md)

> 历史排期：本文记录 2026-07-02 的早期设想，不再代表当前实现。项目已改为每个
> Instance 绑定一个出站 Connector；Core 不直连网关，也不接收网关地址、认证方式或
> 管理凭证。当前事实以 [current-state.md](current-state.md) 和
> [current-closed-loop-and-data-source.md](current-closed-loop-and-data-source.md) 为准。

---

## 0. 总览与目标

**目标：6 周内对一个真实站长收到托管费。** 关键里程碑在**第 4 周**——届时能
「配号 + 体检 + 换号 + 告警 + 出账单」端到端跑通，且**完全不依赖上游侧到位**
（托管费本身是独立价值，砍断双边冷启动的鸡生蛋）。

**团队假设**：1-2 名 Go 工程师，前端由同一人兼顾（antd Pro 省界面代码）。
工作量单位为**人天（pd）**，按 1 人全职估算；2 人可并行压缩日历周。

**原接入策略（已废弃）**：本文当时计划云端直连先行、Connector 后置；当前实现已完全
反转为 per-instance outbound Connector，且不保留云直连兼容路径。

**先做 sub2api**：管理 API 最成熟、有 S2A-Manager 端点序列可参考、且能
`/admin/accounts/sync/crs` 吸站长存量号池（获客钩子）。new-api / CPA 后置到第 5-6 周。

---

## 1. 六周计划表

| 周 | 主题 | 关键交付物 | 工作量 | 依赖 | 验收标准 |
|:--:|---|---|:--:|---|---|
| **W1** | 地基转生产 | PG + 迁移 + sqlc 骨架、账号隔离 + 用户能力 RBAC、密管接入 | **5 pd** | — | 内存 store 换 PG；能注册账号/实例；明文凭证进 Infisical、库里只存 ref |
| **W2** | sub2api 适配器 + 配号闭环 | `AdminClient` 接口 + 云端直连实现、sub2api 适配器（读全+关键写）、一键配号 | **6 pd** | W1 | 平台经 admin API 向真实 sub2api 批量建号绑代理成功；ReadOnlyStub 被真实调用替换 |
| **W3** | 体检器 + 自动换号 + 告警 | River 定时体检、决策引擎（L1 自动/L2 审批）、飞书 webhook + QQ 副通道 | **6 pd** | W2 | 探测到号连续 429→自动降优先级+启用备用号+飞书通知；掉线降级到飞书生效 |
| **W4** ⭐ | 计量结算 + 首个付费站长 | 三维计量聚合、账单视图、前端控制台 v1（实例列表/号池健康/审计） | **6 pd** | W3 | 对 1 个真实站长出账单并收费；控制台能看健康看板和审计日志 |
| **W5** | 横向扩 new-api + 上游供给侧 | new-api 适配器（复用同接口）、SupplyOffer 供给登记 + 供给台账 | **5 pd** | W4 | new-api 实例可配号/体检；上游能按规范上传账号并生成台账；证明适配层可复制 |
| **W6** | CPA + 审批闭环 + 加固上线 | CPA 适配器、飞书卡片 L2/L3 审批、契约测试+版本探测、密钥轮换 | **6 pd** | W5 | 三网关全覆盖；高危动作走审批；契约测试挡版本漂移；正式对外接付费站长 |

**合计约 34 pd**（≈ 7 周单人日历，2 人并行可压到 5-6 周日历）。

---

## 2. 逐周任务拆解与工作量

### W1 · 地基转生产（5 pd）
- PostgreSQL + golang-migrate 迁移骨架，内存 store 换 sqlc 实现 — 2 pd
- Store 抽窄接口 + `(ctx, ...) (T, error)` 签名（骨架 P0 问题）— 1 pd
- 账号隔离 + 用户能力 RBAC（平台管理员 / 托管能力 / 供给能力）— 1 pd
- 接 Infisical/OpenBao，credential_ref 解引用薄封装 — 1 pd

### W2 · sub2api 适配器 + 配号闭环（6 pd）
- `AdminClient` 接口 + 云端直连实现（Phase 1）— 1 pd
- sub2api 适配器：读（accounts/usage/status）+ 写（建号/bulk-update/test）— 3 pd
- 一键配号流程（上游账号→批量注入实例，绑 proxy/分组）— 1.5 pd
- 契约测试 + `GET /admin/system/version` 版本探测 — 0.5 pd

### W3 · 体检器 + 自动换号 + 告警（6 pd）
- River 定时任务：每 60s 拉各实例账号状态入库 — 1.5 pd
- 决策引擎：健康判定 + RiskLevel 分级（L1 自动 / L2 审批）— 2 pd
- 自动换号动作（bulk-update 降优先级 + 启用备用号）+ 审计 — 1 pd
- 通知器：飞书 webhook（签名/聚合/降频）+ QQ NapCat 副通道 + 掉线降级 — 1.5 pd

### W4 · 计量结算 + 首个付费站长（6 pd）
- 三维计量聚合（实例数 + 处置次数 + 用量）入 MeteringEvent — 2 pd
- 账单视图（含 quota→货币 ratio 换算校对）— 1 pd
- 前端控制台 v1：ProLayout + 实例列表 + 号池健康看板 + 审计日志（antd Pro）— 2.5 pd
- OpenAPI spec + 前端 TS 客户端生成打通 — 0.5 pd

### W5 · new-api + 上游供给侧（5 pd）
- new-api 适配器（/api/channel、/api/log/stat，Bearer+New-Api-User 认证）— 2.5 pd
- SupplyOffer 供给登记流程 + supply_ledger 台账 — 1.5 pd
- 上游控制台页面（上传账号 / 看供给台账）— 1 pd

### W6 · CPA + 审批 + 加固（6 pd）
- CPA 适配器（/v0/management，auth-files 注入 + config 读写 + usage-queue）— 2.5 pd
- 飞书企业应用卡片回传审批（长连接，无需公网回调）L2/L3 门禁 — 2 pd
- 契约测试覆盖三网关版本探测 + 密钥轮换 + 灰度加固 — 1.5 pd

---

## 3. 关键路径与并行建议

- **关键路径**：W1(PG/接口) → W2(sub2api 适配器) → W3(体检器) → W4(计费/收钱)。
  这条链决定「何时能收第一笔钱」，优先保障，不要被 new-api/CPA 打断。
- **可并行**（2 人时）：前端控制台可从 W2 起与后端并行（先对 mock，W4 切真接口）；
  通知器（W3）与体检器解耦，可另一人并行。
- **风险缓冲**：new-api/CPA 后置到 W5-W6，若 W1-W4 超期，可先只上 sub2api 收钱，
  横向扩顺延——单网关也能验证商业模式。

---

## 4. 后置增值项排期（第 7 周起，按需触发）

| 增值项 | 触发条件 | 预估 |
|---|---|:--:|
| Phase 2 轻量连接器 | 站长明确不愿暴露管理口 | 4 pd |
| 部署托管（Komodo） | 站长要托管升级 | 6 pd |
| 中央供给网关（GPT-Load 按量分成） | 需数据面可信计量 | 5 pd |
| MaiBot 社群答疑机器人 | 站长社群成型 | 3 pd |
| Temporal 工作流 | Runbook 多步骤带补偿 | 5 pd |

---

## 5. 每周产出物检查清单（DoD）

每周结束应有：可运行的二进制/服务、对应的迁移脚本、契约测试通过、
一条能演示的端到端场景、更新的 `progress.md`。**W4 额外**：一张真实账单 + 一个
愿意付费的站长确认。

---

## 6. 风险登记（贯穿全程，非某周专属）

- **凭证落点 / 上游 ToS / 许可证** 三项须靠合同切分，法务复核安排在 W4 收钱前。
- **网关版本漂移**（CPA 周更、new-api rc 频发、sub2api 日更）：全程 pin 镜像 tag +
  契约测试 + 版本探测，绝不追 latest。
- **CPA usage-queue 60s 破坏性读**：若接 CPA 计费，收集器须高可用独占消费。
