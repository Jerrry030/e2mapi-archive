# 试验功能开关

账单、支付和供应链在形成可信钱包、真实支付履约与供应商结算前默认关闭。正式环境同时保持前端和 Core 开关为 `false`；只在隔离的试验环境成对开启。

| 模块 | 前端构建开关 | Core 运行开关 |
| --- | --- | --- |
| 旧费用估算 | `VITE_E2M_ENABLE_BILLING` | `E2M_ENABLE_BILLING` |
| 支付（配置/渠道/订单/充值下单/webhook/到期清扫） | `VITE_E2M_ENABLE_PAYMENTS` | `E2M_ENABLE_PAYMENTS` |
| 供给登记与分配 | `VITE_E2M_ENABLE_SUPPLY` | `E2M_ENABLE_SUPPLY` |

支付开关是完整充值闭环的唯一 Core 门禁：管理端支付配置与订单、用户充值下单、支付渠道 webhook 回调都在它之下 fail-closed，到期清扫后台任务也只在它开启时启动（清扫间隔 `E2M_PAYMENT_EXPIRY_INTERVAL`，默认 60s）。历史上 webhook 与充值下单还要求 `E2M_ENABLE_HYBRID_SUPPLY`；该耦合已移除，hybrid 开关只保留给已退役的比例路由实验路径。

前端开关控制菜单和页面路由；Core 开关在认证 API 入口拦截相应请求，并对关闭模块返回 `404 feature_disabled`。两端任一关闭，都不能把模块视为可对外使用。

前端开关在构建 Console 时写入，Core 开关在进程启动时读取。通过生产 Compose 构建时，两者从同一组 `E2M_ENABLE_*` 值传入；修改开关后必须重新构建镜像并重启 Core。使用预构建镜像时，需要确认镜像内 Console 的构建开关与 Core 运行开关一致。

`E2M_OWNER_KEY_REVEAL` 是遗留兼容开关，不是正式产品功能。生产环境必须保持关闭，否则用户可能绕过受控 Connector 路径与未来余额门禁。
