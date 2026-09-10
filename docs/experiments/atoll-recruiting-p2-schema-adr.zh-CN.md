# ADR：Atoll Recruiting MySQL 事实存储与事务边界

状态：已接受，P2 实现基线

日期：2026-09-07

依赖：产品设计 v0.8、开发验证计划 v1.0、P1 验收提交 `a94d2b8d`

## 决策

招聘事实使用独立 MySQL 8 schema，作为 Recruiting Actor 控制的 Resource。Atoll ledger、registry、timer 和身份系统继续使用原实现，不迁入 MySQL。数据库不是第二控制面：只有 Recruiting Actor/application 可以提交领域写入，Executor 只能通过 Atoll Message 返回结果。

表按三类组织：

1. 当前带版本聚合：Company、Source、Assignment、Checkpoint、SourceJob、DailyRun、Occurrence、Work、Attempt、Profile、BudgetPermit、RepairIncident；
2. append-only 证据：ListingObservation、JobDetailVersion、Artifact、OverrideVersion、领域事件 outbox；
3. 可恢复流程：command receipt、baseline generation/staging、repair affected Work、execution dispatch outbox。

聚合同时保存强类型索引列与 `state_json`。索引列用于唯一约束、CAS 和领取查询，JSON 用于无损恢复 P1 聚合；写入必须由 adapter 从同一个领域对象同时生成，禁止只修改 JSON 或只修改索引列。

## 唯一性与并发

- Company 名称不是唯一键；非空规范官网键唯一，冲突返回候选重复而不静默合并；
- Source 使用 `(company_id, canonical_source_key)` 唯一；`canonical_source_key` 另建普通索引用于发现跨 Company 冲突并转人工；
- 每个 `(source_id, recipe_kind)` 只有一个当前 Assignment，更新必须比较 `assignment_version`；
- Checkpoint 每 Source 一个当前头，使用 `version` CAS；
- SourceJob 使用 `(source_id, source_job_key)` 唯一，详情接受同时比较聚合 `version` 和 `refresh_generation`；
- DetailVersion 使用 `(job_id, detail_version)` 和 `(job_id, content_hash)` 唯一，重复内容不会产生新有效版本；
- SourceOccurrence 使用 `(source_id, schedule_date, schedule_policy_version)` 唯一；
- 日报关闭后的补跑不修改原 SourceOccurrence 或 DailyRun；production ListingRun 可携带 `recovery_of_occurrence_id`，该外键全局唯一，使一个未覆盖 occurrence 只有一条可重试的补偿谱系；
- Work 的非空 `business_key` 唯一，`command_id` 不代替该约束；
- RepairIncident 使用 `repair_key` 唯一，使相同故障域、签名和失败版本单飞；
- command receipt 以 `command_id` 唯一，并保存 request hash；同 ID 不同请求拒绝；
- Artifact 以 ID 唯一、内容哈希建普通索引；相同内容可因权限、保留策略或 Work 血缘不同而有多个元数据记录。
- BudgetPermit 每 Attempt 唯一；`budget_usage` 为 global、capability、origin、company 和可选 profile 保存活动计数。领取按稳定维度顺序锁定计数行，容量判断、计数递增、Permit 和 Attempt 同事务提交；完成、失败或过期在原事务中递减，禁止用并发不安全的 `COUNT(*)` 后插入。
- execution dispatch 以稳定 `dispatch_id` 唯一，绑定唯一目标 Executor Actor 地址、capability、可选 origin/Profile、cause 和到期时间；相同 ID 改写任一字段必须冲突。目标可以是稳定两段地址或具体三段成员，Atoll 负责投递解析；状态只允许 `pending|delivered|exhausted`，投递次数以 CAS 更新，只有与所存目标匹配的 authenticated 三段 Executor sender 才能确认完成。

所有可修改聚合执行：

```sql
UPDATE ... SET version = version + 1, ...
WHERE id = ? AND version = ?
```

受影响行数为零时重新读取：目标不存在返回 not found，版本不同返回 version conflict，绝不盲重试为成功。

## 事务切点

### 普通命令

单一事务内完成：锁定/读取 command receipt → CAS 聚合 → 插入 append-only 证据（如有）→ 插入 outbox event intent → 保存稳定 receipt。Outbox intent 创建时冻结最大投递次数；失败以 expected attempts 做 CAS，记录错误分类和下次投递时间，达到上限进入 `exhausted`，不能无限重试。提交后再通过 Atoll ledger 发送事件；ledger 失败时 outbox 保留并可重放。接收方仍按 event ID 幂等，因此数据库提交与 ledger append 不要求分布式事务。

可执行 Work 的创建与对应 execution dispatch intent 必须在同一事务提交；Attempt 成功、分类失败及未来重试到期同样在关闭执行权的事务内写入后续 dispatch。Recruiting Actor 使用已有 reconcile timer 按 `(delivery_status, next_attempt_at, dispatch_id)` 有界扫描并发送一次 wake，发送后等待目标 Executor completion acknowledgement；进程在发送与确认之间退出时允许重新投递。dispatch identity 稳定，但不同 delivery attempt 的消息和 offer command identity 必须不同；Attempt 唯一约束与结果 receipt 保证已经执行的业务不重复生效，新 offer 则能观察到当前已无 Work 并用 idle completion 收口。该 outbox 是招聘应用的可恢复流程事实，不修改 Atoll ledger、timer 或 Message 语义。

### 每日列表页

每页最多 500 项，使用小事务：验证当前 Work version/acceptance fence → 插入 Observation → 按来源岗位键收敛 SourceJob → 仅为新岗位或真正更新提升 generation → 插入唯一 Detail Work 意图 → 追加该页 Progress。页内事实、派生意图和 resume cursor 原子提交；同一页响应丢失可精确重放，进程退出则从最后一条 append-only Progress 继续。Checkpoint 不在普通分页事务中推进。

扫描到旧边界、完成重叠、排序契约成立且同时间组完整后，独立最终事务比较旧 Checkpoint version，写入新边界和 occurrence 结论并产生 outbox。失败或崩溃只会留下可重放 Observation/Work，不会产生虚假的新 Checkpoint。

Listing Page Progress 以 `(attempt_id, page_sequence)` 唯一，而不是以 Work 为分页序列边界；`work_id` 仅保留作业务查询和审计关联。旧 Attempt 已接受的页面与 Artifact 不删除，新 Attempt 必须从第 1 页建立自己的连续序列，completion 也只汇总本 Attempt 的页面。这样既允许页面事实幂等保留，也不会让执行器在页间崩溃后把旧 Attempt 的游标或条目数带入重试。

### 日报补偿

DailyRun 与 SourceOccurrence 在窗口关闭后不可改写。需要补跑时，用户创建普通 production ListingRun，并把 `recovery_of_occurrence_id` 冻结进其执行上下文；创建事务反查该 occurrence 属于同一 Source、结论为 exception/excluded，且 DailyRun 已关闭。migration 21 对该关联建立外键和唯一键，因此失败后的机器重试继续复用同一 ListingRun/Work 谱系，并发命令不能为同一缺口制造两个补偿头。只有 ListingRun 成功提交新 Checkpoint 后，查询投影才把它计入 `recovered` 并原子追加 `daily_occurrence.recovered` 事件；原日报的 `uncovered`、summary 和 occurrence outcome 保持原值。

### 共享故障单飞

migration 22 让失败 Work 以 `blocked_by_repair_work_id` 自引用同一张 Work 表，并让 RepairIncident 唯一关联一个确定性 Repair Work。故障域不是 Executor 输入：控制面根据冻结的 Attempt、Work placement、Recipe/Profile/refresh/policy 版本推导 `origin|recipe_version|profile|single_target` 及 failing version；Executor 只可提供不超过 191 字节、字符受限的低基数 `failure_signature`，旧版本缺省时退化为受控 failure class，原始错误、URL 和响应正文不能进入单飞键。

受影响成员的权威集合是 `recruiting_repair_affected_works`，Incident JSON 只保留首次种子，不随 1,000/10,000 个失败 Work 膨胀或反复重写热点行。首个转入 `waiting_human` 的失败事务原子创建 Incident、Repair Work、成员关联、Work 阻塞指针和一条 `repair.opened` 聚合事件；后续失败只增加幂等成员关联，瞬态退避不创建人工修复。Incident→Repair Work 不建立反向外键，以避免单飞头和自引用 Work 的循环插入顺序；Repository 对确定性身份做内容校验，Work→Repair Work 的阻塞关系仍由数据库外键保证。单 Recruiting Actor 的 Repository 对相同 repair key 使用有界哈希锁消除本进程热点死锁，多进程竞争继续由数据库唯一键和 context 有界的 1213/1205 事务重试收敛。

migration 23 为 RepairIncident 的无状态筛选增加 `(updated_at, incident_id)` 索引；已有 `(repair_status, updated_at, incident_id)` 索引服务按状态筛选。公开的 `recruiting.repair.list/get` 只返回有界投影：Incident JSON 中的种子 `affected_work_ids` 不作为成员清单输出，成员总数和待人工数由规范化关系计算，具体 Work 按 `incident_id + work_id` seek 分页。Repair Work 也通过 Actor 查询投影返回，运营端不直连数据库。列表游标绑定 repair status，成员游标绑定 incident ID，不能跨筛选条件复用。

migration 24 增加 `validation_work_id`、`recovered_work_count` 和可空唯一 `active_repair_key`。`repair_key` 是跨历史的故障分组事实，不再全局唯一；只有 open/validating 行持有 `active_repair_key`，resolve 事务将其置空，所以相同签名和失败版本在修复后再次回归会创建新 Incident，而并发回归仍只有一个活动 Incident。首代确定性 Incident ID 与 Repair Work ID 保持兼容；若该 ID 已属于关闭历史，新回归使用命令因果派生的新 ID，并仍由活动键竞争收敛。

`recruiting.repair.validation.begin` 只接受已经由 Executor 完成且 `resolution=succeeded` 的因果 Retry Work，Repository 反查其 `cause_work_id` 必须属于规范化 affected 集合；运营员不能凭文字宣称验证成功。该事务同时把唯一 Repair Work 从 open 推进为 running，使 Work Center 与 Incident 的 validating 投影一致。`repair.resolve` 再次锁定同一验证 Work，在一个事务解决 Incident、释放活动键并把 Repair Work 从 running 完成为 succeeded。`repair.recover` 每个版本栅栏命令最多选择 100 个仍为 `waiting_human` 且阻塞指针仍指向该 Repair Work 的成员，按 Work ID 锁定并恢复为 open；失败 Attempt 和分类元数据不改写。每个 capability 至多创建一个无 origin/Profile 偏置的初始 wake，后续领取仍经过原有 origin/company/profile 预算，避免按积压成员数同时发送控制消息。

### 首次基线

`baseline_generations` 保存 generation 状态和 fencing version；`baseline_staging` 按 `(source_id, generation, source_job_key)` 分块幂等写入，并为每行标记产生它的 `attempt_id`。失败 Attempt 的行不删除，可用于诊断；新 Attempt 必须从第 1 页重扫，同键行会改绑到新 Attempt，旧 Attempt 独有键保持隔离。finalize 只统计当前成功 Attempt 的行，并把该 `listing_attempt_id` 冻结到 generation；后续物化也只读取这个 Attempt 的 staging，因此旧页无法混入基线。游标失效可从头重扫。finalize 锁定 generation、核对当前 Attempt 的实际 staging 数、改变 generation 可见性并建立首个 Checkpoint，不搬运一万行数据；详情 Job/Work 复用上述页提交协议按主键 seek 渐进物化，避免单个超大事务。旧 Attempt staging 的有界保留/清理服从 Artifact 与运行证据保留策略，不进入成功事实热路径。

每个物化出的 baseline 详情成员保存当前负责它的 `detail_work_id` 和独立核算版本。详情失败后若用户不接受缺口而选择修复，关闭旧 Work 不改变 `pending`；创建 Retry Work 的事务同时用旧 `detail_work_id + job_id + pending` CAS 重绑成员。只有重试结果成功或用户明确接受缺口时，才与 BaselineGeneration CAS 一起把成员推进为 `succeeded|accepted_gap`。这样 Work 历史保持不可变，成员又不会错误指向已经终止的执行权。

### Artifact 与执行结果

Executor 先上传外部对象，再返回不可变引用。接受事务验证 Attempt sender/incarnation 和全部 P1 fence，插入 Artifact metadata、业务事实、Attempt/Work 终态与 outbox。拒绝的迟到结果只允许保存隔离的 rejected Artifact 证据，不能更新聚合或 Checkpoint。

## 领取与索引

每日到期领取使用 `source_occurrences(status, due_at, occurrence_id)`；Work 使用 `(status, not_before, priority, deadline_at, work_id)`，并辅以 `(capability, status, not_before)`、`(origin, status, not_before)`、`(profile_id, status, not_before)`；migration 12 增加 `(status, deadline_at, work_id)`，供运维快照统计未终态 deadline miss；execution dispatch 使用 `(delivery_status, next_attempt_at, dispatch_id)`。实现可使用 `SELECT ... FOR UPDATE SKIP LOCKED`，但对外仍表达 execution offer/accept，不暴露数据库 lease 语义。

活动 Attempt 的普通无进展扫描使用 `(attempt_status, updated_at, attempt_id)`；较短 Permit 独立使用 `(permit_status, expires_at, attempt_id)` 找到期项。两条有界索引扫描在应用层去重，避免带跨表 `OR` 的全量扫描；二者都复用同一个 reconcile timer。

查询分页采用稳定 seek cursor `(updated_at, id)` 或业务对应的稳定复合键，不使用大 offset。所有日常扫描必须有 `EXPLAIN` 证据；测试拒绝关键查询 `type=ALL` 且无适用 key。

migration 13 为 Work Center 增加全局 `(updated_at, work_id)` 以及 status、purpose、trigger 分别前置的倒序分页索引；Target 与 initiator 复用 migration 3 已有复合索引。查询按最具选择性的已建索引选择固定白名单中的 `FORCE INDEX`，其余组合条件为残余过滤。`waiting_reason` 首版只允许在 `waiting_human` 状态内使用 JSON 残余过滤，避免在尚无容量证据时复制一列可变诊断枚举；若该 Review Queue 证明成为高基数热点，再以独立 migration 投影索引列。

migration 14 新增 `recruiting_listing_runs`，保存独立人工列表运行的一对一 Work 关联、Source、run mode、状态、冻结 Checkpoint 版本和完整无秘密执行快照。该表不进入 DailyRun 外键或 coverage 分母；`work_id` 唯一约束防止一个 Work 获得多个运行语义，Source/创建时间索引用于后续运维下钻。诊断结果仍复用 Artifact、Attempt、Permit、receipt 和 outbox 表，不复制执行队列。

migration 30 为 Recipe 批量灰度增加 `recruiting_recipe_rollout_batches` 和规范化成员表。父批次持有唯一活动 `kind+scope` 键、目标 Recipe 不可变身份、输入 Artifact/hash、preview hash、canary/wave 大小、当前开放 ordinal 范围和版本；成员冻结 Source/Assignment 版本及切换前 Assignment，不在父 JSON 中嵌入无界 Source 列表。预览按每块至多 500 项提交并保存 `next_chunk_sequence`，完成后才写最终 source count/preview hash；Source ID 以 `sha256(batch_id|source_id)` 确定性排序，使 canary 样本不依赖上传行序，同时 preview hash 绑定该顺序和全部版本。确认后一次只开放 canary 或一个至多 500 项的 wave；未全部重新验证为 ready 前不扩大，任一失败暂停。父 Work 是 Recruiting Actor 推进的控制面事实，不进入 Executor runnable 队列；各 Source 的真实校准仍复用已有 validation Work 和唯一 Executor class。

migration 32 为成员增加应用时间、验证 Work/run 和验证完成时间的规范化证据列；migration 33 追加 `apply_requested_at`，形成可在进程退出后复用同一业务时间和命令 hash 的 `pending → applying → awaiting_validation` saga。成员 JSON 仍保存完整状态机，列投影只用于有界协调查询与外部事实关联。Listing 预览额外冻结切换前已验证的 Endpoint revision、`update_retop`、Checkpoint strategy/overlap 和 assessment version；运行时字段不进入 preview hash。成员 CAS 总是先锁父批次，再锁成员与 Source/Assignment，只有数据库中的 Assignment 已精确指向目标 Recipe、Source/Assignment 版本和生效时间一致时，才允许结束 applying。已经切换后验证失败的重试保持 applied fence，只重建验证，不重复产生 Assignment version；部分成员已经切换的批次不能用普通 cancel 遗留半发布状态，必须进入后续显式回滚流程。

## migration 与权限

- migration 从空 schema 开始，不识别、不导入也不删除 Staircase 旧表；
- migration 文件有固定 SHA-256，`recruiting_schema_migrations` 保存版本、名称、checksum 和完成时间；同版本 checksum 不同立即拒绝；
- migration identity 需要 DDL，运行 identity 只需要 DML；两者都不得是 root；
- 自动测试创建 `atoll_recruiting_test_<run-id>`，只删除本次创建的精确 schema；
- 无 destructive down migration。失败回滚为：停止新版本、恢复升级前备份、运行旧二进制；
- DSN 只从环境/秘密提供方取得，日志和错误必须脱敏。

## 时间与字符集

数据库会话固定 UTC；业务时刻用 `DATETIME(6)`，日程日期用 `DATE`。ID、hash、状态和 key 使用 ASCII/binary 比较；用户文本和 JSON 使用 `utf8mb4`。应用层继续接受/输出 RFC3339，不依赖数据库本地时区。

## 被否决方案

- 复用 Staircase 旧表：会继承旧状态机并形成第二控制面；
- 每个领域对象只存 JSON：无法可靠施加唯一键、CAS 和大规模领取索引；
- 每个字段完全拆表：首版事务和演进成本过高；
- 所有列表页与详情放进单事务：一万岗位基线不可恢复且锁范围过大；
- 数据库提交后直接“尽力而为”写 ledger：进程崩溃会永久丢失因果事件；
- 自动 down migration：当前无兼容降级承诺，破坏性逆向 DDL 比备份恢复风险更大。

## 验证要求

P2 必须以一次性 MySQL 8 schema 验证 migration checksum、重跑、CAS 竞争、稳定分页、事务截点恢复、baseline 分块与 finalize、runnable 索引、deadlock/断连/进程退出后的可判定结果。测试只使用专用非 root 用户，并在退出时核对无残留测试 schema。
