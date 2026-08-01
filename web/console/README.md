# E2M 控制台

E2M Ops 的 B 端管理控制台。React + TypeScript + Vite + Ant Design Pro + TanStack Query。

生产环境下，构建产物 `dist/` 会被嵌入 e2m-core 的 Go 二进制（`internal/webui`），与 API 同源部署在 `/`。

## 本地开发

```bash
npm ci
npm run dev            # http://localhost:5173，/api 与 /healthz 代理到 :8080
```

先起后端（任选其一）：

```bash
# A. Docker 全栈（推荐）
docker compose -f ../../deployments/templates/compose/e2m-core-dev.compose.yml up -d

# B. 本地 core + compose 里的 postgres
E2M_CORE_STORE=postgres \
E2M_CORE_DATABASE_URL="postgres://e2m:e2m@127.0.0.1:5432/e2m?sslmode=disable" \
go run ../../app/e2m-core/cmd/e2m-core
```

## 构建

```bash
npm run build         # 输出到 dist/
```

账单、支付和供给尚未形成正式业务闭环，默认不会进入菜单或允许直链访问。仅在试验环境显式启用：

```dotenv
VITE_E2M_ENABLE_BILLING=true
VITE_E2M_ENABLE_PAYMENTS=true
VITE_E2M_ENABLE_SUPPLY=true
```

这些是 Vite 构建变量，只能放布尔开关，不能放密钥。默认值见 `.env.example`。

平台托管 Key 默认只通过 Connector 加密交付，不提供 owner 明文导出。旧兼容接口只有在 Core 显式设置 `E2M_OWNER_KEY_REVEAL=true` 时才开放；该模式会允许绕开未来的统一余额门禁，不建议在正式环境启用。

## 质量检查

```bash
npm run lint          # ESLint
npm run format:check  # Prettier 格式检查
npm run test          # Vitest 单元测试
```

Docker 镜像会在构建时自动执行本步骤并嵌入二进制，无需手动构建。

## 结构

- `src/api/` — 类型（镜像 e2m-contracts）、fetch 客户端、端点、TanStack 查询钩子
- `src/components/` — 权限与功能门禁、状态标签、公共交互组件
- `src/config/` — 控制台构建期开关
- `src/layouts/` — 按 active role 组织的业务导航与 ProLayout 外壳
- `src/pages/` — client、admin、supplier 的真实业务页面
