# Atoll Recruiting P3 验收记录

状态：进行中，尚未通过退出门

日期：2026-09-08

## 已完成

- Recruiting Actor 仍作为普通 Atoll extension 注册，未改动 `protocol/`、`runtime/`、`lib/`、`platform/`、`registry/`；
- MySQL DSN 不写入 Actor config 或消息，只配置环境变量名，运行时从环境读取非 root runtime DSN；
- 新增独立 `atoll-recruiting-migrate` 可执行程序，migration 与运行身份不混用，错误不打印 DSN；
- Company add/get/list/update/pause/resume/archive/restore 已进入 manifest；Actor handler 不执行网站请求，只做输入验证、短数据库事务和响应；
- Source/Job/Work/DailyRun get 已接入对应 Repository；Work runnable 查询按 capability、可选 origin/Profile、到期时刻和最多 500 条的边界调用已有索引查询，不在 Actor 内复制调度算法；
- Company add 与修改命令将稳定 response receipt、聚合创建/CAS 和 outbox event intent 原子提交；新增冲突不留下 receipt；
- `recruiting.system.reconcile` 每次只读取最多 500 条到期 outbox，将完整 EventIntent 作为公开业务事件写入 Atoll ledger；事件 ID 与 fingerprint 稳定，覆盖 Emit 成功但 SQL checkpoint 前崩溃的重放窗口；失败采用持久 CAS 次数、有界指数退避和 exhausted 终态；
- Actor 在招聘侧状态中持久保存唯一 reconcile timer ID，使用 Atoll 现有 durable timer 自动运行；下一 timer 在当前 fire 被确认前完成挂载与持久化，重启窗口中的孤立 timer 因 ID 不匹配只能被确认、不能继续生长，避免重复周期链；周期可配置为 100ms 至 1h，单轮仍固定最多 100 条以保护 mailbox 公平性；
- `requested_by` 只取 Atoll envelope sender，客户端附带同名未知字段会被严格解码拒绝；
- command request hash 绑定 word 与原始 payload，不绑定短生命周期 human session actor ID；首次操作者进入审计 event 和稳定 response，重连后仍能重放；
- 修改命令在运行领域状态机前先查 receipt，因此 server 重启后不会因聚合版本已经前进而错误拒绝原命令；并发首次执行仍由事务内 receipt 与聚合 CAS 收口；
- 黑盒测试使用真实 `atoll-server`、Portal/WebSocket、隔离 MySQL 8.4、非 root migrator/runtime 身份，完成 add→replay→update→pause→durable timer 自动投递 outbox→Atoll ledger→server restart→get→replay→resume→重启后自动投递→空 reconcile→stale CAS rejection→list；
- P0 probe Actor→Executor、持久 timer 和重启路径保留，Company 控制面没有替换 Atoll 的 actor、message、ledger 或 scheduler。

## 当前验证

```text
make build-go
ATOLL_E2E_BIN=$PWD/bin go test -count=1 ./e2e \
  -run 'TestRecruiting(P0Journey|CompanyControlUsesMySQLAcrossServerRestart)' \
  -v -timeout 240s
make recruiting-mysql-test
go test -race ./drivers/tools/recruiting/...
./scripts/recruiting-boundary-check.sh a94d2b8d
```

## 尚未完成

- Source/Job/DailyRun 的 list、System/Capacity 查询，以及 Source/Work/DailyRun 修改控制词；
- timer→DailyRun→SourceOccurrence 物化和窗口末对账；
- Attempt offer/accept/start/result/fail 与完整数据库 fence；
- 批量导入 preview/confirm 和逐项 outcome；
- 普通非 root 用户的 capability allow/deny 黑盒矩阵；
- `recruiting_recovery_test.go` 的完整重启、重复 ledger delivery 与日报恢复路径。

P3 仍为进行中，Company 纵向切片不能替代完整退出门。
