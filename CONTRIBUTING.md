# 贡献指南

感谢参与 E2M API 项目。提交前请优先保证改动小而清晰,并通过本地工程化检查。

## 工具链

- Go:以 `go.work` 的 `go`/`toolchain` 为准。
- Node.js:以根目录 `.nvmrc` 为准,前端位于 `web/console`。
- Docker:用于本地全栈演示、镜像冒烟构建和生产 compose 模板验证。

## 常用命令

```bash
make fmt          # 格式化 Go 与前端代码
make fmt-check    # 检查格式化
make lint         # Go vet + ESLint
make test         # Go tests
make test-race    # Go race tests, requires CGO and a C compiler
make web-test     # 前端 Vitest 单元测试
make build        # 前端构建 + e2m-core 镜像构建
make ci           # 本地 CI 门禁
```

Windows PowerShell 下如未安装 `make`,可使用等价入口:

```powershell
.\scripts\e2m.ps1 ci
```

## Commit 规范

使用 Conventional Commits:

```text
type(scope): subject
```

常用类型:`feat`、`fix`、`docs`、`refactor`、`test`、`chore`、`ci`、`build`、`perf`、`security`。
常用 scope:`core`、`agent`、`contracts`、`console`、`deploy`、`docs`、`ci`、`deps`、`release`、`security`。

示例:

```text
feat(core): add managed upstream balance alert
ci(security): run govulncheck in scheduled workflow
docs(deploy): clarify production vault key handling
```

## PR 要求

- 一个 PR 聚焦一个主题,避免混合重构和功能改动。
- 填写 PR 模板,说明验证命令、风险和回滚方式。
- 涉及 API/部署/安全边界时,同步更新 README 或 `docs/`。
- 不提交 `.env`、真实凭证、运行时数据和本地 IDE 配置。

## 分支建议

- 功能:`feat/<short-topic>`
- 修复:`fix/<short-topic>`
- 文档:`docs/<short-topic>`
- 工程化:`chore/<short-topic>`
