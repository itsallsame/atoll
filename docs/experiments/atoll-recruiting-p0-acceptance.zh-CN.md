# Atoll Recruiting P0：扩展可行性验收记录

状态：通过

日期：2026-09-07

基线提交：`c9d251f5`

## 1. 本阶段证明的内容

P0 已用真实 Atoll server/daemon 进程证明招聘产品可以作为扩展接入，无需修改 Atoll 核心：

```text
Human request
→ server-placed Recruiting Actor
→ durable actor Resource 中创建 Probe Work
→ Atoll Message 投递给 daemon-placed recruiting-executor
→ Executor 以新 Message 提交结果
→ Recruiting Actor 校验 sender + attempt_id + expected work version
→ 持久化完成状态并发出聚合事件
```

这只是架构探针，不是假装已经实现 Company、Source、Recipe 或真实抓取。

## 2. 交付物

| 交付物 | 位置 | 证明 |
|---|---|---|
| 纯 Probe Work 状态机 | `drivers/tools/recruiting/model/` | Attempt 和版本 fencing 单元测试 |
| Recruiting Actor | `drivers/tools/recruiting/` | command receipt、Resource、Post、timer、result acceptance |
| 唯一 Executor class | `drivers/tools/recruitingexecutor/` | 同 class 不同 capability 实例 |
| actor 注册 | `drivers/tools/all/all.go` | 全 drivers 测试构建通过 |
| 黑盒旅程 | `e2e/recruiting_journey_test.go` | server/daemon、timer、重启、重放 |
| 核心冻结守卫 | `scripts/recruiting-boundary-check.sh` | 相对基线扫描冻结目录 |
| 公共契约基线 | `drivers/tools/recruiting/contract.go` | 版本、错误词和 cursor pagination |

## 3. 已通过验证

```bash
go test -race ./drivers/tools/recruiting/... ./drivers/tools/recruitingexecutor/...
go test ./drivers/tools/...
go test ./e2e -run '^TestRecruitingP0Journey$' -count=1 -v -timeout 180s
./scripts/recruiting-boundary-check.sh c9d251f5
git diff --check
```

黑盒测试具体证明：

- 两个 Executor 使用同一个 `recruiting-executor` class，以不同 identity/capability 同时在线；
- 用户命令返回 actor-owned Work，而不是 Executor 自己创建业务事实；
- Executor 结果由 Recruiting Actor 接受并转为 completed；
- 相同 `command_id` 返回第一次的稳定响应，不创建第二个 Work；
- durable timer 在 server 进程替换后仍触发并完成 Work；
- Recruiting Actor 的 Work 和 command receipt 在 server 替换后恢复。

## 4. 核心零改动证据

相对 `c9d251f5`，招聘变更没有修改：

```text
protocol/
runtime/
lib/
platform/
registry/
```

本阶段只增加/修改计划允许的 `drivers/tools/recruiting*`、actor assembly import、`e2e/`、`scripts/`、`Makefile` 和招聘文档。

## 5. 已知基线问题

全仓 `go test ./archtest/` 当前仍失败，原因是本分支开始招聘开发前已经存在的 `web/societyconsole/public.go → drivers/tools/society/model` 越层依赖。招聘新增包没有出现在违规列表。本阶段不修改该无关实现，也不放松 archtest；后续每次验证继续分别报告“招聘边界通过”和“全仓既有门失败”。

## 6. 下一阶段

进入 P1：实现 Company、Recruitment Source、SourceEndpoint/Assignment、SourceJob、Recipe、Checkpoint、SourceOccurrence、Daily Run、Work 和 Attempt 的纯领域模型与命令转换；先完成 S01、S03、S04、S15、S16 的状态机和性质测试，再接 MySQL schema ADR。
