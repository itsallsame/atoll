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
- 命令 receipt、领域更新和 outbox 的原子事务与 ledger 失败重放；
- 列表分页事务截点、Checkpoint CAS 恢复以及 baseline staging/finalize；
- deadlock、timeout、断连、重复提交和进程 kill 故障注入；
- 10,000 条基线不使用超大事务的证明与所有关键领取查询的 EXPLAIN；
- 随机数据库连续 100 次 migration+contract，测试身份的 migration/runtime DDL/DML 权限拆分，以及残留 schema 核对。

P2 仍为进行中，不能以首个 Repository 切片替代完整退出门。
