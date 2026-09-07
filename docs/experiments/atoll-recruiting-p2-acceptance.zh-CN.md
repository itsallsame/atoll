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

- Source、Recipe/Assignment、Checkpoint、Job/Observation/Detail、Work/Attempt、DailyRun/Occurrence、Override、Profile、Budget、Repair、Artifact 和 outbox 的 Repository contract；
- 将已验证的命令 receipt/聚合/outbox 原子事务推广到其余修改命令，并实现 outbox 有界重试状态；
- 每日 Listing Observation/SourceJob/Detail Work 与既有 Checkpoint 的增量 CAS 恢复；baseline staging/finalize 已完成，详情渐进物化仍待实现；
- deadlock、timeout、断连、重复提交和进程 kill 故障注入；
- 10,000 条基线不使用超大事务已经验证；仍需所有关键领取查询的 EXPLAIN；
- 随机数据库连续 100 次 migration+contract，测试身份的 migration/runtime DDL/DML 权限拆分，以及残留 schema 核对。

P2 仍为进行中，不能以首个 Repository 切片替代完整退出门。
