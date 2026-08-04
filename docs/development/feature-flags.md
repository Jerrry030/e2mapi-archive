# 试验功能开关

更新：2026-08-04

平台商业化（支付、兑换、充值）默认关闭。正式环境同时保持前端和 Core 开关为 `false`；只在隔离的试验环境成对开启。

## Core 业务开关

`main.go` 启动时读入以下六个开关，注意后两个**不带** `E2M_ENABLE_` 前缀：

| 模块 | 前端构建开关 | Core 运行开关 | 当前状态 |
| --- | --- | --- | --- |
| 支付与兑换（配置/渠道/订单/充值下单/webhook/到期清扫/兑换码生成与兑换） | `VITE_E2M_ENABLE_PAYMENTS` | `E2M_ENABLE_PAYMENTS` | 有真实前后端消费方 |
| 旧费用估算 | `VITE_E2M_ENABLE_BILLING` | `E2M_ENABLE_BILLING` | 历史占位，无可达路由与页面 |
| 供给登记与分配 | `VITE_E2M_ENABLE_SUPPLY` | `E2M_ENABLE_SUPPLY` | 历史占位，无可达路由与页面 |
| 混合供给（已退役比例路由） | `VITE_E2M_ENABLE_HYBRID_SUPPLY` | `E2M_ENABLE_HYBRID_SUPPLY` | 当前不守任何可达路由，保留仅为兼容模板 |
| 上游情报推荐/实验 | — | `E2M_UPSTREAM_RECOMMENDATIONS` | 对应端点未注册（暗启动） |
| 上游优化自动执行 | — | `E2M_UPSTREAM_OPTIMIZATION_AUTO_APPLY` | 对应端点未注册（暗启动） |

## 支付开关的门禁范围

`E2M_ENABLE_PAYMENTS` 是整个商业化闭环的唯一 Core 门禁，关闭时以下路径在**认证之前**返回 `404 feature_disabled`：

- `/api/v1/admin/payment/*` — 支付配置、渠道实例、订单查询与取消；
- `/api/v1/payment/webhooks/*` — Stripe 与 EasyPay 回调（签名鉴权，非会话鉴权）；
- `/api/v1/owner/hybrid-supply/recharge-orders` — 用户充值下单；
- `/api/v1/admin/redeem-codes/*` — 兑换码生成、列表、停用、删除、`create-and-redeem`；
- `/api/v1/redeem` — 用户兑换。

**注意**：兑换码域（发卡与用户兑换）同样受这个开关控制。关掉支付不只是关掉充值，发卡与兑换也一起关闭。

订单到期清扫后台任务也只在开关开启时启动（间隔 `E2M_PAYMENT_EXPIRY_INTERVAL`，启动时读取，默认 60s）。

## 两端语义差异

前端开关控制菜单与页面路由，Core 开关在 API 入口拦截请求。两端任一关闭，都不能把模块视为可对外使用。

取值解析不一致，配置时以 Core 为准：Core 只认小写字面量 `true`；前端接受 `1|true|yes|on` 且大小写不敏感。

前端开关在构建 Console 时写入（`app/e2m-core/Dockerfile` 的 `VITE_E2M_ENABLE_*` build args），Core 开关在进程启动时读取。镜像目前只传 payments/billing/supply 三个，`VITE_E2M_ENABLE_HYBRID_SUPPLY` 未传（构建产物内恒为 false）。修改开关后必须重新构建镜像并重启 Core；使用预构建镜像时，需要确认镜像内 Console 的构建开关与 Core 运行开关一致。

## 商业化运行参数（非功能开关）

| 变量 | 读取时机 | 说明 |
| --- | --- | --- |
| `E2M_PAYMENT_EXPIRY_INTERVAL` | 启动时 | 订单到期清扫间隔，默认 60s |
| `E2M_PRICE_TABLE_PATH` | 启动时 | 基准价目表文件；加载失败为致命错误。留空使用内置引导快照 |
| `E2M_USD_TO_CNY_RATE` | **仅首启种子** | 之后以数据库值为准（系统设置 → 商务定价），热生效；改环境变量对已初始化的库无效 |
| `E2M_PLATFORM_BALANCE_THRESHOLD` | **仅首启种子** | 同上；留空表示关闭低余额告警 |
| `E2M_AUTH_INVITATION_REQUIRED` | 启动默认值 | `auth_settings` 行一旦存在即被完全覆盖 |

`E2M_OWNER_KEY_REVEAL` 是遗留兼容开关，不是正式产品功能。生产环境必须保持关闭，否则用户可能绕过受控 Connector 路径与余额门禁。
