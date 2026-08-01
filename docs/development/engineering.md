# 工程化基线

本文档说明 E2M API 仓库的工具链、质量门禁、协作规范、安全检查和发布流程。

## 工具链约束

- Go 版本以 `go.work` 为准,各模块 `go.mod` 保持一致。
- Node.js 版本以根目录 `.nvmrc` 为准,CI 与 Docker 前端构建同样使用 Node 22。
- 前端依赖必须通过 `npm ci` 基于 `web/console/package-lock.json` 安装。
- 编辑器基础风格由 `.editorconfig` 统一。
- Docker Go 构建阶段支持 `--build-arg GOPROXY=https://goproxy.cn,direct` 等代理覆盖,便于国内网络环境构建。

## 本地入口

根目录 `Makefile` 是统一入口:

```bash
make fmt
make fmt-check
make lint
make test
make test-race
make web-test
make build
make ci
make security-scan
```

Windows PowerShell 下如未安装 `make`,使用等价入口:

```powershell
.\scripts\e2m.ps1 ci
.\scripts\e2m.ps1 security-scan
```

`make ci` 覆盖格式化、lint、Go 测试、前端单测和前端构建。提交前至少执行 `make ci`。
`make test-race` 与 CI 中的 Go race tests 需要 CGO 和 C compiler；Windows 本地如未安装 GCC,可先执行普通 `make test`。

## 代码质量

- Go:使用 `gofmt` 统一格式,`go vet` 作为基础 lint,`.golangci.yml` 保留更严格的 golangci-lint 配置。
- Web:使用 ESLint flat config 与 Prettier,脚本位于 `web/console/package.json`。
- CI 在 PR 和 main push 上执行 Go、Web、Docker smoke build 三类检查。

## 提交与评审

- Commit message 遵循 Conventional Commits,配置见 `commitlint.config.cjs`。
- Husky 钩子在提交前执行格式化/lint 检查,提交信息通过 commitlint 校验。
- PR 必须填写 `.github/PULL_REQUEST_TEMPLATE.md`,issue 使用 `.github/ISSUE_TEMPLATE/`。
- `CODEOWNERS` 用于默认评审路由,后续可替换为真实团队或个人账号。

## 安全治理

- Dependabot 覆盖 Go modules、npm 和 GitHub Actions。
- `Security` workflow 覆盖 gitleaks、govulncheck 和 npm audit。
- 本地安全检查入口为 `make security-scan`。运行该目标前需确保本机已安装 `govulncheck`。
- 安全披露与凭证处理要求见 `SECURITY.md`。

## 发布流程

版本号遵循 SemVer。推送 `vX.Y.Z` tag 后,`Release` workflow 会:

1. 构建前端控制台。
2. 将前端产物嵌入 core webui 目录。
3. 构建 Linux/Windows 的 Core 与 Connector 二进制。
4. 创建 GitHub Release 并上传产物。

变更记录维护在 `CHANGELOG.md` 的 `[Unreleased]` 区域,发布前将条目归档到目标版本。
