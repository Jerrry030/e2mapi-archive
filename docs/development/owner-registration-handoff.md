# 个体账号注册与账号能力登录现状

> **历史文档（2026-07-09 快照）。** 本文所述事实已被 2026-08-04 平台商业化批次全面取代：支付下单与回调、钱包充值、兑换码、基准价目表定价、用户级限流、统一设置模块均已实现（默认由 `E2M_ENABLE_PAYMENTS` 关闭），控制台信息架构也已重构为「平台管理 / 通用功能」两区。本文**不再对任何其他文档具有优先权**；现状以 [current-state.md](current-state.md) 与 [platform-commerce-execution-plan.md](platform-commerce-execution-plan.md) 为准。

Updated: 2026-07-09

本文记录当前项目的注册、登录和权限模型。E2M 面向个体账号：一个邮箱对应一个账号，
实例、凭证、通知、审计、账单等资源直接归属到 `users.id` / `user_id`。旧的租户/团队/工作区
模型已经废弃，不能再作为运行时资源隔离或用户可见概念。

## 当前账号能力

| 角色值 | 作用范围 | 用户可理解能力 |
|---|---|---|
| `platform_admin` | 全局平台 | 管理用户、系统注册设置、平台上游池、供给分配等平台级能力 |
| `owner` | 当前账号的 `user_id` | 托管能力：接入实例、安装连接器、写入凭证、配置通知、待确认操作、账单、一键开通、查看健康与审计 |
| `supplier` | 当前账号的 `user_id` | 供给能力：写入上游/代理凭证、登记供给、查看自己相关的供给台账和审计 |

约束：

- `platform_admin` 不能与业务能力混用。
- 同一账号可以同时拥有 `owner` + `supplier`，登录后前端用右上角模式切换决定当前操作面。
- 业务角色没有下级用户管理权限，用户管理仅限 `platform_admin`。

## 注册闭环

公开注册只创建个体账号：

1. 访问 `/register`。
2. 输入登录邮箱、用户名 / 昵称和密码。
3. 后端校验注册开关、邮箱后缀白名单，以及可选 Cloudflare Turnstile。
4. 系统创建 `roles: ["owner"]` 的用户，用户 ID 即资源归属键。
5. 注册成功后返回 token，前端写入 session 并进入控制台。
6. 用户进入入门向导，按“写入凭证 -> 接入实例 -> 安装连接器 -> 配通知 -> 一键开通 -> 发布验证”完成托管闭环。

公开注册不会创建 `platform_admin` 或 `supplier`。供给能力由平台管理员在用户管理里分配。

## 接口边界

| 资源面 | 允许角色 |
|---|---|
| 托管实例、账号、连接器、待确认操作、账单、通知、路由计划、一键开通、自动切换摘要 | `platform_admin` 或同账号 `owner` |
| 供给登记 | `platform_admin` 或同账号 `supplier` |
| 供给分配/回收 | `platform_admin` |
| 凭证管理 | `platform_admin` 可管理全部；`owner` 可管理自己账号的凭证；`supplier` 仅可管理自己账号的 `upstream` / `proxy` 凭证 |
| 审计 | `platform_admin` 全局；业务角色仅看自己账号 |
| 用户/系统设置/平台上游池 | `platform_admin` |

## 验证命令

```powershell
go test ./app/e2m-core/... ./packages/e2m-contracts/...
cd web/console
npm test -- --run
npm run build
```

## 迁移说明

当前项目仍处雏形期，不维护旧租户/工作区模型到新账号模型的自动升级路径。
数据库以重新初始化为准：新库直接创建 `users.id` 数字账号 ID，并用 `user_id` 作为资源归属键。
