# Atoll Recruiting Deep Discovery v1 验收

日期：2026-09-13

状态：通过（Deep Discovery v1 范围）

## 验收范围

本验收只判断“一次性深度发现招聘入口”是否已经成为 Atoll Recruiting 的领域能力，不把后续 Source 创建、Recipe 生成/发布、baseline 或每日增量冒充为本切片成果。

实现保持 Atoll 核心冻结：`protocol/`、`runtime/`、`lib/`、`platform/`、`registry/` 相对本分支基线均无改动。正式运行使用当前生产机的 systemd Atoll 服务和已指定的远程 `staircase` MySQL；未启动 Docker 数据库，未使用 root 数据库账号，也没有通过数据库旁路推进业务状态。

## 已证明的领域契约

- 一公司同一时刻至多一个活动 Mission，终态后才释放下一 generation；
- 八阶段只能停留或逐级推进，不能跳级，也不能在覆盖不足时完成；
- 搜索轮数与外部操作预算在 Mission 创建时冻结，Browser Probe 在派发前原子消耗一次操作；
- Evidence Graph 保存固定节点/边类型、来源、Evidence URL 或 Artifact 和判断依据；
- Web Search 不能单独把 ListURL 提升为最终已验证候选；
- `waiting_human → resume` 保留原 checkpoint 和图，cancel 保留证据并释放活动键；
- 所有修改命令带 command receipt、request hash、expected version、领域事件和稳定重放；
- Browser 探测是 Recruiting Actor 创建的异步 Work，不是 Agent 绕过领域权威直接调用 Executor；
- Browser Executor 复用同一个 `recruiting-executor` class 和既有 offer/accept/start/result、Attempt fence、BudgetPermit、Artifact、dispatch 与恢复协议；
- Browser 只读效果由 typed attestation 拒绝写请求、表单、下载、弹窗与跨源文档导航，响应 DOM 和 trace 分别保存为 response/supporting Artifact。
- `browser.get` 同时返回 Probe、Work 和 result；即使没有成功 result，也能根据权威 Work 终态提示人工结案并创建新的预算化 Probe，不会永久显示“等待”。通用 `work.retry` 明确拒绝为不可变 Probe 创建一个失去绑定的新 Work。

## 真实业务验证

字节跳动 Mission `mission-bytedance-deep-discovery-g1-20260913` 在正式远程数据库中为 `completed/completed`，版本 9。七个不可变 checkpoint 依次覆盖 `scope_building`、`brand_expansion`、`site_enumeration`、`site_exploration`、`pool_detection`、`candidate_validation` 和 `coverage_review`，最终保存 40 个节点、39 条边与 3 个验证 ListURL：

- `https://joinbytedance.com/search`
- `https://jobs.bytedance.com/campus/position?current=1&limit=10`
- `https://lifeattiktok.com/search?language=en`

候选不是由搜索结果直接批准：最终三个 ListURL 的 sensor 为 `network` 或 `detail_reverse`，另有 Browser、官网和排除盲区证据共同形成图。

## 真实 Browser 与恢复验证

正式节点以自然语言 `agent.ask` 调用公开 Actor 能力，对 `https://joinbytedance.com/search` 创建 v4 Probe：

- Mission：version 5，operations_used 4；
- Probe：completed，version 2；
- Work：completed，resolution `succeeded`；
- Attempt：`attempt-e61743d6b76698dfdb6aa548b279bc79`，status `succeeded`；
- final URL：`https://joinbytedance.com/search`；
- 规范链接：29；
- response Artifact：`artifact-cf5998fbfedf7c2875cd4c1aa0839dd2`；
- trace Artifact：`artifact-fa97020f5f2a44578cd1bf11c8a3348e`；
- attestation：仅观察 GET/OPTIONS，一次文档导航，public endpoint/robots 均通过，允许写请求、表单、下载、弹窗和跨源文档导航均为 0；
- 所有 Deep Discovery Browser Work 的活动 Permit 数为 0。

第一次 canary 曾在业务结果已经提交后因 Executor 结果响应解码遗漏而误报失败。修复前遗留 Attempt 由既有过期恢复机制收口，原 Work 产生新 Attempt 并最终成功；没有删除记录、直接改状态或创建专用 Worker。第三次 canary 同时得到控制端 `execution.result` 和 Executor `execution.wake status=completed`，证明该协议缺陷已关闭。

完成审计发现 checkpoint 摘要曾被误写为 active/completed Mission 的 `waiting_reason`。模型现只允许 `waiting_human` 和 canceled 状态保留该原因；前向 migration 59 只从 active/completed Mission 的 JSON 状态移除错误字段，不删除图、checkpoint 或其他业务事实。正式库 migration 57—59 均为 `applied`，两个现存 Mission 的错误字段均已不存在。

## 自动验证

以下门已通过：

```text
go test -race ./drivers/tools/recruiting ./drivers/tools/recruiting/model
go test -race ./drivers/tools/recruiting/store ./drivers/tools/recruitingexecutor/...
go test -race ./lib/metatool
go vet ./drivers/tools/recruiting/... ./drivers/tools/recruitingexecutor/...
make recruiting-boundary-check
git diff --check
```

全仓 `go test ./...` 仍被基线已有的 `TestCoderunnerHumanJourney` 进程名/父子关系断言阻塞；在 detached `HEAD=30fa641b` 上独立复跑得到相同失败，本分支没有修改该测试或 coderunner。`make lint` 的 `web → drivers` 基线违规也在同一 detached HEAD 可复现。本验收不把这两个仓库级既有失败写成通过，也不为关闭招聘功能而修改 Atoll 核心。

机读摘要：`docs/experiments/evidence/recruiting-deep-discovery-v1-20260913.json`。
