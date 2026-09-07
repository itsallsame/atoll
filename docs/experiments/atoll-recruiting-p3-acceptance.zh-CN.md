# Atoll Recruiting P3 验收记录

状态：进行中，尚未通过退出门

日期：2026-09-08

## 已完成

- Recruiting Actor 仍作为普通 Atoll extension 注册，未改动 `protocol/`、`runtime/`、`lib/`、`platform/`、`registry/`；
- MySQL DSN 不写入 Actor config 或消息，只配置环境变量名，运行时从环境读取非 root runtime DSN；
- 新增独立 `atoll-recruiting-migrate` 可执行程序，migration 与运行身份不混用，错误不打印 DSN；
- Company add/get/list/update/pause/resume/archive/restore 已进入 manifest；Actor handler 不执行网站请求，只做输入验证、短数据库事务和响应；
- Company add 与修改命令将稳定 response receipt、聚合创建/CAS 和 outbox event intent 原子提交；新增冲突不留下 receipt；
- `requested_by` 只取 Atoll envelope sender，客户端附带同名未知字段会被严格解码拒绝；
- command request hash 绑定 word 与原始 payload，不绑定短生命周期 human session actor ID；首次操作者进入审计 event 和稳定 response，重连后仍能重放；
- 修改命令在运行领域状态机前先查 receipt，因此 server 重启后不会因聚合版本已经前进而错误拒绝原命令；并发首次执行仍由事务内 receipt 与聚合 CAS 收口；
- 黑盒测试使用真实 `atoll-server`、Portal/WebSocket、隔离 MySQL 8.4、非 root migrator/runtime 身份，完成 add→replay→update→pause→server restart→get→replay→stale CAS rejection→list；
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

- Source、Job、Work、DailyRun、System/Capacity 查询与修改控制词；
- timer→DailyRun→SourceOccurrence 物化和窗口末对账；
- Attempt offer/accept/start/result/fail 与完整数据库 fence；
- 批量导入 preview/confirm 和逐项 outcome；
- outbox→Atoll ledger dispatcher 与 reconcile handler；
- 普通非 root 用户的 capability allow/deny 黑盒矩阵；
- `recruiting_recovery_test.go` 的完整重启、重复 ledger delivery 与日报恢复路径。

P3 仍为进行中，Company 纵向切片不能替代完整退出门。
