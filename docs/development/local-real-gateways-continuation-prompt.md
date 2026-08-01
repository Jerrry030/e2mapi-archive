# 新窗口接替提示词

你现在接手 `D:\all\agent\e2mapi` 仓库中的本地真实实例测试任务。请先阅读：

```text
D:\all\agent\e2mapi\docs\development\local-real-gateways-handoff.md
```

## 任务背景

这个项目之前大量依赖 Mock 数据。现在本地已经部署了真实的 `sub2api`、`new-api`、`CPA` 和 `E2M` 实例，目标是继续向真实实例和 E2M 控制面写入一批可重复、可观察、可用于前端展示和功能验证的数据，验证当前开发的各项功能是否生效，并让我能看到项目在真实数据支撑下的完整形态。

## 当前本地服务

请基于以下本地服务继续：

```text
E2M:     http://localhost:18080
sub2api: http://localhost:18090
new-api: http://localhost:13000
CPA:     http://localhost:18317
```

统一账号密码：

```text
账号：admin@local.dev
密码：admin123456
```

当前已知 E2M 实例：

```text
sub2api: inst-9cef27a502cd7c82 <!-- gitleaks:allow -- public fixture ID -->
new-api: inst-958940169e2b72ed <!-- gitleaks:allow -- public fixture ID -->
CPA:     inst-3eb2841327f0abcb
```

真实网关中已存在基础种子数据：

```text
sub2api account: e2m-seed-sub2api-primary, ID 2
new-api channel: e2m-seed-newapi-primary, ID 1
CPA auth files:
  - e2m-seed-cpa-primary.json
  - e2m-seed-cpa-spare.json
```

## 请你先做的事

1. 先验证当前服务是否仍在运行。
2. 用 E2M 登录接口确认 `admin@local.dev / admin123456` 可登录。
3. 调用 E2M 的 `/api/v1/instances` 和 `/api/v1/instances/{id}/accounts`，确认仍能读取 3 个真实网关的数据。
4. 阅读相关 API 和 contracts，确认创建 upstream pool/channel/route plan/supply/approval/health 数据的 payload。
5. 新建一个幂等脚本：

```text
D:\all\agent\e2mapi\scripts\seed-real-gateways.ps1
```

脚本目标是：一键把本地真实网关和 E2M 控制面补齐成可演示、可测试的数据环境。

## seed 脚本应覆盖的内容

请优先实现这些：

1. 登录 E2M 并复用 token。
2. 确保供给能力账号存在，例如 `local-supplier@example.local`，不要创建租户/工作区。
3. 创建或更新 upstream pools，例如：
   - `Local OpenAI Stable Pool`
   - `Local Anthropic OAuth Pool`
4. 创建或更新 upstream channels，并绑定真实网关 remote id：
   - new-api channel：`remote_id = "1"`
   - sub2api account：`remote_id = "2"`
   - CPA auth file：`remote_id = "e2m-seed-cpa-primary.json"`
5. 创建 route plans，并让它们关联真实实例、pool 和 channel。
6. 创建 supply offers / supply ledger，让供应和账务页面有真实数据。
7. 创建至少一条 approval request，让审批页面有数据。
8. 补健康观测或健康快照数据，让 Pool Health、自动切换摘要、决策记录等页面有可展示内容。
9. 脚本必须幂等：重复运行不应重复制造同名数据。
10. 脚本运行后输出关键资源 ID 和下一步验证命令。

## 实现约束

- 当前 git worktree 很脏，有大量用户/未提交变更。不要执行 `git reset --hard`、`git checkout --` 或任何会覆盖用户工作的命令。
- 不要删除或回滚已有改动。
- 优先小范围修改。
- 修改文件请用 `apply_patch`。
- 不要在最终回复中泄露 runtime vault 文件里的真实 token。
- 运行脚本可能会改写 `deployments/runtime/real-gateways/` 下的运行时文件，这类文件通常不要提交。
- 如果 `new-api` token 失效，可参考交接文档中已验证过的修复命令：

```powershell
.\scripts\bootstrap-real-gateways.ps1 -SkipComposeUp
```

该命令会刷新每个实例的 Connector enrollment 文件和本地持久卷配置，
再重建 Connector 服务；不会向 Core 注入网关凭证。

## 重点参考文件

```text
D:\all\agent\e2mapi\scripts\bootstrap-real-gateways.ps1
D:\all\agent\e2mapi\deployments\templates\compose\e2m-core-real-gateways.compose.yml
D:\all\agent\e2mapi\app\e2m-core\internal\httpapi\server.go
D:\all\agent\e2mapi\app\e2m-core\internal\httpapi\upstream.go
D:\all\agent\e2mapi\app\e2m-core\internal\httpapi\health_metrics.go
D:\all\agent\e2mapi\packages\e2m-contracts\upstream_pool.go
D:\all\agent\e2mapi\packages\e2m-contracts\channel_health.go
D:\all\agent\e2mapi\packages\e2m-contracts\route_strategy.go
D:\all\agent\e2mapi\packages\e2m-contracts\auth.go
```

## 验证目标

完成后请验证这些 API 至少有合理数据返回：

```text
GET /api/v1/instances
GET /api/v1/instances/{id}/accounts
GET /api/v1/upstream-pools
GET /api/v1/upstream-channels
GET /api/v1/route-plans
GET /api/v1/supply-offers
GET /api/v1/supply-ledger
GET /api/v1/approvals
GET /api/v1/health-snapshots
GET /api/v1/route-plans/{id}/auto-switch-summary
```

如果前端可运行，也请打开 E2M 控制台逐页确认页面不再是空状态。

## 关于 Mock 清理

暂时不要直接删除全部 Mock。当前建议是：真实实例成为本地集成测试和人工验收主路径；Mock 保留为单元测试、适配器契约测试、CI 和无 Docker 环境 fallback。等真实 seed 和 UI 验证完成后，再专项评估哪些 Mock 可以清理。

## 最终交付

请交付：

1. `scripts/seed-real-gateways.ps1`。
2. 必要的 compose/API 配置小改动。
3. 运行和验证结果摘要。
4. 说明哪些页面/API 已经有真实数据支撑。
5. 如发现 Mock 已明显过时，列出建议清理项，但先不要大规模删除。
