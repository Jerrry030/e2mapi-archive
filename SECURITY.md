# 安全策略

E2M API 涉及托管实例、凭证存储和账号隔离边界,所有安全问题请优先私下披露。

## 支持范围

- `main` 分支是当前维护分支。
- 生产部署模板、凭证 vault、认证/RBAC、网关适配器和审计链路属于高优先级安全范围。

## 报告漏洞

请通过私有渠道联系维护者,不要在公开 issue 中贴出可利用细节、真实 endpoint、token、cookie、数据库连接串或 vault key。

报告中建议包含:

- 受影响组件和版本/commit。
- 复现步骤或最小 PoC。
- 影响范围评估。
- 是否已在生产环境观察到异常。

## 凭证处理要求

- 网关管理地址、API key 和管理 token 只保存在对应 Connector 的本地私有数据卷，不得进入 Core、Core 数据库或 Core Vault。
- Core 数据库只保存 `credential_ref`；Core Vault 仅保存通知、上游业务账号/API 提供方和代理等确实由 Core 使用的非网关凭证。
- `E2M_VAULT_KEY` 必须由外部密钥管理系统注入,不得提交仓库。
- `.env`、运行时目录和本地设置文件已在 `.gitignore` 中排除。
- 安全相关 PR 必须执行 `make ci` 与 `make security-scan` 或等价检查。
