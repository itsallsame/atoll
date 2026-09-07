# 切片 A：真实架构契约验证

2026-09-05。**结论：BLOCKED，未通过阶段 A。** 按用户要求暂停依赖该能力的实现，不进入新版原型与模拟用户验证。

## 已验证的事实

- 分支 `codex/job-application-autofill`；源码及安装的 Atoll 基线为 v0.0.11 / `b447c91a`。全部新增文件限于 `tools/cvmax/`，核心 tracked diff 为空。
- 使用独立 Atoll home、合成用户、私有频道、独立 Chromium 测试配置与本地测试页。未使用用户现有 root 凭证或真实招聘账户。
- 通过公开 `/ws` v5 接口声明现有 `mcp` 类、创建工具成员并绑定现有 local-device。应用没有注册新的 Go 类。见 [mount.json](evidence/mount.json)。
- Atoll 的 MCP stdio 适配器启动应用工具子进程；真实浏览器扩展已配对。CLI → Atoll → MCP → 应用工具 → 扩展的**只读观察链路**可用，能读到真实姓名/邮箱字段。
- 请求记录中的发送者为测试用户 `human:cvmax-alice:1788591962708`，不是 Atoll 托管 Codex Agent。Bob 对 Alice 频道的状态请求被公开入口以 `forbidden / no eligibility for channel` 拒绝。这里只证明该次非成员读取被拒绝，尚不能替代写入隔离验收。

## 阻塞复现

同一工具 `tool:cvmax-slice-a:1788592265742`，同一用户及频道，连续公开调用：

| 请求 | 实际结果 | request ID |
|---|---|---|
| `cvmax.status {}` | completed；插件 connected=true；runs=[] | `9f8c43b8-2af5-42d8-bcab-4ad0c94d32c2` |
| `cvmax.observe {tabId:204746263}` | completed；返回真实页面两个必填字段及 documentEpoch | `dcfc4534-f8e6-4014-8ad8-6b25a0c6f281` |
| `actor.describe {}` | completed，但 `words={}` | `966e81c2-a89a-422f-9779-ba8a1dae3d53` |
| `actor.describe {type:"cvmax.status"}` | failed / invalid_args，声称该工具未声明此 word | `2f17fb8c-6473-4c04-8af4-ed05a24d184a` |
| 再次 `cvmax.status {}` | 仍 completed，插件 connected=true | `97be7912-7316-4239-8ed9-03080bbc083e` |

完整请求与终态响应保存在 [boundary.json](evidence/boundary.json)。多次读取结果一致，观察后的描述请求约 16ms，不能据此归咎于启动未就绪或超时。

最小复核命令（在本目录、隔离测试环境运行期间；凭证仅在 ignored `.runtime` 中）：

```sh
node src/cli.mjs --config .runtime/alice-client.json status < /dev/null
node src/cli.mjs --config .runtime/alice-client.json doctor < /dev/null
printf '%s' '{"type":"cvmax.status"}' | node src/cli.mjs --config .runtime/alice-client.json doctor
node src/probe-boundary.mjs
```

初次测试驱动误读旧名称 `types`，已修正为当前源码定义的 `words`。修正后真实闸门仍失败，见 [checks.json](evidence/checks.json) 与 [calls.json](evidence/calls.json)。不能把初次 TypeError 当核心故障；核心契约不一致的证据是上表的实际响应。

## 核心边界与原因范围

需求是沿系统现有 MCP 扩展完成动态能力发现和受控填写，不能硬编码能力表来掩盖系统契约差异。

只读源码定位：

- [MCP refresh](../../../drivers/tools/mcp/actor.go) 将 `tools/list` 转为 word schema，写入 `_manifest` 后更新调用快照；此处只检查 State.Put 的 Go error，没有检查 Outcome.Accepted。
- [引擎能力投影](../../../lib/actorbase/manifest.go) 从 `_manifest` 读取动态 words。它在检查 Accepted 之前处理 `!Found`，因此部分被拒绝结果可能表现为空表。
- [引擎请求入口](../../../lib/actorbase/engine.go) 自行处理 `actor.describe`；应用 MCP server 不能通过增加同名业务工具覆盖该行为。
- [状态契约](../../../runtime/accessdoor/state.go) 区分 Go error 与拒绝 Outcome；二者不能混为一谈。

这些是需要核心维护者进一步核查的路径，**不是已证明的唯一根因**。本轮未读取核心 SQLite、未修改核心、未加入追踪补丁，也未验证根因修复。现有 MCP 配置内未找到可直接修正该投影的应用选项。

当前仍在系统设计内的操作只有只读复核、证据整理及隔离环境清理。跳过发现并硬编码执行、直连插件填值、增加核心注册或修改上述投影均不作为本轮兼容方案。不是断言 Atoll 永远无法承载业务，而是当前基线尚不满足已约定的验收。

## 明确未通过的项目

一个字段的实际填写与核验、附件上传、停止/节点断开/工具退出后的行为、重复请求、未知回执归并、恢复与人工编辑保护、文件数据面、完整申请、持久经验和自进化均未验收。测试驱动在发现闸门退出，没有启动任何业务 run。

本目录代码是停在闸门前的验证夹具，不是可发布插件。Skill 为仓库本地显式读取，未安装，也未验证自动发现和用户安装体验。旧原型及旧十角色评审不能用于证明新版通过。

尚未执行的验证代码也不能视为已正确实现。它仅覆盖固定字段与固定合成附件；夹具的 `ready_for_review` 目前用于单次操作状态，不能当作整份申请已完成。应用 JSON 状态文件的持久性、并发行为与崩溃恢复尚未认证。恢复实施时须先校正这些应用范围内的语义，再运行相应验收。

## 环境收尾

已通过 `atoll stop --dir` 关闭此次独立节点，并向核对过的自有浏览器启动进程发送 SIGTERM。18832–18835 无监听；用户原浏览器和旧原型未关闭。夹具数据留在 ignored `.runtime` 以便核查，不纳入可分享证据。收尾不代表生命周期故障验收通过。

下一步前置条件：核心维护侧提供在允许基线上可用的现有契约用法，或另行处理并验证核心问题；本任务保留“核心零改动”约束。满足后首先重跑发现闸门与切片 A，之后才做新版原型和十角色复验。
