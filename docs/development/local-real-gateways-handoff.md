# 本地真实实例测试交接记录

更新时间：2026-07-07
仓库路径：`D:\all\agent\e2mapi`

## 当前目标

项目此前主要依赖 Mock 数据。现在已经本地部署了真实的 `sub2api`、`new-api`、`CPA` 以及 `E2M` 实例，下一步目标是继续向真实实例和 E2M 控制面写入一批可重复、可观察的测试数据，用来验证当前开发的各项功能，并让前端页面在真实数据支撑下展示完整形态。

> 定位校正：本文早期记录中出现过 `tenant` / 租户说法。该模型已经废弃，后续真实栈种子和验证都应以个体账号 `users.id` / `user_id` 作为唯一资源归属边界，不再创建或依赖工作区、租户、团队。

## 已运行的本地服务

当前使用的 compose 文件：

```powershell
deployments/templates/compose/e2m-core-real-gateways.compose.yml
```

本地访问地址：

| 服务 | 地址 |
| --- | --- |
| E2M | http://localhost:18080 |
| sub2api | http://localhost:18090 |
| new-api | http://localhost:13000 |
| CPA | http://localhost:18317 |

统一账号密码目标：

```text
账号：admin@local.dev
密码：admin123456
```

补充说明：

- 用户之前反馈 `new-api: http://localhost:13000` 页面只显示 `dev`，不能直接登录。后续应优先确认是否需要通过 API 登录、是否前端静态资源/路由没有正确启用，或者该镜像本身只是开发占位页。
- 当前 E2M API 登录已验证可用。

## E2M 当前资源

E2M 中已存在账号归属下的真实实例：

| 类型 | 名称 | ID |
| --- | --- | --- |
| Instance | sub2api | `inst-9cef27a502cd7c82` |
| Instance | new-api | `inst-958940169e2b72ed` |
| Instance | CPA | `inst-3eb2841327f0abcb` |

可用 E2M 登录验证命令：

```powershell
$body = @{ email='admin@local.dev'; password='admin123456' } | ConvertTo-Json -Compress
$login = Invoke-RestMethod -Method Post -Uri http://localhost:18080/api/v1/auth/login -Body $body -ContentType 'application/json'
$h = @{ Authorization = "Bearer $($login.token)" }
$instances = Invoke-RestMethod -Method Get -Uri http://localhost:18080/api/v1/instances -Headers $h
$instances | ConvertTo-Json -Depth 8
```

拉取各实例账号/通道/auth-file 的验证命令：

```powershell
foreach ($inst in $instances) {
  Write-Host "--- $($inst.name) $($inst.kind) $($inst.id)"
  Invoke-RestMethod -Method Get -Uri "http://localhost:18080/api/v1/instances/$($inst.id)/accounts" -Headers $h | ConvertTo-Json -Depth 8
}
```

验证结果：

- sub2api：可通过 E2M 读取到 2 个账号。
- new-api：可通过 E2M 读取到 1 个 channel。
- CPA：可通过 E2M 读取到 2 个 auth files。

## 已写入真实网关的数据

### sub2api

已创建种子账号：

| 字段 | 值 |
| --- | --- |
| ID | `2` |
| name | `e2m-seed-sub2api-primary` |
| status | active / schedulable |
| priority | `10` |

此外还有一个旧测试账号：

| 字段 | 值 |
| --- | --- |
| ID | `1` |

已知可用创建方式：

```powershell
# POST http://localhost:18090/api/v1/admin/accounts
# Header: x-api-key: <sub2api admin api key>
```

请求体关键结构如下，注意字段是 `credentials`，不是 `credential`：

```json
{
  "name": "e2m-seed-sub2api-primary",
  "platform": "openai",
  "type": "apikey",
  "credentials": {
    "api_key": "sk-local-sub2api-primary",
    "base_url": "https://api.openai.local/v1",
    "model_mapping": {
      "gpt-4o-mini": "gpt-4o-mini"
    }
  },
  "extra": {},
  "schedulable": true,
  "priority": 10,
  "concurrency": 3
}
```

### new-api

已创建种子 channel：

| 字段 | 值 |
| --- | --- |
| ID | `1` |
| name | `e2m-seed-newapi-primary` |
| status | active / schedulable |
| balance | `88.4` |
| used quota | `1200` |
| tag | `e2m:seed-probe-newapi` |

已知可用创建 payload：

```json
{
  "mode": "single",
  "channel": {
    "name": "e2m-seed-newapi-primary",
    "type": 1,
    "status": 1,
    "group": "default",
    "priority": 10,
    "tag": "e2m:seed-probe-newapi",
    "key": "sk-local-newapi-primary",
    "models": "gpt-4o-mini,gpt-5-mini",
    "base_url": "https://api.openai.local/v1",
    "balance": 88.4,
    "used_quota": 1200
  }
}
```

注意：

- `PUT /api/channel/` 直接用 `{id,status}` 曾失败，返回 `Invalid parameters`。
- 用 `{channel:{...}}` 包裹更新也曾失败，返回 `record not found`。
- 如果后续需要通过 E2M 改 new-api channel 的启停/可调度状态，需要继续检查 new-api 上游 API 的更新参数形态。

### CPA

已创建种子 auth files：

| name | 状态 |
| --- | --- |
| `e2m-seed-cpa-primary.json` | active / schedulable |
| `e2m-seed-cpa-spare.json` | disabled / not schedulable |

正确创建方式是 raw auth JSON 加 query 参数 `name`：

```powershell
Invoke-RestMethod `
  -Method Post `
  -Uri 'http://localhost:18317/v0/management/auth-files?name=e2m-seed-cpa-primary.json' `
  -Headers @{ Authorization='Bearer admin123456' } `
  -Body $raw `
  -ContentType 'application/json'
```

状态切换接口：

```powershell
Invoke-RestMethod `
  -Method Patch `
  -Uri 'http://localhost:18317/v0/management/auth-files/status' `
  -Headers @{ Authorization='Bearer admin123456' } `
  -Body '{"name":"e2m-seed-cpa-primary.json","disabled":true}' `
  -ContentType 'application/json'
```

## 已处理的重要问题

new-api 调用 `/api/user/token` 时会轮换 token。旧方案曾把该 token 写入 E2M vault，导致 E2M 访问 new-api 返回 502；当前方案已改为连接器本地配置，凭证不进入 E2M Core。

已执行修复：

```powershell
.\scripts\bootstrap-real-gateways.ps1 -SkipComposeUp
```

该命令会用最新网关凭证更新三个独立 Connector 持久卷，并通过 enrollment token file 重建对应 Connector 服务。修复后，E2M 的 `/instances/{id}/accounts` 对 3 个实例都可正常读取。

注意：再次运行 bootstrap 可能会轮换 sub2api/new-api 相关凭据，并重建连接器容器。运行后最好重新验证 E2M 访问真实网关是否正常。

## 当前仍缺的数据

虽然真实网关里已经有账号/channel/auth-file，但 E2M 控制面自己的业务数据仍基本为空。已观察到：

- `upstream_pools` 为空。
- `upstream_channels` 为空。
- `route_plans` 预计为空。
- 供应、账本、审批、健康观测、自动切换摘要等页面仍缺少完整演示数据。

下一步应继续写入 E2M 控制面数据，而不仅仅是写真实网关数据。

建议通过可重复脚本完成，脚本位置建议：

```text
scripts/seed-real-gateways.ps1
```

脚本应尽量幂等：按名称、label、remote_id 检查已有数据，存在则更新或跳过，不重复创建。

## 建议下一步实施清单

1. 新建 `scripts/seed-real-gateways.ps1`。
2. 脚本登录 E2M：`admin@local.dev` / `admin123456`。
3. 确保供给能力账号存在，例如 `local-supplier@example.local`。
4. 确保通知路由存在，可先用本地 harmless webhook 或 disabled route。
5. 创建供应报价和供应账本数据，让 Supply/Billing 页面有内容。
6. 创建 upstream pools，例如：
   - `Local OpenAI Stable Pool`
   - `Local Anthropic OAuth Pool`
7. 创建 upstream channels，并绑定真实网关 remote id：
   - new-api channel：`labels.remote_id = "1"`
   - sub2api account：`labels.remote_id = "2"`
   - CPA auth file：`labels.remote_id = "e2m-seed-cpa-primary.json"`
8. 创建 route plans，让每个真实实例和 pool/channel 有可查看的路由计划。
9. 触发 route plan dry-run；在安全时触发 apply。
10. 写入 route strategy，让 auto apply / fallback / health threshold 等页面有配置。
11. 写入健康观测和健康快照，让 Pool Health、自动切换摘要、决策记录能展示数据。
12. 提交至少一条 approval request，让 Approvals 页面有待处理/已处理记录。
13. 最后通过 E2M API 和前端逐页验证数据展示。

## 健康观测数据的选择

旧的全局 `E2M_INGEST_TOKEN` 与 `/api/v1/channel-observations` 入口已经删除。它无法把写入权限限制到 Connector 所绑定的实例，会形成第二套跨实例机器身份。

当前如需本地演示健康快照，可直接写入测试数据库。正式采集应作为绑定实例的 Connector typed protocol 能力实现，由 Core 从 Connector 身份推导实例范围，而不是恢复全局静态令牌。

## 需要重点验证的 E2M API

建议 seed 后依次验证：

```text
GET /api/v1/upstream-pools
GET /api/v1/upstream-channels
GET /api/v1/route-plans
GET /api/v1/supply-offers
GET /api/v1/supply-ledger
GET /api/v1/approvals
GET /api/v1/health-snapshots
GET /api/v1/route-plans/{id}/auto-switch-summary
```

也建议继续验证真实实例账号读取：

```text
GET /api/v1/instances/{id}/accounts
```

## 相关代码位置

核心脚本和 compose：

```text
scripts/bootstrap-real-gateways.ps1
deployments/templates/compose/e2m-core-real-gateways.compose.yml
```

E2M HTTP API：

```text
app/e2m-core/internal/httpapi/server.go
app/e2m-core/internal/httpapi/upstream.go
app/e2m-core/internal/httpapi/health_metrics.go
```

真实网关适配器：

```text
app/e2m-core/internal/adapters/sub2api/adapter.go
app/e2m-core/internal/adapters/newapi/adapter.go
app/e2m-core/internal/adapters/cpa/adapter.go
```

contracts：

```text
packages/e2m-contracts/upstream_pool.go
packages/e2m-contracts/channel_health.go
packages/e2m-contracts/route_strategy.go
packages/e2m-contracts/auth.go
```

## Mock 数据是否可清理的初步判断

此前用户提出：既然已有真实实例，评估原本 Mock 那套是否过时可清理。

当前建议：

- 不建议立刻删除全部 Mock。
- Mock 仍适合单元测试、适配器契约测试、CI、无 Docker/无真实网关环境的快速开发。
- 真实实例应成为本地集成测试和人工验收的主路径。
- 后续可以把 Mock 从“默认开发路径”降级为“测试 fixture / fallback”，并清理重复、过时、与真实 API 形态不一致的 Mock。

推荐在完成真实数据 seed 和 UI 验证之后，再做一次专项清理：

1. 标记哪些 Mock 仍被测试引用。
2. 删除没有测试价值、与真实 API 偏离明显的 Mock。
3. 保留最小可维护 Mock，用于 CI 和边界场景。
4. 更新 README/开发文档，把真实实例 compose + seed 脚本作为推荐本地验证流程。

## 注意事项

- 当前 git worktree 很脏，有大量用户/未提交变更。不要执行 `git reset --hard`、`git checkout --` 等会覆盖用户工作的命令。
- 修改文件时应尽量小范围进行，避免顺手重构。
- 本地运行脚本可能会改写 `deployments/runtime/real-gateways/` 下的运行时文件；这些通常不应提交。
- 不要在最终回复或文档中泄露真实 token。当前可使用本地默认密码/本地测试 key 说明形态，但不要展开 runtime enrollment 或 Connector 本地配置中的敏感值。

## 推荐给新窗口的第一步

从这里继续最稳：

1. 先运行 E2M 登录和实例账号读取命令，确认当前服务仍在线。
2. 阅读 `app/e2m-core/internal/httpapi/upstream.go` 和相关 contracts，确认创建 upstream pool/channel/route plan 的 API payload。
3. 新建幂等脚本 `scripts/seed-real-gateways.ps1`。
4. 先只 seed upstream pools/channels/route plans，验证 API 和前端页面。
5. 再补 supply、approvals、health observations、auto-switch summary 数据。

## 配套接替提示词

可直接复制以下文件内容到新窗口，作为接替任务的第一条消息：

```text
docs/development/local-real-gateways-continuation-prompt.md
```
