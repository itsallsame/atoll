# Atoll Recruiting P1 验收记录

状态：已通过

日期：2026-09-07

基线提交：`7255c6f7`（P0）

当前实现提交：`051233dc`、`a74c7fa7` 及其后续 P1 检查点

## 已实现事实

- Company 接入状态与 control 状态独立，支持带版本的暂停、恢复、归档和受控恢复；
- Company 与 Source 是一对多，Source 具有 candidate/active Endpoint、独立 readiness/control/health 以及版本化 Recipe Assignment；
- Listing Assignment 变更契约时必须提供 Checkpoint 兼容证明，否则拒绝复用旧边界；
- Recipe 中的排序/update-retop 声明只是假设，不再等同于事实。Source 发布必须携带绑定 Source、candidate Endpoint revision、Recipe/version 和 contract hash 的四维校准证据；identity、pagination、ordering、update-retop 全部为 `verified` 才能成为每日增量候选；
- 校准结论固定为 `verified|unverified|violated`，必须带版本、时间和有界且唯一的 Artifact ID；Recipe 实现或 Endpoint revision 改变后旧证据失配，Listing Recipe 替换会自动进入 repairing 并清除旧校准；
- Recipe、Profile、Work、Attempt、Checkpoint、SourceOccurrence、DailyRun、BudgetPermit 和 RepairIncident 均有显式状态转换与 CAS/fencing；
- Work 明确保存 `initiator_actor_id`、`cause_message_id` 和 `cause_work_id`；终态非成功 Work 的 retry 创建版本 1 的独立新 Work，旧终态不变，非终态或已成功 Work 不能被 retry；
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

当前 `go test -cover ./drivers/tools/recruiting/model` 的语句覆盖率为 79.0%。`make recruiting-model-test` 固化 race、四组 fuzz 和 75% 语句覆盖回退门。关键状态转换采用显式状态矩阵覆盖；语句覆盖率用于阻止整体测试质量回退，不代替矩阵断言。

## 退出门结论

- Company onboarding/control、Source readiness/control、Recipe、Profile、Work/Attempt、Checkpoint、DailyRun/Occurrence 和 RepairIncident 的合法/非法主状态组合均有矩阵测试；
- fuzz 合法事件序列证明终态 Work 单调、Source 不会在缺少生产 Endpoint/Assignment/匹配的四维校准证据或归档状态下进入每日运行；
- Attempt 对 Executor incarnation 以及 Company、Source、Assignment、Recipe、Checkpoint、refresh generation、Profile 的每一项变化均逐项拒绝；
- Source 结构最多持有一个当前 Listing/Detail/Discovery Assignment、一个 active Endpoint 和一个与当前 Listing 事实精确绑定的校准；领域层与 Repository 层都拒绝伪造的 ready Source；
- BaselineGeneration 明确区分 listing、details_pending、completed 和 completed_with_exceptions，列表 finalize 与详情逐步核算使用独立版本转换；
- 模型无网络、数据库和墙钟读取，业务时间均由调用方显式注入；
- 全部相关回归、构建和核心冻结检查通过。

P1 退出门通过，可以开始 P2 schema ADR、migration 和 Repository contract；P2 不得反向放宽上述不变量。
