# Atoll Recruiting P2 验收记录

状态：进行中，尚未通过退出门

日期：2026-09-08

P1 契约基线：`a94d2b8d`

## 已完成

- Schema ADR 冻结访问模式、唯一约束、CAS、事务切点、outbox 恢复、基线 staging 和权限边界；
- migration 从空库创建 26 张招聘业务表，migration runner 另建版本/checksum ledger；
- migration 使用 MySQL advisory lock 串行化，成功重跑幂等，checksum 改变拒绝启动，残留 `applying` 状态要求从升级前备份恢复；
- DSN 必须显式数据库和非 root 用户，adapter 强制 UTC、关闭 multi-statements，并设置有界连接池；
- Company Repository 已实现 create/get、规范官网并发唯一约束、`updated_at + company_id` seek pagination 和单版本 CAS；
- Source Repository 已实现 create/get、按 Company 或全局 seek pagination 和单版本 CAS；追加 migration `000002` 建立 `(company_id, updated_at, source_id)` 索引，游标绑定 Company selector，不能跨公司复用；
- 两个并发 Company 更新验证只有一个成功、另一个得到明确 VersionConflict；
- Company 修改命令把聚合 CAS、逐字节稳定 response receipt 和领域 event outbox 放在同一事务；相同 command ID 并发重放只生效一次，不同 request hash 被拒绝；
- Source add/update/validate/pause/resume/archive/restore 同样把聚合 CAS、稳定 response receipt 和 outbox 放在单一短事务；创建时在事务内锁定所属 Company 并拒绝 archived 状态，消除 Actor 预检与插入之间的竞态；已提交命令仍在 Company 后续归档后稳定重放；
- outbox 插入失败会同时回滚聚合与 receipt；数据库提交后即使尚未写入 Atoll ledger，pending event 仍可查询和补投；
- Baseline staging 固定每 500 条一个事务；10,000 行实测形成 20 个提交块，完整重扫后仍收敛为 10,000 个来源岗位键；
- baseline listing finalize 只 CAS generation 并原子建立首个 Checkpoint，不搬移或删除 staging；Checkpoint 冲突会回滚 generation 更新，不留下半完成状态；
- Checkpoint Repository 使用独立 `checkpoint_version` CAS；两个日常增量候选并发提交时实测只有一个边界生效，另一个得到当前版本冲突；
- 每条 ListingObservation、SourceJob 收敛和唯一 Detail Work 意图在同一短事务提交；完全重放只返回既有 Job，不重复写事实；
- 安全重叠会追加 Observation 证据但不提升 Job generation、不创建 Detail Work；活动时间/指纹变化才各提升一次；两个等价更新并发时只产生一个新 generation 和一个 Detail Work；
- Detail Work 写入故障会回滚同事务中的 Observation 与 Job 更新，重试不会看到半完成分页结果；
- Work Repository 支持业务键唯一、版本 CAS，以及按 capability/origin/Profile、`not_before`、deadline 和 priority 过滤排序的 runnable 查询；EXPLAIN 验证使用招聘专用索引；
- migration `000003` 为 Work 增加 initiator/message/work cause 列和专用索引；Work get 同时返回领域状态与 placement，人工创建、暂停、恢复、取消、结案和 retry 均采用 receipt/聚合或新 Work/outbox 单事务；失败创建不留下 receipt；
- retry 事务锁定并重读原 Work 的版本和终态，验证 target/purpose/parent 因果一致后插入新 Work；对于 `listing_sync`，同一事务还锁定未终结 SourceOccurrence，以精确旧 Work/version 将其重绑到新 Work，再提交 receipt、event 和 execution dispatch。合同测试证明新 Work 可被 listing Offer 领取、旧 Work 不再解析为 occurrence；原 Work/Attempt 不更新，在途结果仍由原 acceptance fence 判定，终态 occurrence 不能重开；
- Attempt 保存 Executor identity/incarnation 与全部领域 fence；状态通过预期前态 CAS，两个并发 accept 只有一个成功，状态机无循环因此不产生 ABA；
- Recipe 使用 `(recipe_id, recipe_version)` 身份和独立 state version CAS；ABI、opaque content ref、transport、required capability、内容与 contract 在同一 Recipe version 内不可变，Repository 只接受领域状态机产生的状态转换；Source candidate→validating 单独持久化，ready Endpoint 与首个 Listing Assignment 同事务发布；
- Recipe rollout 要求匹配 active kind/contract，并在同一事务比较 Source version 与 Assignment version；两个并发 rollout 只有一个成功，任一 CAS 冲突都会回滚另一侧，数据库不会出现 Source JSON 与 Assignment 行不一致；
- Detail result 接受会从数据库重读 Company、Source、Detail Assignment/Recipe、Job refresh generation、Profile（如有）、Work acceptance version、BudgetPermit 和 Attempt Executor incarnation；不信任结果消息声明的“当前版本”。Listing Checkpoint 不标识详情代际，因此不参与 detail fence；
- Listing page 接受同样重读 Attempt/Work/Occurrence 和全部当前 fence，每页最多 500 项；Artifact、Observation、由当前 detail Recipe 与详情 URL 派生的 detail Work、顺序恢复点在一个事务内提交。Executor 不能指定 detail capability/origin，也不能指定 Job/Work identity；这些值由控制面 Recipe、规范 URL 和稳定业务键生成；
- Listing completion 只接受已提交 terminal page 和完整 identity/pagination/order/frontier/same-time/overlap 证明；Executor 只能提出新 frontier 时间/键，Source、Recipe、contract、策略、overlap、occurrence 和版本均由当前 Checkpoint 与冻结 occurrence 重建；Checkpoint、Attempt succeeded、Work completed、Occurrence completed 和 outbox 在一个事务内提交；
- migration `000010` 将 Listing Page Progress 的顺序与唯一性改为 Attempt 作用域，同时保留 Work 索引用于审计。MySQL crash/retry 合同已证明：Attempt A 接受部分页面后失败，Attempt B 可从第 1 页重新开始，两个 Attempt 的证据均保留，且 B 的 completion 只汇总 B 的页面；
- accepted completion outcome 随 Attempt 保存，后续 Checkpoint 再次推进后重放旧结果仍返回第一次接受的稳定快照；错误 incarnation、变更后的 Source 或不完整质量证明只保存 rejected Artifact，不产生 Observation、恢复点或 Checkpoint 变化；
- stale Attempt 领取使用 `(attempt_status, updated_at, attempt_id)` 索引和最多 500 条候选；每个 Attempt 以独立短事务按主键 `SKIP LOCKED` 恢复。并发恢复同一 running Attempt 只有一次进入 expired、一次将 Work 置为 `waiting_retry`，并原子追加唯一 outbox 事件；offered/accepted 过期只释放活动槽，不伪造已开始执行；
- 合法详情结果在一个事务内提交 Artifact、SourceJob、append-only JobDetailVersion、Attempt succeeded 和 Work completed；同 Artifact/Attempt 重放返回既有 Job，不增加详情版本；
- 错误 Executor incarnation 或已变化 Source version 的详情结果只新增 `rejected=true` Artifact，Job、Work 和 Attempt 均保持原状态；
- Profile Repository 只持久化 opaque secret reference，并以 profile version CAS 驱动 repairing/verifying；测试确认没有 Cookie、密码或 OTP 字段；
- BudgetPermit 以 Attempt 唯一并使用版本 CAS，只能从 granted 进入一个终态；migration `000009` 新增 global/capability/origin/company/profile 活动计数和 Permit 到期索引。计数行、Permit 和 Attempt 同事务取得，完成/失败/超时同事务释放；origin 上限为 1 的并发测试没有超发，Permit 到期即使 Attempt 尚未达到普通 stale 阈值也会由同一 reconcile 回收；
- RepairIncident 以 failure domain/signature/failing version 形成的 repair key 单飞；相同 origin 故障的多个 Work 只形成一个 incident 和多条幂等 affected-work 关联；
- DailyRun 按 schedule date 唯一并冻结调度策略版本、截点、执行窗口和 expected sources；普通启动使用版本 CAS，两个并发启动实测只有一个成功，全部计划字段在创建后不可修改；
- 可信日切路径在一个 Repeatable Read 事务中读取 Company/Source/Listing Assignment/active Recipe，使用领域 `EligibleForDailyRun` 再验证四维增量契约，并原子写入 running DailyRun、全部轻量 SourceOccurrence 和 `daily_run.started` outbox；`expected_sources` 直接取该快照行数，不接受外部调用方声明；
- SourceOccurrence 以 `(source_id, schedule_date, schedule_policy_version)` 唯一，ID 与 due time 由 Source ID、日期和策略版本确定性生成并均匀散列到执行窗口；同日同配置重放返回原名单，改变 ID、策略或窗口被拒绝；两个并发日切调用实测为一次创建、一次重放且只有一个 outbox；
- 旧的分块 `MaterializeOccurrences` 仅保留给显式非日切组装路径，并已加强为校验 DailyRun 策略、窗口、Occurrence JSON due time、数据库 due time 和全部不可变快照；不得将外部部分集合通过该 API 冒充可信每日截点；
- 每个 SourceOccurrence 冻结 Company/Source/调度策略版本、Endpoint、Assignment、Recipe/内容哈希、ABI/transport/capability/origin 与 due time，后续 Source 更新不会改写当日执行输入；planned occurrence 可由操作员显式排除并记录原因，绑定 Work 后不能绕过 Work fencing 直接排除；
- migration `000005` 建立 occurrence→listing Work 的唯一外键。到期物化每批最多 500 条，以 `FOR UPDATE SKIP LOCKED` 只锁 occurrence 行，并在同一事务创建 capability/origin 路由的 Work、推进 occurrence 为 queued、写入 outbox；窗口已经结束的 planned occurrence 直接形成明确异常，不创建 deadline 已失效的 Work；
- `run.join_occurrence` Repository 合同以 occurrence expected version 锁定一个 planned 项，验证所属 DailyRun 仍 running、业务时间早于冻结窗口末且 Work placement 完全来自 occurrence 快照；Work、queued occurrence、稳定 receipt、业务 event 和定向 execution dispatch 同事务提交。重放返回原响应，陈旧版本不留下 receipt，新 Work 可被统一 Listing Offer 领取；
- 并发实测曾发现把 DailyRun JOIN 进 locking read 会锁住所有 occurrence 共享的父行、令多实例串行；修正为只锁 occurrence、随后只读校验 DailyRun 后，两个实例连续三轮都各自领取不同项，最终 Work 无重复；
- 到期 occurrence 查询有界为 500 条，`EXPLAIN FORMAT=JSON` 验证使用 `(status, due_at, occurrence_id)` 索引；
- DailyRun 只能用数据库内不可变 occurrence 终态事实闭账：总数必须等于 cutoff 时 expected sources，成功、异常、排除统计必须逐项一致，未完成项或伪造 summary 都不能关闭日批次；物化和闭账锁定同一 DailyRun 行，避免关闭后晚插任务的竞态；
- CuratedOverride 每次人工创建、替换或撤销都先追加不可变 version，再在同一事务用精确旧 head 做 CAS；两个并发替换只有一个成为当前值，失败候选不会残留孤立 version；
- Override head 不删除历史来源事实；后续 verified detail 即使版本更高仍由有效人工值覆盖，撤销版本发布后才重新显露 verified detail；历史查询有界且 `EXPLAIN FORMAT=JSON` 验证使用 target/field 专用索引；
- Outbox event 创建时冻结最大投递次数；失败按 expected attempts 做 CAS，持久化错误分类和 `next_attempt_at`，达到上限进入 `exhausted` 并退出 runnable 集合；
- Outbox 到期查询有界为 500 条且 `EXPLAIN FORMAT=JSON` 验证使用 pending 索引；重复成功确认幂等，迟到的真实成功可把 exhausted event 更正为 delivered，未知 event 不被伪装成成功；
- migration `000011` 增加 execution dispatch outbox 及 pending/target 索引。稳定 dispatch ID 的精确重放幂等、改内容冲突；到期查询只返回 pending 且使用有界批次，投递尝试以 expected-attempt CAS 递增并最终 exhausted，只有记录绑定的 authenticated Executor Actor ID 可以确认 delivered，迟到确认仍可纠正 exhausted；
- 到期 SourceOccurrence 物化可在创建 Work/推进 occurrence 的同一事务中按 capability 和 fleet 实例数写入初始 dispatch；人工 listing/detail Work create/retry，以及 Attempt 成功、失败释放容量和未来 retry 到期也与各自领域事务原子写入 dispatch。故意提供不匹配 placement 的 dispatch 时，Work、receipt、event 和 dispatch 全部回滚；
- Listing 页提交把 Work version/acceptance fence、最多 500 条 Observation/Job/Detail Work 和 append-only Progress 放入同一事务；失败页不留下 Job 或 resume cursor，同页确认丢失可精确重放，暂停后的旧执行者不能推进进度，恢复后的新 fence 可继续；
- Baseline staging 每块锁定 generation，finalize 后不可修改；finalize 在同一锁内校验数据库实际 staging 数与 `details_expected`，消除并发晚写和伪造计数；
- 10,000 条 baseline staging 通过主键 seek 形成 20 个页事务，实测恰好产生 10,000 个 SourceJob 和 10,000 个唯一 Detail Work；`EXPLAIN FORMAT=JSON` 验证 seek 使用复合主键；
- 真实 MySQL 行锁 timeout 会返回 deadline 且不留下 Company 半更新；压力分片曾暴露 autocommit UPDATE 的取消竞态：客户端先收到 deadline，释放测试锁后服务端仍可能在连接关闭前提交。Company CAS 已改为显式事务，超时连接只能回滚；同一 fault test 在 4 个独立 MySQL 容器中累计 100/100 次通过，随后完整 contract 单轮通过；
- 子测试进程在未提交事务中被 OS kill，连接断开后 MySQL 回滚；在命令事务已提交但客户端尚未确认时被 kill，重启后稳定 receipt 可重放且 pending outbox 仍可找回；
- MySQL 测试身份拆为非 root `staircase_migrator` 与 `staircase_runtime`；migration binary 只用前者，全部 Repository contract 使用后者，实测 runtime 具备所需 DML 且执行 DDL 被数据库拒绝；
- harness 使用进程号与随机数命名一次性 schema，支持 `RECRUITING_MYSQL_ITERATIONS=N` 和无 eval 的 `RECRUITING_MYSQL_TEST_RUN` 定向回归；stress 入口把总轮数精确分配给多个独立容器/schema/非 root 账号，并逐 shard 校验终态成功标记；commit `0af563b3f7d2` 已完成 4×25＝100 轮完整 migration+25-test contract，另完成 100 轮 timeout fault 定向回归；所有容器及其随机 schema 随后整体删除；
- 完整 100 轮的四份 runtime log 各含 25 个成功结果和最终 schema/identity 标记；紧凑证据及日志 SHA-256 保存在 `docs/experiments/evidence/recruiting-mysql-stress-0af563b3.json`，原始日志留在 ignored `.cache`，不把一次性数据库输出提交到 Git；
- `EXPLAIN FORMAT=JSON` 验证 Company seek 和按 Company 的 Source seek 查询使用专用索引；
- Job list 固定按 Source seek pagination，DailyRun list 按 schedule date/ID seek，Occurrence drill-down 按 DailyRun/ID seek；游标绑定父 selector 且严格拒绝未知字段、尾随 JSON 和跨父对象复用，三个查询均有专用索引的 `EXPLAIN FORMAT=JSON` 证据；
- DailyRun 实时摘要以单条 LEFT JOIN/GROUP BY 快照同时读取运行状态与各 Occurrence 状态计数，避免分两次查询时物化或闭账并发导致自相矛盾；显式返回 expected/materialized/missing/planned/queued/running/completed/exceptions/excluded；
- migration `000012` 增加 Work `(status, deadline_at, work_id)` 运维索引。System/Capacity Repository 在 Repeatable Read 只读事务中形成同一时点快照，分别返回状态计数、runnable/最老等待/deadline miss、双 outbox backlog，以及最多 100 个活动预算和每类最多 100 个 capability/origin/Profile runnable 分组；分组输入按 `open`/`waiting_retry` 各最多扫描 5,000 条最老 runnable，并返回 exact/truncated 证明，避免有限输出背后的无界 GROUP BY。合同用 `EXPLAIN FORMAT=JSON` 证明 deadline 与 capacity scan 各命中专用索引；
- `make recruiting-mysql-test` 启动一次性 MySQL 8.4，以随机 schema 和非 root `staircase` 测试账号运行 race 集成测试，退出后删除整个测试容器，不连接共享数据库。
- migration 30 的 Recipe rollout batch Repository 已实现父控制 Work/批次原子创建、最多 500 项的连续预览分块、数据库事实重算 preview hash、成员 seek 分页、确认启动和取消；migration 31 以追加升级补齐不可变 schema/policy version 并将其纳入 hash，不修改历史 migration checksum。预览逐项锁定并复核 Source/Assignment/当前及目标 Recipe，乱序或任一不兼容成员使整块回滚；完成时父 Work 与 Batch 原子进入 `waiting_human(preview_ready)`/`previewed`。确认不在单个事务修改所有 Source，避免 20K 成员热锁；隔离非 root MySQL 8.4 合同覆盖命令重放、原子拒绝、分页和终态活动键释放。

- migration 13 为 Work Center 增加全局/status/purpose/trigger 的稳定倒序分页索引；Repository 支持状态、purpose、trigger、`waiting_human` 原因、Target、initiator 和更新时间区间组合筛选，游标绑定完整 selector 并返回 placement。MySQL 8.4 合同已验证跨页顺序、跨 selector 拒绝及全局/status `EXPLAIN` 命中预期索引；测试仍使用分离的非 root migrator/runtime 身份；

- migration 14 新增独立 `recruiting_listing_runs`；创建命令在同一事务中重检 Source/Company/Assignment/active Recipe/Checkpoint 快照，并原子写入 ListingRun、Work、receipt、event 和可选 dispatch。合同测试证明 diagnostic 完成只写 Artifact 和执行生命周期事实，岗位、Observation 与 Checkpoint 均不变化；

## 当前验证

```text
make recruiting-mysql-test
make recruiting-mysql-stress
go test -race ./drivers/tools/recruiting/...
go vet ./drivers/tools/recruiting/...
make build-go
./scripts/recruiting-boundary-check.sh a94d2b8d
```

## 尚未完成

- 其余修改命令的 receipt/聚合/outbox 原子编排随 P3 Actor command handler 实现；P2 已用 Company、Source、Work 命令纵向证明事务模板，并完成全部 Resource 纵向合同；
- 10,000 条基线不使用超大事务已经验证；仍需所有关键领取查询的 EXPLAIN；
- 首次 100 轮压力运行暴露的 timeout/autocommit 竞态已修复；修复提交上的完整 100 轮与容器残留核对均已通过，不再列为未完成项。

P2 仍为进行中，不能以首个 Repository 切片替代完整退出门。
