# ADR：Atoll Recruiting MySQL 事实存储与事务边界

状态：已接受，P2 实现基线

日期：2026-09-07

依赖：产品设计 v0.8、开发验证计划 v1.0、P1 验收提交 `a94d2b8d`

## 决策

招聘事实使用独立 MySQL 8 schema，作为 Recruiting Actor 控制的 Resource。Atoll ledger、registry、timer 和身份系统继续使用原实现，不迁入 MySQL。数据库不是第二控制面：只有 Recruiting Actor/application 可以提交领域写入，Executor 只能通过 Atoll Message 返回结果。

表按三类组织：

1. 当前带版本聚合：Company、Source、Assignment、Checkpoint、SourceJob、DailyRun、Occurrence、Work、Attempt、Profile、BudgetPermit、RepairIncident；
2. append-only 证据：ListingObservation、JobDetailVersion、Artifact、OverrideVersion、领域事件 outbox；
3. 可恢复流程：command receipt、baseline generation/staging、repair affected Work。

聚合同时保存强类型索引列与 `state_json`。索引列用于唯一约束、CAS 和领取查询，JSON 用于无损恢复 P1 聚合；写入必须由 adapter 从同一个领域对象同时生成，禁止只修改 JSON 或只修改索引列。

## 唯一性与并发

- Company 名称不是唯一键；非空规范官网键唯一，冲突返回候选重复而不静默合并；
- Source 使用 `(company_id, canonical_source_key)` 唯一；`canonical_source_key` 另建普通索引用于发现跨 Company 冲突并转人工；
- 每个 `(source_id, recipe_kind)` 只有一个当前 Assignment，更新必须比较 `assignment_version`；
- Checkpoint 每 Source 一个当前头，使用 `version` CAS；
- SourceJob 使用 `(source_id, source_job_key)` 唯一，详情接受同时比较聚合 `version` 和 `refresh_generation`；
- DetailVersion 使用 `(job_id, detail_version)` 和 `(job_id, content_hash)` 唯一，重复内容不会产生新有效版本；
- SourceOccurrence 使用 `(source_id, schedule_date, schedule_policy_version)` 唯一；
- Work 的非空 `business_key` 唯一，`command_id` 不代替该约束；
- RepairIncident 使用 `repair_key` 唯一，使相同故障域、签名和失败版本单飞；
- command receipt 以 `command_id` 唯一，并保存 request hash；同 ID 不同请求拒绝；
- Artifact 以 ID 唯一、内容哈希建普通索引；相同内容可因权限、保留策略或 Work 血缘不同而有多个元数据记录。
- BudgetPermit 每 Attempt 唯一；`budget_usage` 为 global、capability、origin、company 和可选 profile 保存活动计数。领取按稳定维度顺序锁定计数行，容量判断、计数递增、Permit 和 Attempt 同事务提交；完成、失败或过期在原事务中递减，禁止用并发不安全的 `COUNT(*)` 后插入。

所有可修改聚合执行：

```sql
UPDATE ... SET version = version + 1, ...
WHERE id = ? AND version = ?
```

受影响行数为零时重新读取：目标不存在返回 not found，版本不同返回 version conflict，绝不盲重试为成功。

## 事务切点

### 普通命令

单一事务内完成：锁定/读取 command receipt → CAS 聚合 → 插入 append-only 证据（如有）→ 插入 outbox event intent → 保存稳定 receipt。Outbox intent 创建时冻结最大投递次数；失败以 expected attempts 做 CAS，记录错误分类和下次投递时间，达到上限进入 `exhausted`，不能无限重试。提交后再通过 Atoll ledger 发送事件；ledger 失败时 outbox 保留并可重放。接收方仍按 event ID 幂等，因此数据库提交与 ledger append 不要求分布式事务。

### 每日列表页

每页最多 500 项，使用小事务：验证当前 Work version/acceptance fence → 插入 Observation → 按来源岗位键收敛 SourceJob → 仅为新岗位或真正更新提升 generation → 插入唯一 Detail Work 意图 → 追加该页 Progress。页内事实、派生意图和 resume cursor 原子提交；同一页响应丢失可精确重放，进程退出则从最后一条 append-only Progress 继续。Checkpoint 不在普通分页事务中推进。

扫描到旧边界、完成重叠、排序契约成立且同时间组完整后，独立最终事务比较旧 Checkpoint version，写入新边界和 occurrence 结论并产生 outbox。失败或崩溃只会留下可重放 Observation/Work，不会产生虚假的新 Checkpoint。

Listing Page Progress 以 `(attempt_id, page_sequence)` 唯一，而不是以 Work 为分页序列边界；`work_id` 仅保留作业务查询和审计关联。旧 Attempt 已接受的页面与 Artifact 不删除，新 Attempt 必须从第 1 页建立自己的连续序列，completion 也只汇总本 Attempt 的页面。这样既允许页面事实幂等保留，也不会让执行器在页间崩溃后把旧 Attempt 的游标或条目数带入重试。

### 首次基线

`baseline_generations` 保存 generation 状态和 fencing version；`baseline_staging` 按 `(source_id, generation, source_job_key)` 分块幂等写入。游标失效可从头重扫。finalize 锁定 generation、核对数据库实际 staging 数、冻结 staging、改变 generation 可见性并建立首个 Checkpoint，不搬运一万行数据；详情 Job/Work 复用上述页提交协议按主键 seek 渐进物化，避免单个超大事务。

### Artifact 与执行结果

Executor 先上传外部对象，再返回不可变引用。接受事务验证 Attempt sender/incarnation 和全部 P1 fence，插入 Artifact metadata、业务事实、Attempt/Work 终态与 outbox。拒绝的迟到结果只允许保存隔离的 rejected Artifact 证据，不能更新聚合或 Checkpoint。

## 领取与索引

每日到期领取使用 `source_occurrences(status, due_at, occurrence_id)`；Work 使用 `(status, not_before, priority, deadline_at, work_id)`，并辅以 `(capability, status, not_before)`、`(origin, status, not_before)`、`(profile_id, status, not_before)`。实现可使用 `SELECT ... FOR UPDATE SKIP LOCKED`，但对外仍表达 execution offer/accept，不暴露数据库 lease 语义。

活动 Attempt 的普通无进展扫描使用 `(attempt_status, updated_at, attempt_id)`；较短 Permit 独立使用 `(permit_status, expires_at, attempt_id)` 找到期项。两条有界索引扫描在应用层去重，避免带跨表 `OR` 的全量扫描；二者都复用同一个 reconcile timer。

查询分页采用稳定 seek cursor `(updated_at, id)` 或业务对应的稳定复合键，不使用大 offset。所有日常扫描必须有 `EXPLAIN` 证据；测试拒绝关键查询 `type=ALL` 且无适用 key。

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
