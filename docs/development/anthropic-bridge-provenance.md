# Anthropic 协议桥：代码来源与裁剪记录

更新：2026-08-05

`app/e2m-core/internal/anthropicbridge` 的协议翻译层**移植自 sub2api**
（<https://github.com/Wei-Shaw/sub2api>，**LGPL-3.0**），钉在 commit
`00b8596176809906993169c283671811ad04f58d`（2026-08-04）的
`backend/internal/pkg/apicompat` 包。

本文件是唯一的来源记录——代码文件内不加来源标注，按项目负责人 2026-08-05 的决定。

## 为什么需要这份记录

E2M 是 **MIT**，sub2api 是 **LGPL-3.0**。二者不兼容。

LGPL 的 copyleft 义务在**分发**时触发，纯内部自用不触发。本仓库当前按自用定位，
因此现状没有义务问题。但一旦本仓库公开发布（推送到公开远端、发布二进制、
对外提供服务的源码分发），下列文件就必须先处理，否则构成许可证冲突：

| 文件 | 来源 | 处置 |
|---|---|---|
| `bridge.go` | `chatcompletions_anthropic_bridge.go` | 移植（含裁剪，见下） |
| `types.go` | `types.go` 的 Anthropic + Chat Completions 段 | 移植，Responses 段已删 |
| `bridge_test.go` | `chatcompletions_anthropic_bridge_test.go` | 移植，去 testify、删 Responses 路径用例 |
| `helpers.go` | — | **E2M 原生编写**，非移植 |
| `require_test.go` | — | **E2M 原生编写**，非移植 |

公开发布前的选项：① 按 LGPL-3.0 单独授权这几个文件并保留原始版权声明；
② 依照本文件记录的行为规格原生重写 `bridge.go` 与 `types.go`。

## 裁剪了什么

原包 34 个文件、**8,315 行**非测试代码。移植进来 **1,578 行**（不含测试），裁掉 81%。

**整体删除**：全部 Responses API 中间表示（`chatcompletions_responses_bridge.go`、
`anthropic_to_responses*.go`、`responses_to_*.go`、`responses_namespace.go`、
`responses_client_tools.go`、`response_format.go` 等，约 6,600 行）。E2M 的上游
一律 OpenAI 兼容，不存在 Responses 语义，该层是纯开销。连带删除的还有 Codex
命名空间、tool-search 代理、工具输出媒体归属等子系统。

**行为改动**（三处必须改，否则会损坏 E2M 数据或打第三方端点报 400）：

1. **不再丢弃 system 内容**。原实现会静默丢弃任何以 `x-anthropic-billing-header: `
   开头的 system 文本块（`isAnthropicBillingHeaderText`）。那是 sub2api 自己的
   传输约定；在 E2M 会静默删除客户内容。已删除该过滤。
2. **不再改写工具入参**。原实现对名为 `Read` 的工具特判，删掉它值为空串的
   `pages` 字段（`sanitizeAnthropicToolUseInput`）。客户若有同名工具，入参会被
   静默篡改。已删除，入参原样透传。
3. **不再凭空下发请求字段**：
   - `reasoning_effort` 原实现默认 `"medium"` 且**总是**下发；改为仅当客户端
     显式设置 `output_config.effort` 时才带上。
   - `parallel_tool_calls` 原实现总是设为 `true`；Anthropic 没有对应旋钮，
     改为完全不下发。
   - `max_completion_tokens` 改为 `max_tokens`——第三方 OpenAI 兼容端点绝大
     多数实现的是后者。

**原生重写**（原实现与 Responses 层耦合，无法直接使用）：`helpers.go` 中的
18 个 helper，其中 `normalizeChatMessages` 最关键——它把工具调用历史修复成
OpenAI 兼容端点能接受的形状（丢弃孤儿 tool 回复、剔除无应答的 `tool_calls`、
把每条 tool 回复紧跟在宣告它的 assistant 之后）。Anthropic 客户端的截断会话
完全可能产出不配对的历史，不修复就会被上游硬拒。

## 为什么保留这批代码而不是全部重写

流式方向有四个边界情况是靠猜猜不出来的，也是移植的主要价值（`bridge.go`
头部注释有完整说明，测试逐条覆盖）：延迟宣告工具名、宣告前的参数缓冲、
工具名始终不到达时的兜底、零参数增量时的 `{}` 占位。

## 验证

移植后的包通过了 sub2api 自己的规格套件（39 个测试函数）。唯三失败的用例
正是上述三处有意的行为改动，已改断言为 E2M 的预期行为并在用例上注明原因。
