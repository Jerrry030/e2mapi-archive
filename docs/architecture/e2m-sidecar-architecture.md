# 简驭 E2M 架构设计书（历史旁路插件版）

日期：2026-07-02
状态：**历史设计**。当前产品已收缩为“Connector 托管站长自有号池 + E2M Core
原生承载平台上游分发”；Sub2API 仅作设计参考，不是部署组件。事实边界以
[current-state.md](../development/current-state.md) 与
[platform-boundaries.md](../development/platform-boundaries.md) 为准。

> 2026-07-31 更新：本文后续关于供给下发、中心密钥、计费和自动质量恢复的描述仅保留
> 决策历史，不得作为当前产品、API 或部署依据。

实现状态说明（2026-07-04）：本文件描述产品架构和目标技术方向。当前代码已经完成
旁路插件 MVP，但部分技术选型仍是目标而非已落地：HTTP 层使用 Go `net/http` 而非
Gin；健康检查使用进程内 ticker 而非 River；PostgreSQL 查询为手写 pgx 而非 sqlc；
前端 API client 为手写类型而非 OpenAPI 生成；密管为 `Vault` 接口 + MemoryVault，
Infisical/OpenBao 尚未接入。事实基准见
[current-state.md](../development/current-state.md)。

---

## 1. 定位：旁路插件，不做转发

E2M 不是替站长「开车的代驾」，而是「装在站长车上的行车电脑 + 路况播报」——
车还是站长自己开，E2M 只告诉他哪个轮胎快没气、要不要换道，并能帮他把
导航目的地一键设好。

一句话定位：

> **E2M 是装在站长中转站（sub2api / new-api / CPA）旁边的旁路插件（side-car），
> 通过各网关自己的官方管理 API，替站长自动配号、持续体检、按风险等级处置或提醒；
> E2M 永不进入请求数据面。**

三件核心事：

1. **一键配号** —— 上游给的新账号，E2M 调网关官方 `admin` 接口替站长批量建好、
   绑代理、设分组，站长一个字都不用填。
2. **持续体检** —— E2M 中心每分钟拉一遍各实例账号状态，发现某号响应异常
   （如连续 429）就判定并处置。
3. **按风险处置/提醒** —— 低风险自动做（换号/停坏号）事后告知；高风险发飞书卡片
   等站长审批；其余仅播报。

明确排除：
- **不做请求转发**：token 流量永远从站长实例贴代理直出上游，E2M 不碰。因此
  带宽、明文可见、请求改写、数据面单点等问题**从根上不存在**。
- **不完全接管**：站长的中转站仍是主角，E2M 是可被随时拔掉的插件。

---

## 2. 服务对象（双边）

| 角色 | 是谁 | E2M 为它做什么 |
|---|---|---|
| **站长（客户 A）** | 运营 sub2api / new-api / CPA 中转站 | 一键配号、号池体检、失效自动换号、QQ/飞书告警 |
| **上游渠道（客户 B）** | 按平台规范供给 API 资源（账号 / Key / 配额） | 统一供给入口、供给台账、按规范对接（免去逐个站长对接） |

理想态：站长专心业务、不碰运维；上游专心供资源、不碰对接；E2M 在中间做撮合与体检。

---

## 3. 起步阶段的三个组件（都很轻）

```
┌─────────────────────────────────────────────────────────────┐
│  ① E2M 中心（自研 Go，核心资产）                                │
│     - 上游在此上传账号（明文进密管，库里只存 credential_ref）     │
│     - 站长在此创建实例并生成该实例的一次性 Connector enrollment │
│     - 体检器：当前 ticker 轮询；目标 River 定时任务               │
│     - 决策引擎：低风险自动改 / 高风险发审批 / 其余仅提醒           │
│     - 审计（OperationAudit）+ 三维计量聚合                       │
└───────────────┬───────────────────────────┬─────────────────┘
                │ 经 AdminClient 调官方 admin API │ 飞书 / QQ
                ▼                             ▼
┌──────────────────────────────┐   ┌──────────────────────────────┐
│ ② 适配器层（自研，三个）        │   │ ③ 通知器（自研，~300 行）       │
│   sub2api / new-api / CPA     │   │   飞书 webhook（主，确定性送达） │
│   各包一层「替站长点按钮」的     │   │   QQ / NapCat（副，尽力而为）   │
│   官方管理 API 调用            │   │   高危动作发飞书卡片等审批        │
│   只依赖 AdminClient 接口      │   │   ── MaiBot 不在此，在社群侧 ── │
└──────────────────────────────┘   └──────────────────────────────┘
        凭证明文 → Infisical/OpenBao；中心与实例间只传 credential_ref
```

起步阶段**先砍掉**（列为后置增值项，见第 8 节）：
- ❌ 部署托管（Komodo）——站长自己部署，E2M 起步不管升级。
- ❌ 中央供给网关（GPT-Load 转发）——不做转发，API-Key 类上游也走配置下发。
- ❌ Temporal / 低代码 Portal——用 River 任务队列 + 自研 React 控制台替代。

---

## 4. 号池：两条链路都是「配置下发」，都不转发

按凭证类型分流，规则做成中心的一等策略：

- **订阅型 OAuth 账号（主）**：Claude / Codex / Grok 订阅号必须一号一 IP、贴固定
  代理保持指纹一致，绝不经任何中央出口。E2M 把「订阅凭证 + 配套独立代理」通过
  网关 admin API 注入站长实例（sub2api 建号绑 `proxy_id`；CPA 传 auth-files +
  per-key proxy），**流量从站长实例贴代理直出上游**。切换复用网关自带的
  429 冷却 / 重试 / failover。
- **API-Key 类上游**：裸 Key 同样经 admin API 写入站长实例的一个渠道
  （sub2api `type=apikey`、new-api `channel`），E2M 不聚合、不转发。

计量可信度决定计费模式：
- 配置下发链路 E2M 不在数据面、计量靠站长网关自报（站长有 root 可篡改）→
  **只收固定托管服务费 + 处置费，不做按量分成**。
- 若将来引入中央网关（后置项）走数据面、逐请求可信 → 那条链路才做按量分成。

---

## 5. RiskLevel 动作边界（分寸感）

沿用骨架 `contracts` 的 `RiskLevel L0-L3`，把每类操作明确归档：

| 等级 | 处置方式 | 覆盖动作举例 |
|---|---|---|
| **L0 只读** | 直接采集，无副作用 | 拉账号状态、用量、健康探测 |
| **L1 低风险写** | **自动执行**，事后飞书告知 | 停用已明显失效的号、切到备用号、清单个号的错误态 |
| **L2 中风险** | **发飞书卡片等审批**后执行 | 批量停号、注入新凭证、改分组路由、单实例升级 |
| **L3 高风险** | **强制审批 + 二次确认**，落审计 | 破坏性配置变更、可能影响全池的操作 |

每个处置动作写一条 `OperationAudit`（含 `Action / RiskLevel / Result /
ApprovalID`）。这是「日常琐事自动扛、大事必须问过你」的落地机制。

---

## 6. 每实例一个 Connector

站长先在控制台创建实例。Core 为该实例生成一次性 enrollment；enrollment
在生成时已经绑定用户与实例。站长在网关所在网络运行一个 Connector，
通过 enrollment token file 首次注册，并把长期 Connector token、本地 UI token、
网关地址、认证方式和管理凭证保存在该 Connector 的独立持久卷中。

Connector 只向 Core 发起出站请求。站长无需暴露网关管理端口，Core 也不接收、
保存或解析网关管理凭证。每个实例必须使用不同的 Connector ID、enrollment
文件、本地 UI 端口和数据卷。

### 适配器边界：`AdminClient` 接口
适配器只依赖一个接口，不关心任务传输细节：

```go
// 认证管理请求由实例绑定的 Connector 执行。
type AdminClient interface {
    Do(ctx context.Context, instanceID string, req *http.Request) (*http.Response, error)
}
```

`AdminClient` 保持适配器与任务传输解耦，但认证管理请求没有 Core 直连实现。

---

## 7. 前后端技术选型

贯穿逻辑：**后端沿用 Go 优势并补齐工程底座；前端与契约层全押最成熟的中后台方案、
绝不自研 UI；把 1-2 人的精力全留给核心——编排与适配器逻辑。**

### 7.1 后端（单体 Go，定位是编排器+体检器，不是网关）

| 层 | 目标选型 | 当前状态 | 理由 |
|---|---|---|---|
| 语言 | **Go** | 已实现 | 沿用骨架；与三网关 + Komodo 同构，读源码理解字段无缝 |
| HTTP 框架 | **Gin** | 目标；当前 `net/http` | Gin 生态最大、中间件全；当前 MVP 先用标准库路由收敛复杂度 |
| 数据库 | **PostgreSQL** | 已实现 | 替换内存 store；审计、审批、供给、能力声明等均有迁移 |
| 任务队列 ⭐ | **River**（PG 原生） | 目标；当前进程内 ticker | 体检+换号长期需要可恢复任务、重试和唯一任务；MVP 先用 ticker 验证策略 |
| 查询层 | **sqlc** | 目标；当前手写 pgx | 长期希望编译期类型安全；MVP 先用 Store 接口隔离查询层 |
| 迁移 | **golang-migrate** | 已实现 | 写第一行 SQL 前的前置 |
| 缓存 | **Redis** | 已在 compose 预置，Core 暂不依赖 | 后续用于体检实时快照、连接器在线态、审批短期会话 |
| 密管 | **Infisical / OpenBao** | 目标；当前 MemoryVault | 凭证生死线；当前已通过 `Vault` 接口隔离明文凭证落点 |
| 契约 | **OpenAPI**（ogen 出 server，前端生成 TS） | 目标；当前手写 TS client | 改字段前后端类型同时报错，压制 1-2 人团队的联调内耗 |

单体分模块：`internal/orchestrator`（配号/换号决策）、`internal/health`（体检器）、
`internal/adapters`（三网关适配器）、`internal/notify`（通知）、`internal/audit`。
不拆微服务——旁路插件模式下可能永远不需要拆。

**不用 Temporal**：起步任务（拉状态→判断→改→通知）River 足够；Temporal 是给
多步骤 durable workflow 的重系统，后置。

### 7.2 前端（B 端中后台控制台）

| 层 | 选型 | 理由 |
|---|---|---|
| 框架 | **React + TypeScript + Vite** | 中后台生态最厚、招人易、构建快 |
| UI 组件 ⭐ | **Ant Design Pro** | `ProTable`/`ProForm`/`ProLayout` 让实例列表/账号列表/审计日志几乎白送，省 80% 界面代码 |
| 数据请求 | **TanStack Query** | 号池健康轮询刷新/缓存/失效重取一行搞定 |
| 轻状态 | **Zustand** | 登录态/当前模式；不上 Redux |
| 图表 | **Ant Design Charts / ECharts** | 健康度、用量趋势 |
| 契约客户端 | **openapi-typescript + openapi-fetch** | 从后端 spec 生成全类型调用客户端 |

**不用低代码 Portal（Backstage / NocoBase）**：界面高度定制（号池健康可视化、
审批卡片、双边计费），低代码的「省事」在定制面前变成「与框架搏斗」，且多养一个
重系统违背「职责别太重」。单人维护一套 antd 控制台，远比维护 Backstage 插件现实。

### 7.3 部署形态
前端静态资源可 `embed` 进 Go 二进制单文件部署；中心用 `docker compose` 起
（PG + Redis + Infisical + e2m-core + 前端），与被托管对象技术栈一致，运维认知统一。

---

## 8. 后置增值项（明确触发条件，避免蓝图引力）

| 增值项 | 触发条件 | 起步替代 |
|---|---|---|
| 部署托管（Komodo） | 站长信任建立、明确要托管升级 | 站长自行部署 |
| Connector 托管安装 | 站长要求平台代管 Connector 升级 | 站长按实例自行部署 |
| 中央供给网关（GPT-Load） | 需按量分成 / API-Key 大盘聚合 | 配置下发 |
| Temporal 工作流 | Runbook 复杂到多步骤带补偿 | River 任务队列 |
| 飞书审批增强 | L2/L3 动作规模化 | 飞书卡片基础版 |

---

## 9. 旧骨架的处理

| 现有资产 | 处理 |
|---|---|
| `packages/e2m-contracts`（RiskLevel/OperationAudit/credential_ref/AdapterCapability） | **保留**，是纪律核心，继续沿用扩展 |
| 出站不开入站端口思路 | **保留**，每实例 Connector 沿用 |
| `app/e2m-core` 内存 store | 已抽象为 Store 接口，并增加 PostgreSQL 实现；sqlc 仍是目标 |
| `app/e2m-agent` | 仅作为每实例 outbound Connector；删除独立观测模型 |
| Backstage / NocoBase 占位包 | **移除或搁置**，改自研 React 控制台 |
| Dokploy/Komodo 模板 | **搁置**到部署托管增值项 |

上次会话确认的 4 个骨架结构问题在新架构下仍是前置：Store 抽接口、能力
declared/reported 拆分、账号作用域 + token、协议版本化（Connector 需要）。

---

## 10. 三个必须靠合同而非代码解决的风险

1. **凭证落点**：下发进站长网关的上游凭证，站长有 root 本就能读走。「注入即清内存」
   只保护传输段、不保护落点。对上游的凭证保护**最终靠合同**——卖「托管」给上游时
   如实说明。
2. **上游 ToS / 封号**：三网关自述可能违反上游 ToS，E2M 作托管方=共同运营者，
   封号潮可摧毁号池。业务性风险，非技术可解，条款切分责任。
3. **许可证**：new-api AGPL+署名、sub2api LGPL、Komodo GPL——只要不改源、独立进程、
   仅 API 编排、分发未修改官方镜像即在安全区；规模化前法务复核或买商业授权。
   CPA 虽 MIT 但有仓库删重建前科，自留快照。
