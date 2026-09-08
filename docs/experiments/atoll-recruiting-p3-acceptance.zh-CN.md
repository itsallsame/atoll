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
- Work 渐进物化同样使用 Atoll durable one-shot timer，但不固定频率轮询：Actor 查询数据库最早 planned `due_at` 后只挂一个 timer；到点以配置上限领取，仍有到期项时续挂 1ms timer，无待办时不产生空轮询。多个 Actor 依靠 occurrence 行级 `SKIP LOCKED` 协作，Work capability/origin 来自截点 Recipe 快照；
- `recruiting.execution.offer/accept/started/failed` 已进入同一个 Recruiting Actor；Executor 必须是 Atoll envelope 中的 authenticated tool actor，稳定 Actor ID 取 envelope，incarnation 由本次执行进程声明并在后续每一步严格匹配，客户端不能替换 envelope 身份；
- accept/started/failed 的 command receipt 已与 Attempt/Work/Permit/Failure Artifact 置于同一 MySQL 事务。请求哈希绑定 word、原始 payload 和 authenticated Executor；响应丢失后同命令返回第一次的稳定领域响应（Atoll correlation 按本次投递重建），复用 command ID 改参数或换 Executor 被拒绝。两个相同 accept 并发到达时，Attempt 行锁保证一个推进、一个 receipt replay；
- `execution.failed` 现在强制携带分类 Failure Artifact，不能用无证据失败绕过控制面策略。策略版本、自动 Attempt 计数、最后失败类别和下一次可运行时间随 Work 状态保存，索引化 `not_before` 同事务更新：瞬态失败按可配置 base/max 指数退避，429 类至少等待独立 throttled delay；确定性 Recipe/质量错误、不可重试错误或达到可配置次数上限进入 `waiting_human`。`work.retry_scheduled|work.waiting_human` 事件与 Attempt/Work/Permit/Artifact/receipt 原子提交；
- listing offer 在一个 MySQL 事务内锁定单个 runnable Work、重读 cutoff SourceOccurrence 及当前 Company/Source/Assignment/Recipe/Checkpoint/Profile 条件、创建绑定 Executor 的 Attempt；Recipe 正文仍只通过 opaque content ref 传递，完整轻量 offer（含当时 checkpoint）随 Attempt 持久化，因此 Actor/Executor 重启后相同命令可精确重放原始输入，不会读到后来推进的 checkpoint；
- 同一 Work 的活动 Attempt 由数据库生成列唯一约束保证最多一个；领取先无锁读取最多 100 个候选 ID，再按 Work 主键逐项 `FOR UPDATE SKIP LOCKED`，避免 MySQL 对带 `ORDER BY/EXISTS` 的 range locking read 扩大锁范围。两个 Executor 的并发领取连续三轮均获得不同 Work；
- accept 在执行权授予前重检完整领域 fence；started 在一个事务内推进 `Attempt accepted→running`、`Work open/waiting_retry→running` 和首次 `Occurrence queued→running`。错误 incarnation、暂停/变更后的 Source 或不再 active 的 Recipe 均不能启动；failed 不发布业务数据，允许在配置变化后关闭旧执行权，并把 Work 显式置为 `waiting_retry`，不在 Repository 内盲目自动循环。生产失败可携带绑定本 Attempt/Work 的结构化 FailureReport，失败 Artifact metadata、Attempt/Work 状态与 BudgetPermit 释放在同一事务提交；
- `recruiting.execution.result` 现在以 `listing_page|listing_completion|detail` 区分有界页面、列表最终证明和详情结果，同时保留 P0 probe 的旧消息路径；生产结果只接受 authenticated tool actor。页面提交最多 500 个 Observation，并将 Artifact、岗位事实、detail Work 和恢复游标原子落库；最终提交原子推进 Checkpoint、Attempt、Work、Occurrence 与 outbox，Actor handler 不读取网站或等待外部 I/O；
- `listing_page/listing_completion/detail` 三类结果的 command receipt 已分别与页面事实或终态业务事务原子提交，请求哈希绑定原始消息和 authenticated Executor。相同命令并发到达时由 Attempt 行锁串行化，只产生一份页面、岗位、详情版本及 outbox，后续调用返回首次 outcome 快照；复用 command ID 改 payload 或更换 Executor 明确冲突。Artifact 业务键仍作为独立第二层幂等，可为升级前已经提交但尚无 receipt 的历史结果补建回执；
- 页面恢复序列按 Attempt 隔离：同一 Work 的旧 Attempt 页面作为不可变证据保留，新 Attempt 从第 1 页重新执行，terminal 与条目质量统计只读取当前 Attempt。该行为已通过 MySQL 8.4 的部分页面→执行失败→新 Attempt→完成合同测试；
- 同一个 `recruiting.execution.offer/accept/started/failed/result` 生命周期现在按全局 Work priority 为同一 Executor class 领取 `listing_sync` 或 `detail_sync`，步骤类型通过 offer discriminator 表达，不新增 Detail Worker/Actor。detail offer 固定 Job、Detail Assignment、active Recipe/opaque content ref、capability、origin、Profile 和 refresh generation；accept/start/result 均重检 Company/Source/Assignment/Recipe/Profile/Job fence；
- offer 同时在数据库内原子取得 BudgetPermit：global、capability、origin、company、可选 profile 五个维度按稳定顺序锁定计数行，所有上限由带版本的 extension config 配置。并发 origin=1 合约证明两名 Executor 只会获得一个 Permit；失败或成功随 Attempt 事务释放，Permit 到期由已有 reconcile 的独立索引扫描回收，不新增轮询或 Worker 类型；
- detail result 最大 1 MiB，必须带 response Artifact、normalized content hash 和 authenticated executor incarnation；Artifact、Job 状态、仅在内容变化时追加的 DetailVersion、Attempt/Work 终态以及唯一 `detail.completed` outbox 在一个事务内提交。相同结果重放保留原 `content_changed` 语义；错误 sender、配置变化或 refresh generation 变化只保留 rejected Artifact。详情代际不绑定 Listing Checkpoint，因此列表完成推进水位不会误杀已领取的正确详情；
- Result message 不能声明当前领域版本，也不能选择派生 Job/Work ID、detail capability/origin 或 Checkpoint 控制字段；这些值由数据库 fence、Recipe、规范 URL、业务键和 occurrence 决定。失败的 fence/质量证明仅留下 `rejected=true` Artifact；相同 Artifact 页面重放及 terminal outcome 重放稳定；
- Executor 无进展恢复复用已有 reconcile durable timer，不增加 Worker/Actor 类型或 heartbeat 流量。`attempt_stale_after_ms` 默认 15 分钟、限制 1 秒至 24 小时，单轮 `attempt_recovery_limit` 默认 100、最大 500；offered/accepted 超时释放活动 Attempt 槽，running 超时同时把仍匹配 acceptance fence 的 Work 置为 `waiting_retry`，Occurrence 保留其业务生命周期；
- 恢复查询有专用状态/更新时间索引，先读取有界候选，再按 Attempt 主键逐行 `SKIP LOCKED`；两个恢复实例并发处理同一 Attempt 只有一个提交。每次过期追加 `attempt.expired` outbox，当前 tick 随后复用原有 outbox 投递，避免另建周期链；
- 每日窗口结算复用 Atoll durable one-shot timer，招聘扩展只查询最早 running DailyRun 的持久 `window_end_at` 并保存当前 close timer ID；窗口到期后在一个事务内锁定当日固定 occurrence 名单，并按 DailyRun 各一次批量锁取 listing/detail Work，避免约 20,000 Source 下的逐 occurrence 查询；仍为 planned/queued/running 的项被明确结算为异常，未完成 Work 被取消并提升 acceptance fence，再从事实计算 listing/detail/excluded 精确口径，原子提交不可变 DailyRun summary 和唯一 `daily_run.completed` outbox；
- 日报关闭前拒绝提前结算；关闭后相同调用稳定重放且不重复事件。已成功详情、人工接受缺口和其他终态/未终态详情分别计入 `succeeded/accepted_gap/exceptions`，不会用“有终态”冒充 coverage；新的迟到 listing 页面或完成证明因 occurrence 终态及 Work fence 被拒绝，仅允许已接受结果的精确幂等重放；
- Company 和 Source 新增/修改命令均将稳定 response receipt、聚合创建/CAS 和 outbox event intent 原子提交；新增冲突不留下 receipt；Source 创建还在同一事务锁定所属 Company，拒绝向 archived Company 添加 Source，同时不妨碍历史成功命令在父对象状态改变后重放；
- `recruiting.system.reconcile` 每次只读取最多 500 条到期 outbox，将完整 EventIntent 作为公开业务事件写入 Atoll ledger；事件 ID 与 fingerprint 稳定，覆盖 Emit 成功但 SQL checkpoint 前崩溃的重放窗口；失败采用持久 CAS 次数、有界指数退避和 exhausted 终态；
- migration 11 新增独立 execution dispatch outbox。每日 Work 渐进物化按 capability 和可配置 Executor fleet 只创建不超过实例数的初始 wake；人工 listing/detail Work、新 Retry Work、Attempt 成功/失败释放容量及未来 `retry_not_before` 都在各自领域事务中原子写入定向 dispatch。fleet 与 Executor 的对端控制地址可用 Atoll 稳定两段 tool 地址，解除双方必须预知具体 seating ID 的配置循环；投递仍由 Atoll 解析唯一当前成员，completion 只接受匹配目标的 authenticated 三段 sender。现有 reconcile timer 每轮最多投递 500 条，Executor 每次只领取一份 Work；dispatch identity 稳定，同一 delivery 的 offer command 可重放而 redelivery 使用新 command，避免 completion 丢失时旧 offer receipt 令已结束 Work 再执行；未确认投递使用有界 CAS 次数和退避恢复，不新增 Worker class、轮询器或 Atoll core 语义；
- Actor 在招聘侧状态中持久保存唯一 reconcile timer ID，使用 Atoll 现有 durable timer 自动运行；下一 timer 在当前 fire 被确认前完成挂载与持久化，重启窗口中的孤立 timer 因 ID 不匹配只能被确认、不能继续生长，避免重复周期链；周期可配置为 100ms 至 1h，单轮仍固定最多 100 条以保护 mailbox 公平性；
- `requested_by` 只取 Atoll envelope sender，客户端附带同名未知字段会被严格解码拒绝；
- 已认证但不属于招聘 Channel 的第二个普通用户，对 Company 查询和修改均由 Atoll 入口以无 eligibility 拒绝；Recruiting Actor 不复制 Channel 权限逻辑，也没有数据库绕行入口；
- command request hash 绑定 word 与原始 payload，不绑定短生命周期 human session actor ID；首次操作者进入审计 event 和稳定 response，重连后仍能重放；
- 修改命令在运行领域状态机前先查 receipt，因此 server 重启后不会因聚合版本已经前进而错误拒绝原命令；并发首次执行仍由事务内 receipt 与聚合 CAS 收口；
- 黑盒测试使用真实 `atoll-server`、Portal/WebSocket、隔离 MySQL 8.4；应用侧由新注册的普通 `recruiting-operator` 在自己的 Home Channel 创建并运维 Recruiting Actor，不借用 root 会话；数据库侧使用非 root migrator/runtime 身份。Company 完成 add→replay→update→pause→server restart→get/replay/resume；随后在同一真实会话完成一个 Company 下两个 Source 的 add→replay→一对多分页→endpoint/category 修正→validate→pause→archive→restore→resume→stale CAS rejection，并验证 Company 归档后旧 add 仍可重放而新 Source 被拒绝；outbox 均由 durable timer 投递到 Atoll ledger；
- opt-in 真实网站进程验收 `scripts/recruiting-live-e2e.sh` 使用普通 `recruiting-live-operator` 的 Home Channel、真实 server/daemon、Recipe KV、Artifact File 和非 root MySQL。测试先让 daily timer 原子提交 occurrence/Work/pending dispatch，在首次投递前强杀 server；重启、重新登录并确认 daemon Executor 恢复 present 后，同一 Recruiting Actor 从 MySQL 找回 dispatch，完成 HTTP Executor→Greenhouse 公共 Job Board API→分类结果→authenticated completion。2026-09-09 实测 Work=`waiting_human`、Attempt=`failed`、failure=`quality_rejected`、delivered dispatch=1；真实列表未满足活动倒序契约，因此证据落盘但 Checkpoint 未推进。随后注入“completion acknowledgement 丢失”，同一 dispatch 以第二个 delivery 重投；Executor 得到当前 idle 并重新确认，delivery attempts=2 而该 Work 的 Attempt 总数仍为 1。相同旅程随后由普通用户启动独立 diagnostic；真实 HTTP 执行同样如实归类为 `quality_rejected`，但 Job、ListingObservation、Checkpoint 三类业务事实前后完全不变；
- 同一黑盒旅程继续完成 repair Work 的 create→replay→get placement→pause/fence→resume→cancel→retry，并验证 retry Work 是唯一 runnable 项、保留 authenticated initiator 和 cause Work，非终态 Work 不能再次 retry；MySQL 控制面合同进一步证明人工重试已失败/取消的每日 `listing_sync` Work 时，会在创建新因果 Work 和 dispatch 的同一事务重绑未终结 occurrence，因此新 Work 可进入真实 listing Offer，而旧 Work/Attempt 只保留为历史；通用 `work.create` 拒绝直接创建缺少运行模式和执行快照的 listing/detail Work，列表执行由已实现的 `run.diagnostic|join_occurrence|production` 专用命令原子创建完整上下文，不会产生“命令成功但执行找不到 occurrence/ListingRun”的假任务；
- 黑盒旅程还通过真实 Portal 验证新 Source 的空 Job list、调度尚未启动时的空 DailyRun list、不存在日报 summary 和畸形 Source cursor 的失败响应；有数据的分页与摘要事实由真实 MySQL Repository contract 验证；
- `recruiting.system.status` 和 `recruiting.capacity.status` 已进入公开 manifest；前者返回 Work/Attempt/DailyRun/Repair 与 event/dispatch backlog 的一致性快照，后者合并活动预算、runnable capability/origin/Profile 分组、配置上限和 fleet 配置数。普通用户 Portal 黑盒在 timer 与人工 join 各创建一个 listing Work 后看到同一 running DailyRun、两个 runnable Work 和 `http.fetch` backlog；查询不领取 Work，也不把配置 fleet 冒充在线状态；
- 同一黑盒旅程由普通用户再创建启用日程的 Recruiting Actor，以未来 15 秒 UTC 截点验证真实 durable timer→Actor→非 root MySQL 路径；测试准备的两个 verified Source 进入策略版本 11 的 DailyRun，archived/candidate/validating Source 均未错误进入分母；其中早到期 Source 的确定性 due time 到达后，第二条 durable timer 自动创建 `http.fetch`、正确 origin 和 Source Target 的唯一 listing Work；
- 上述普通用户黑盒日程现使用两个 verified Source：一个由 durable due timer 物化，另一个在较晚 due time 前通过 `recruiting.run.join_occurrence` 按 occurrence version 提前加入。命令与重放返回同一 Work，最终两个 Source 各有且仅有一个 runnable listing Work，证明人工参与复用日报快照、Atoll 权限和统一执行队列，而不是数据库旁路或独立 Worker；
- P0 probe Actor→Executor、持久 timer 和重启路径保留，Company 控制面没有替换 Atoll 的 actor、message、ledger 或 scheduler。

- `recruiting.work.list` 已分成默认 `operational` 和显式/兼容 `runnable` 两个视图。operational 支持 Work Center 所需筛选、倒序 opaque cursor、Work 与 placement 同页返回且只读；runnable 保留携带 `due_at` 的旧调用。普通运营员 Portal 黑盒已按 Source、open 状态和 repair purpose 找到唯一 retry Work，并验证无关 Target 返回空集；

- `recruiting.run.diagnostic` 和 `recruiting.run.production` 已进入公开 manifest（本项替代上文“专用命令均未实现”的旧范围说明）：普通运营员以 Source expected version 创建独立 ListingRun 和 Work，命令可稳定重放；Executor Offer 携带冻结 Endpoint/Recipe/Checkpoint 与 run mode，仍走统一 accept/start、预算和分类失败路径。diagnostic 成功结果只接受有界 page/trace Artifact 与质量摘要，并原子完成 Attempt/Work/ListingRun、释放 Permit、写 receipt/event/dispatch，MySQL 合同断言 Job、ListingObservation 和 Checkpoint 为零变化。production 额外要求 Source 具备已验证增量契约和已有基线，只在完整质量证明后写入岗位事实并 CAS 推进 Checkpoint；并发日常运行先推进水位时 completion 被 fence，不能完成 Work/ListingRun 或覆盖新水位。两种模式的终态非成功重试都会原子把 ListingRun 重绑到新因果 Work；

## 当前验证

```text
make build-go
ATOLL_E2E_BIN=$PWD/bin go test -count=1 ./e2e \
  -run 'TestRecruiting(P0Journey|CompanySourceAndWorkControlUsesMySQLAcrossServerRestart)' \
  -v -timeout 240s
make recruiting-mysql-test
RECRUITING_MYSQL_ITERATIONS=3 RECRUITING_MYSQL_TEST_RUN='TestListingExecutionOfferAndLifecycleAreFenced|TestConcurrentListingOffersClaimDistinctWorks' \
  ./scripts/recruiting-mysql-test.sh
go test -race ./drivers/tools/recruiting/... ./drivers/tools/recruitingexecutor/...
./scripts/recruiting-boundary-check.sh a94d2b8d
```

## 尚未完成

- Work correct 和 DailyRun 修改控制词；Work resolve 的真实 `waiting_human` 旅程依赖后续 Attempt/repair 切片；Source validate 当前只进入 `validating`，验证 Attempt 的接受、契约证明和原子发布仍属于后续纵向切片；
- 日报关闭后的 recovered 补偿记录仍待实现；
- 分类失败已有版本化、有界退避并能转 `waiting_human`；按 origin/Recipe/Profile 故障域创建单飞 RepairIncident、站点级覆盖参数和修复后分批唤醒仍待实现。主动 incarnation 失效信号当前仅按无进展超时恢复；正常 dispatch→Executor→result→ack、“Work/dispatch 已提交、首次投递前 server 退出”及 completion acknowledgement 丢失均已通过真实进程与真实网站，仍需 Executor 处理中退出和业务结果 acknowledgement 丢失等切点；execution offer 和高频 page 是否写 ledger/outbox 的审计分层仍待按容量测试确定（accept/start/fail/result 的数据库 receipt 已完成）；
- 批量导入 preview/confirm 和逐项 outcome；
- `recruiting_recovery_test.go` 的完整重启、重复 ledger delivery 与日报恢复路径。

P3 仍为进行中，Company/Source/Work 纵向切片不能替代完整退出门。
