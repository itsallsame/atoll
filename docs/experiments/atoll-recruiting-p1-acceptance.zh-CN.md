# Atoll Recruiting P1 验收记录

状态：候选，尚未通过退出门

日期：2026-09-07

基线提交：`7255c6f7`（P0）

当前实现提交：`051233dc`、`a74c7fa7` 及其后续 P1 检查点

## 已实现事实

- Company 接入状态与 control 状态独立，支持带版本的暂停、恢复、归档和受控恢复；
- Company 与 Source 是一对多，Source 具有 candidate/active Endpoint、独立 readiness/control/health 以及版本化 Recipe Assignment；
- Listing Assignment 变更契约时必须提供 Checkpoint 兼容证明，否则拒绝复用旧边界；
- Recipe、Profile、Work、Attempt、Checkpoint、SourceOccurrence、DailyRun、BudgetPermit 和 RepairIncident 均有显式状态转换与 CAS/fencing；
- Attempt 接受结果同时核对 Work acceptance version、Executor incarnation、Company、Source、Assignment、Recipe、Checkpoint、Job refresh generation 和 Profile 版本；
- ListingObservation 与 JobDetailVersion 保留 Recipe/Artifact 血缘，人工 Override 不覆盖底层证据；
- 每日安全重叠中的相同岗位不会提升 `refresh_generation`；只有活动时间、列表指纹或规范详情 URL 变化才派生详情刷新；
- 岗位不因列表缺失进入删除或下架状态；
- command receipt 只解决相同请求的传输重放；每日列表、详情、基线和共享修复另有稳定业务键；
- 手工运行固定为 `diagnostic|join_occurrence|production`，回填固定为 `artifact_recompute|live_refetch`；diagnostic 和回填都不能推进日常 Checkpoint；
- 批量取消逐子 Work 执行：已终态项保留，未终态项取消并提升 acceptance version，从而拒绝在途结果。

## 本次验证证据

以下命令在分支 `codex/atoll-recruiting` 运行通过：

```text
go test -race ./drivers/tools/recruiting/...
go test ./drivers/tools/...
go test ./e2e -run '^TestRecruitingP0Journey$' -count=1 -v -timeout 180s
go vet ./drivers/tools/recruiting/... ./drivers/tools/recruitingexecutor/...
make build-go
./scripts/recruiting-boundary-check.sh a74c7fa7
```

性质测试曾发现并保存 URL 规范化反例 `HTTPS://00000000000::`；修复后连续 fuzz 通过。反例保存在：

```text
drivers/tools/recruiting/model/testdata/fuzz/FuzzCanonicalHTTPURLIdempotent/8a331a9ab9cd6bd5
```

当前 `go test -cover ./drivers/tools/recruiting/model` 的语句覆盖率为 76.8%。`make recruiting-model-test` 固化 race、四组 fuzz 和 75% 语句覆盖回退门。该数字只作趋势证据，不冒充计划要求的关键状态转换分支全覆盖。

## 未通过项

P1 暂不标记完成，原因是：

1. 需要补齐所有关键状态机的合法/非法转换矩阵，尤其是 Company、Source、Recipe、Profile、Work/Attempt、Checkpoint、DailyRun 和 RepairIncident；
2. 已建立随机事件序列性质测试；还需扩展到 Company/Recipe/Profile/Checkpoint/DailyRun/Repair 全状态组合，并证明旧 Attempt 在任意版本变化下均被拒绝；
3. 完成后再次运行全仓相关回归和核心冻结检查。

因此 P2 MySQL schema/migration 尚未开始，避免在领域契约仍可能变化时固化数据库结构。
