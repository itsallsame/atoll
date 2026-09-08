# Atoll Recruiting P3 验收记录

状态：进行中，尚未通过退出门

日期：2026-09-08

## 已完成

- Recruiting Actor 仍作为普通 Atoll extension 注册，未改动 `protocol/`、`runtime/`、`lib/`、`platform/`、`registry/`；
- MySQL DSN 不写入 Actor config 或消息，只配置环境变量名，运行时从环境读取非 root runtime DSN；
- 新增独立 `atoll-recruiting-migrate` 可执行程序，migration 与运行身份不混用，错误不打印 DSN；
- Company add/get/list/update/pause/resume/archive/restore 已进入 manifest；Source add/get/list/update/validate/pause/resume/archive/restore 也已进入同一个 Recruiting Actor 控制面；Actor handler 不执行网站请求，只做输入验证、短数据库事务和响应；
- Work create/get/runnable-list/pause/resume/retry/cancel/resolve 已进入同一控制面；人工 create 固定 `trigger=manual` 且 initiator/cause message 取 Atoll envelope，purpose 与 Target 类型组合受白名单约束，capability、origin、priority 和时间窗口有界；
- pause/cancel 提升 acceptance version 以 fence 在途结果；人工 resolve 只能明确记录 `accepted_gap|skipped|terminated`，不能冒充 Executor 的 `succeeded`；retry 只接受终态非成功 Work，并创建带 `cause_work_id` 的新生命周期，不重开或改写原 Work；
- Source/Job/Work/DailyRun get 已接入对应 Repository；Source list 支持全局或按 Company 的一对多 seek pagination，游标绑定查询 Company；Work runnable 查询按 capability、可选 origin/Profile、到期时刻和最多 500 条的边界调用已有索引查询，不在 Actor 内复制调度算法；
- Job list、DailyRun list 和 DailyRun summary 已进入 manifest；Job 按 Source 分页，summary 同时返回一致的运行覆盖计数及可分页 SourceOccurrence，空集合返回稳定数组，畸形或跨 selector 游标按 payload error 拒绝；
- Repository 已实现可信日切原语：在同一 Repeatable Read 事务内从 Company/Source/Listing Assignment/active Recipe 计算完整 eligible 名单，原子创建 running DailyRun、全部轻量 SourceOccurrence 和 outbox；并发同日触发只产生一份确定性名单，昂贵 Work 不在截点洪峰中创建；
- Recruiting Actor 使用 Atoll 现有 durable one-shot timer 驱动日切，不引入进程内 cron；招聘侧只持久化当前 timer ID，只有该 ID 的 fire 可创建名单和续接下一日。时区、本地截点、窗口延迟/时长和策略版本均为严格校验的 extension config；timer payload 冻结本次 UTC 截点和窗口，延迟交付不会按当前墙钟改写所属日期；
- Company 和 Source 新增/修改命令均将稳定 response receipt、聚合创建/CAS 和 outbox event intent 原子提交；新增冲突不留下 receipt；Source 创建还在同一事务锁定所属 Company，拒绝向 archived Company 添加 Source，同时不妨碍历史成功命令在父对象状态改变后重放；
- `recruiting.system.reconcile` 每次只读取最多 500 条到期 outbox，将完整 EventIntent 作为公开业务事件写入 Atoll ledger；事件 ID 与 fingerprint 稳定，覆盖 Emit 成功但 SQL checkpoint 前崩溃的重放窗口；失败采用持久 CAS 次数、有界指数退避和 exhausted 终态；
- Actor 在招聘侧状态中持久保存唯一 reconcile timer ID，使用 Atoll 现有 durable timer 自动运行；下一 timer 在当前 fire 被确认前完成挂载与持久化，重启窗口中的孤立 timer 因 ID 不匹配只能被确认、不能继续生长，避免重复周期链；周期可配置为 100ms 至 1h，单轮仍固定最多 100 条以保护 mailbox 公平性；
- `requested_by` 只取 Atoll envelope sender，客户端附带同名未知字段会被严格解码拒绝；
- 已认证但不属于招聘 Channel 的第二个普通用户，对 Company 查询和修改均由 Atoll 入口以无 eligibility 拒绝；Recruiting Actor 不复制 Channel 权限逻辑，也没有数据库绕行入口；
- command request hash 绑定 word 与原始 payload，不绑定短生命周期 human session actor ID；首次操作者进入审计 event 和稳定 response，重连后仍能重放；
- 修改命令在运行领域状态机前先查 receipt，因此 server 重启后不会因聚合版本已经前进而错误拒绝原命令；并发首次执行仍由事务内 receipt 与聚合 CAS 收口；
- 黑盒测试使用真实 `atoll-server`、Portal/WebSocket、隔离 MySQL 8.4；应用侧由新注册的普通 `recruiting-operator` 在自己的 Home Channel 创建并运维 Recruiting Actor，不借用 root 会话；数据库侧使用非 root migrator/runtime 身份。Company 完成 add→replay→update→pause→server restart→get/replay/resume；随后在同一真实会话完成一个 Company 下两个 Source 的 add→replay→一对多分页→endpoint/category 修正→validate→pause→archive→restore→resume→stale CAS rejection，并验证 Company 归档后旧 add 仍可重放而新 Source 被拒绝；outbox 均由 durable timer 投递到 Atoll ledger；
- 同一黑盒旅程继续完成 repair Work 的 create→replay→get placement→pause/fence→resume→cancel→retry，并验证 retry Work 是唯一 runnable 项、保留 authenticated initiator 和 cause Work，非终态 Work 不能再次 retry；
- 黑盒旅程还通过真实 Portal 验证新 Source 的空 Job list、调度尚未启动时的空 DailyRun list、不存在日报 summary 和畸形 Source cursor 的失败响应；有数据的分页与摘要事实由真实 MySQL Repository contract 验证；
- 同一黑盒旅程由普通用户再创建启用日程的 Recruiting Actor，以未来 15 秒 UTC 截点验证真实 durable timer→Actor→非 root MySQL 路径；到点生成策略版本 11 的 DailyRun，当时 archived/candidate/validating Source 均未错误进入分母；
- P0 probe Actor→Executor、持久 timer 和重启路径保留，Company 控制面没有替换 Atoll 的 actor、message、ledger 或 scheduler。

## 当前验证

```text
make build-go
ATOLL_E2E_BIN=$PWD/bin go test -count=1 ./e2e \
  -run 'TestRecruiting(P0Journey|CompanySourceAndWorkControlUsesMySQLAcrossServerRestart)' \
  -v -timeout 240s
make recruiting-mysql-test
go test -race ./drivers/tools/recruiting/...
./scripts/recruiting-boundary-check.sh a94d2b8d
```

## 尚未完成

- System/Capacity 查询、Work correct 和 DailyRun 修改控制词；Work resolve 的真实 `waiting_human` 旅程依赖后续 Attempt/repair 切片；Source validate 当前只进入 `validating`，验证 Attempt 的接受、契约证明和原子发布仍属于后续纵向切片；
- 到期 occurrence→Work 的渐进物化和窗口末对账仍待实现；
- Attempt offer/accept/start/result/fail 与完整数据库 fence；
- 批量导入 preview/confirm 和逐项 outcome；
- `recruiting_recovery_test.go` 的完整重启、重复 ledger delivery 与日报恢复路径。

P3 仍为进行中，Company/Source/Work 纵向切片不能替代完整退出门。
