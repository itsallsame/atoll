# Atoll Recruiting 阶段测试报告（2026-09-13）

## 结论

当前 Recruiting 后端分段能力较完整，但产品的第一条用户闭环没有跑通，不能作为可用产品交付。

真实验收只允许以下入口：用户在公网 Web 页面发送自然语言消息；测试不预置 Company、Source 或 Recipe，不直接写业务表。输入为“新增字节跳动，并由系统继续发现招聘岗位列表来源”。

## 正式节点结果

- `https://staircase.justai.cool/` 返回 HTTP 200，Atoll systemd 服务正常。
- 自然语言消息经 `agent.ask` 到达 Agent，Agent 能发现并调用 Recruiting Actor。
- `recruiting.company.add` 成功创建 `bytedance / 字节跳动`。
- 复测时 `company.get` 和 `source.list` 一次调用成功；正式库为 1 Company、0 Source、0 SourceDiscovery、0 Recipe。
- 系统没有写入虚假 Source、Recipe 或半截 SourceDiscovery。
- 流程在 Source Discovery 前停止：公司官网未知，且没有可生成、登记、验证、枚举或自动选择的 Discovery Recipe。

因此当前可证明的连续路径只有：

`页面自然语言 → Agent → Recruiting Actor → Company`。

尚未成立的路径是：

`Company 名称 → 官网识别 → Discovery Recipe → 候选 Source → Source 校验/发布 → Baseline`。

## 本轮发现并修正的可测试性问题

- Recruiting P0 公开词原先只发布描述、不发布输入 Schema，Agent 首次调用只能从错误信息猜字段。本轮为 Company 查询/修改、Source Discovery 查询和系统状态等 P0 词补充闭合 JSON Schema。
- `cmd/devtools/asksteward` 使用旧 attach 帧，遗漏必填 `generation`，无法连接当前生产协议；本轮已修正。
- 上述修改只位于 Recruiting 扩展和开发测试客户端，没有修改 Atoll 核心目录。

## 自动化与真实网站测试

| 测试 | 结果 | 说明 |
|---|---|---|
| Recruiting Go/race 包回归 | 通过 | Actor、模型、Store、Executor 相关包通过 |
| 隔离 MySQL 8.4 合同回归 | 通过 | 非 root migrator/runtime 账号；Store 257.699 秒，Actor 3.280 秒 |
| 真实公开招聘 API 只读诊断 | 通过安全判定 | 读取 405 条；发现排序契约不成立，正确拒绝 Checkpoint，未提交生产增量资格 |
| `make recruiting-model-test` | 失败 | 代码测试和 fuzz 通过，但 statement coverage 71.7%，低于 75% 门槛 |
| 全仓库 `make test` | 失败 | 既有 Society Web 跨层依赖违规；`TestCoderunnerHumanJourney` 未观察到预期 Node 子进程 |
| 页面自然语言 P00 | 失败 | Company 成功，Recipe bootstrap 缺失，无法进入 Source Discovery |

## 下一阶段必须先完成

1. 增加“公司名/可选官网 → 官网候选与人工确认”的产品能力，证据和选择都必须进入领域审计，不能由人工在系统外搜索后直写数据库。
2. 增加 Discovery Recipe bootstrap：由系统生成候选 Resource，经真实页面 evidence-only 校验和审批后登记；Agent 能枚举并选择与 Company 官网 scope 匹配的 active 版本。
3. 建立一条从 `agent.ask` 起步、零业务预置的自动化 P00 测试，并在正式节点用至少一个真实公司完成到候选 Source；之后才继续 Source 发布、Listing/Detail Recipe、baseline 和每日增量验收。

在 P00 关闭前，S01—S25 的“完成”仅表示对应领域切片在其前置事实已存在时通过，不能合并推导为端到端产品已完成。
