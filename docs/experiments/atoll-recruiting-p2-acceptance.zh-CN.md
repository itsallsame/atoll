# Atoll Recruiting P2 验收记录

状态：进行中，尚未通过退出门

日期：2026-09-07

P1 契约基线：`a94d2b8d`

## 已完成

- Schema ADR 冻结访问模式、唯一约束、CAS、事务切点、outbox 恢复、基线 staging 和权限边界；
- 首版 migration 从空库创建 23 张招聘业务表，migration runner 另建版本/checksum ledger；
- migration 使用 MySQL advisory lock 串行化，成功重跑幂等，checksum 改变拒绝启动，残留 `applying` 状态要求从升级前备份恢复；
- DSN 必须显式数据库和非 root 用户，adapter 强制 UTC、关闭 multi-statements，并设置有界连接池；
- Company Repository 已实现 create/get、规范官网并发唯一约束、`updated_at + company_id` seek pagination 和单版本 CAS；
- 两个并发 Company 更新验证只有一个成功、另一个得到明确 VersionConflict；
- Company 修改命令把聚合 CAS、逐字节稳定 response receipt 和领域 event outbox 放在同一事务；相同 command ID 并发重放只生效一次，不同 request hash 被拒绝；
- outbox 插入失败会同时回滚聚合与 receipt；数据库提交后即使尚未写入 Atoll ledger，pending event 仍可查询和补投；
- Baseline staging 固定每 500 条一个事务；10,000 行实测形成 20 个提交块，完整重扫后仍收敛为 10,000 个来源岗位键；
- baseline listing finalize 只 CAS generation 并原子建立首个 Checkpoint，不搬移或删除 staging；Checkpoint 冲突会回滚 generation 更新，不留下半完成状态；
- Checkpoint Repository 使用独立 `checkpoint_version` CAS；两个日常增量候选并发提交时实测只有一个边界生效，另一个得到当前版本冲突；
- 每条 ListingObservation、SourceJob 收敛和唯一 Detail Work 意图在同一短事务提交；完全重放只返回既有 Job，不重复写事实；
- 安全重叠会追加 Observation 证据但不提升 Job generation、不创建 Detail Work；活动时间/指纹变化才各提升一次；两个等价更新并发时只产生一个新 generation 和一个 Detail Work；
- Detail Work 写入故障会回滚同事务中的 Observation 与 Job 更新，重试不会看到半完成分页结果；
- Work Repository 支持业务键唯一、版本 CAS，以及按 capability/origin/Profile、`not_before`、deadline 和 priority 过滤排序的 runnable 查询；EXPLAIN 验证使用招聘专用索引；
- Attempt 保存 Executor identity/incarnation 与全部领域 fence；状态通过预期前态 CAS，两个并发 accept 只有一个成功，状态机无循环因此不产生 ABA；
- Recipe 使用 `(recipe_id, recipe_version)` 身份和独立 state version CAS；Source candidate→validating 单独持久化，ready Endpoint 与首个 Listing Assignment 同事务发布；
- Recipe rollout 要求匹配 active kind/contract，并在同一事务比较 Source version 与 Assignment version；两个并发 rollout 只有一个成功，任一 CAS 冲突都会回滚另一侧，数据库不会出现 Source JSON 与 Assignment 行不一致；
- Detail result 接受会从数据库重读 Company、Source、Detail Assignment/Recipe、Checkpoint、Job refresh generation、Profile（如有）、Work acceptance version 和 Attempt Executor incarnation；不信任结果消息声明的“当前版本”；
- 合法详情结果在一个事务内提交 Artifact、SourceJob、append-only JobDetailVersion、Attempt succeeded 和 Work completed；同 Artifact/Attempt 重放返回既有 Job，不增加详情版本；
- 错误 Executor incarnation 或已变化 Source version 的详情结果只新增 `rejected=true` Artifact，Job、Work 和 Attempt 均保持原状态；
- Profile Repository 只持久化 opaque secret reference，并以 profile version CAS 驱动 repairing/verifying；测试确认没有 Cookie、密码或 OTP 字段；
- BudgetPermit 以 Attempt 唯一并使用版本 CAS，只能从 granted 进入一个终态；两个并发 release 只有一个成功；
- RepairIncident 以 failure domain/signature/failing version 形成的 repair key 单飞；相同 origin 故障的多个 Work 只形成一个 incident 和多条幂等 affected-work 关联；
- DailyRun 按 schedule date 唯一，启动使用版本 CAS；两个并发启动实测只有一个成功，计划日期和期望 Source 数在创建后不可修改；
- SourceOccurrence 在 DailyRun 进入 running 后分块物化，并以 `(source_id, schedule_date, schedule_policy_version)` 唯一；相同输入重放保留原 ID，冲突 ID 或变化的快照被拒绝，物化总数不能超过 cutoff 时的 expected sources；
- 每个 SourceOccurrence 冻结 Company/Source/调度策略版本与 due time，后续 Source 更新不会改写当日执行口径；planned occurrence 可由操作员显式排除并记录原因；
- 到期 occurrence 查询有界为 500 条，`EXPLAIN FORMAT=JSON` 验证使用 `(status, due_at, occurrence_id)` 索引；
- DailyRun 只能用数据库内不可变 occurrence 终态事实闭账：总数必须等于 cutoff 时 expected sources，成功、异常、排除统计必须逐项一致，未完成项或伪造 summary 都不能关闭日批次；物化和闭账锁定同一 DailyRun 行，避免关闭后晚插任务的竞态；
- `EXPLAIN FORMAT=JSON` 验证 Company seek 查询使用专用索引；
- `make recruiting-mysql-test` 启动一次性 MySQL 8.4，以随机 schema 和非 root `staircase` 测试账号运行 race 集成测试，退出后删除整个测试容器，不连接共享数据库。

## 当前验证

```text
make recruiting-mysql-test
go test -race ./drivers/tools/recruiting/...
go vet ./drivers/tools/recruiting/...
make build-go
./scripts/recruiting-boundary-check.sh a94d2b8d
```

## 尚未完成

- Override 和 outbox 完整 Repository contract；Company、Source、Recipe/Assignment、Checkpoint、Listing/Detail Job、Observation/DetailVersion、Artifact、Work/Attempt、Profile、Budget、Repair、DailyRun/Occurrence 已有纵向合同；
- 将已验证的命令 receipt/聚合/outbox 原子事务推广到其余修改命令，并实现 outbox 有界重试状态；
- 每日 Listing Observation/SourceJob/Detail Work 的单项事务与并发收敛已完成；仍需分页进度/进程退出恢复以及 baseline 详情渐进物化；
- deadlock、timeout、断连、重复提交和进程 kill 故障注入；
- 10,000 条基线不使用超大事务已经验证；仍需所有关键领取查询的 EXPLAIN；
- 随机数据库连续 100 次 migration+contract，测试身份的 migration/runtime DDL/DML 权限拆分，以及残留 schema 核对。

P2 仍为进行中，不能以首个 Repository 切片替代完整退出门。
