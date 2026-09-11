# Atoll Recruiting：完整开发与验证计划

状态：待执行工程计划

版本：v1.0

日期：2026-09-07

上位设计：`docs/experiments/atoll-recruiting-product-design.zh-CN.md` v0.8

目标分支：`codex/atoll-recruiting`

## 1. 目标与完成定义

本计划把产品设计转化为可提交、可测试、可回滚的工程工作。最终交付必须证明：用户可以通过 Atoll 新增公司、建立 Source 和 Recipe、完成基线、每日增量采集、查看和运维 Work、修复异常；在重复消息、进程退出、数据库故障和大规模负载下，不丢事实、不接受陈旧结果、不重复产生有效业务结果。

“开发完成”同时满足以下条件：

1. 产品设计中 M0—M5 的在册能力均有代码、自动测试和运行证据，或明确标成后续非首版能力；
2. 24 个业务场景和 1 个跨场景大规模日常运行均进入验收矩阵；
3. 至少完成 Live Smoke、Nightly Canary 和 Weekly Coverage 三层真实网站验证；
4. 在参考环境完成 10,000 Company、20,000 Source 及详情放大负载测试；
5. 备份恢复、安全审计、数据保留、上线和回滚手册通过演练；
6. Atoll 核心代码和架构没有被修改。

本计划不把预计日期当作完成证据。建议由 2 名 Go 后端、1 名执行/浏览器工程师、1 名前端、1 名测试/运维兼职组成小组，以 2 周迭代执行，初始估算 8 个迭代；每个阶段只能以退出门通过为准。

## 2. 不可违反的开发边界

### 2.1 Atoll 核心冻结

招聘产品必须作为 Atoll 的扩展实现，禁止修改以下核心目录及其架构语义：

```text
protocol/
runtime/
lib/
platform/
registry/
```

也禁止：

- 为招聘业务向通用协议增加专用字段或消息类型；
- 修改 Channel、ledger、membership、timer、Actor 生命周期或 Resource 的既有语义；
- 在 Gateway、数据库脚本或 Executor 中建立绕过 Recruiting Actor 的第二控制面；
- 因吞吐问题把业务队列、租约或 Worker 角色塞进 Atoll core；
- 为通过招聘测试而放松现有 `archtest` 不变量。

允许的仓库改动范围：

```text
drivers/tools/recruiting/             # Recruiting Actor、领域模型和 MySQL Resource adapter
drivers/tools/recruitingexecutor/     # 唯一 Executor actor class
drivers/tools/all/all.go              # 只允许增加 actor 注册的空 import
e2e/recruiting_*                      # 黑盒验收
scripts/recruiting-*                  # 测试、迁移和边界检查入口
docs/experiments/atoll-recruiting-*   # 产品、开发、验收、运行文档
Makefile                              # 只允许增加招聘专用目标
go.mod / go.sum                       # 只允许经审查增加必要的 MySQL/测试依赖
```

Web Work Center 优先在独立的 `atoll-web` 仓库实现并通过 `WEB_VERSION` 固定版本，不把招聘 UI 反向嵌入 Atoll core。若必须增加仓库内占位入口，只能走现有 Gateway 的公开扩展方式，不能改认证和权限路径。

每个招聘 PR 运行 `scripts/recruiting-boundary-check.sh <merge-base>`。脚本发现冻结目录变化立即失败；确有核心缺口时停止该功能，形成“缺失能力、复现步骤、可选替代”的架构阻塞报告，由用户另行决定，招聘分支不得自行修 core。

### 2.2 Atoll 能力的使用方式

- Recruiting Actor 使用标准 tool actor 注册、Channel membership、Message、timer 和 Actor Resource；
- Work、Attempt、SourceOccurrence、审批、预算和修复事件是招聘领域事实，不假定 Atoll 当前已有通用 Jobs/approvals/quotas；
- 一个 Recruiting Actor 是公开领域权威，不等于新增 Atoll 核心服务；
- 一个 `recruiting-executor` class 可以有多个 Actor 实例，能力和部署位置不同不产生新的 Worker 类型；
- 网站网络 I/O 只在 Executor/Driver 发生，Recruiting Actor 只执行短时校验和持久事务；
- 高频执行数据进入 MySQL/Artifact Resource，Channel ledger 只记录控制、决策、终态、因果及稳定引用。

### 2.3 数据库边界

使用 Staircase 所在 MySQL 服务，但不继承 Staircase 的表、状态机和控制逻辑：

- 使用专用非 root 账号 `staircase`；密码只从进程环境或秘密提供方读取；
- 招聘产品拥有全新的、带版本的 migration，禁止由程序启动时偷偷建表；
- 开发、自动测试、Live 测试和生产使用不同数据库或严格隔离的 schema；
- 自动测试不得清空共享开发库；测试库名称必须带随机 run ID，并在测试后只删除自己创建的库；
- 运行身份默认只有 DML 权限，migration 身份才有 DDL 权限；若第一阶段只能共用账号，必须记录为上线前待拆风险；
- Atoll 自己的 SQLite ledger/registry 不迁入 MySQL，MySQL 只是 Recruiting Actor 控制的数据 Resource。

## 3. 目标代码布局

```text
drivers/tools/recruiting/
  register.go                 # actor class 声明，仅接 Atoll 公共 API
  actor.go                    # 收发消息、鉴权结果消费、调用 application
  manifest.go                 # recruiting.* 公开词和 schema
  config.go
  model/                      # 纯领域状态与转换；禁止网络、数据库、墙钟
  application/                # 命令 handler、用例编排、事务边界
  store/                      # Repository 接口和 MySQL adapter/migrations
  artifacts/                 # Artifact 元数据、哈希、保留策略
  scheduling/                 # Daily Run/SourceOccurrence 物化和对账
  testdata/                   # 小型确定性数据，不保存真实秘密

drivers/tools/recruitingexecutor/
  register.go
  actor.go                    # offer/accept/start/result/fail 协议
  recipe/                     # Recipe ABI、校验器、版本装载
  httpdriver/                 # 公共 HTTP/API
  browserdriver/              # Browser/Profile/Extension adapter
  quality/                    # 身份、排序、边界、字段质量证明
  testdata/                   # 本地站点 fixture

e2e/
  recruiting_journey_test.go
  recruiting_recovery_test.go
  recruiting_permissions_test.go

scripts/
  recruiting-boundary-check.sh
  recruiting-migrate.sh
  recruiting-live-smoke.sh
  recruiting-live-nightly.sh
  recruiting-live-weekly.sh
  recruiting-capacity.sh
```

布局在第一笔代码 PR 中由 `archtest` 和最小 actor 启动测试验证；若现有 import 墙不允许某个子包，调整招聘扩展自身的子目录，不修改 Atoll 分层规则。

## 4. 交付阶段与依赖

| 阶段 | 主题 | 依赖 | 主要退出证据 |
|---|---|---|---|
| P0 | 契约与扩展可行性 | 产品设计 v0.8 | 核心零改动的 actor→executor→result 探针 |
| P1 | 纯领域内核 | P0 | 状态机、命令、幂等和 CAS 单元/性质测试 |
| P2 | MySQL Resource 与恢复 | P1 | migration、事务、并发和崩溃恢复集成测试 |
| P3 | Recruiting Actor 控制面 | P1、P2 | 公开词、权限、timer、重启 e2e |
| P4 | Executor 与 Recipe ABI | P0、P1 | HTTP/Browser fixture 合同测试、陈旧结果拒绝 |
| P5 | 公司接入与基线 | P2—P4 | 首个真实网站纵向切片 |
| P6 | 每日增量与详情版本 | P5 | 连续运行、Checkpoint CAS、边界异常测试 |
| P7 | 运维、修复和人工参与 | P6 | Work Center、共享故障、灰度和 Profile 修复 |
| P8 | 完整业务场景 | P7 | S01—S24 自动/人工验收全部有结论 |
| P9 | 容量、安全与生产准入 | P8 | S25、备份恢复、真实网站分层验收和发布手册 |

P2 和 P4 可在 P1 契约冻结后并行；P3 可先用内存 Repository 开发，但不能在 P2 事务语义通过前宣称完成。P7 的 UI 可在 P3 的查询/命令 schema 冻结后并行。

建议迭代排布：

| 迭代 | 主线 | 可并行工作 | 评审门 |
|---|---|---|---|
| I1 | P0 探针、边界检查 | P1 命令/状态机设计 | 扩展可行、core 零改动 |
| I2 | P1 领域内核 | P2 schema ADR、P4 Recipe ABI | 不变量和 schema 评审 |
| I3 | P2 MySQL、P3 控制面 | P4 fixture/HTTP Driver | crash cut、权限和 actor 重启 |
| I4 | P5 接入与基线 | Browser/Profile adapter | 首个真实纵向切片 |
| I5 | P6 每日增量 | Work Center 查询 UI | 连续七日模型和真实 canary |
| I6 | P7 修复与人工运维 | 批量/纠正/回填 | 故障单飞、普通用户闭环 |
| I7 | P8 S01—S24 | L0/L1 容量和安全测试 | 业务场景全覆盖 |
| I8 | P9 S25、L2—L4、发布准备 | Weekly Coverage、文档 | 生产准入评审 |

职责按能力而不是新 Actor 类型划分：领域负责人维护状态机和命令契约；执行负责人维护 Recipe/Driver；数据负责人维护 MySQL 与恢复；Web 负责人维护 Work Center；验证负责人维护 fixture、e2e、真实网站和容量报告。产品负责人审批场景语义、真实站点名单和 accepted gap，不直接改数据库。

## 5. P0：契约与可行性探针

执行状态：已通过，证据见 `docs/experiments/atoll-recruiting-p0-acceptance.zh-CN.md`。

### 开发项

- 建立上述目录、`recruiting` 和 `recruiting-executor` 两个 actor class，并在 `drivers/tools/all/all.go` 注册；
- 冻结首批 Message schema、错误词、稳定 ID、时间格式、分页和版本字段；
- 实现最小探针：用户命令 → Recruiting Actor 创建内存 Work → Executor 返回 fixture → Actor 接受 → Channel 返回摘要；
- 用 Atoll durable timer 产生一次 SourceOccurrence 到期消息；
- 验证两个 Executor 实例可使用同一 class、不同 capability；
- 建立核心冻结检查和设计追踪表。

### 验证与退出门

- Actor 与 Executor 均只 import Atoll 公开扩展 API；
- 两个真实进程重启后，同一 `command_id` 不重复生效；
- timer 在进程替换后仍能产生唯一 occurrence；
- 探针不修改冻结目录；
- 若 Actor 间调用、Resource 或 timer 不能通过公开 API 完成，P0 失败并停止，不进入 P1。

## 6. P1：纯领域模型与契约

执行状态：已通过，证据见 `docs/experiments/atoll-recruiting-p1-acceptance.zh-CN.md`。领域契约自 P1 验收提交起作为 P2 schema 设计输入；后续若改变不变量或字段语义，必须先更新 P1 测试和验收记录，不能只迁就数据库实现。

### 6.1 类型与状态机

实现 Company、Source、SourceEndpoint、SourceRecipeAssignment、SourceJob、ListingObservation、JobDetailVersion、CuratedOverride、Recipe、Checkpoint、SourceOccurrence、DailyRun、Work、Attempt、Artifact metadata、Profile metadata、BudgetPermit 和 RepairIncident 的领域类型。

状态转换必须是纯函数，显式输入当前版本、命令和业务时间，输出新状态与领域事件。至少覆盖：

- Company onboarding/control；
- Source readiness/control/health；
- Recipe draft/validating/active/quarantined/superseded/disabled；
- Profile ready/repairing/verifying/disabled；
- Work、Attempt、Checkpoint、Job refresh generation 和 Daily Run；
- archived→paused→active 的受控恢复；
- 终态 Work 不重开，后续操作产生有因果链接的新 Work。

### 6.2 命令与结果契约

冻结产品设计 8.3 的公开词。每个修改命令至少含：

```text
command_id
target_id / selector
expected_version
reason
requested_by（来自 Atoll envelope 上下文，不是客户端可填写字段）
```

批量命令增加 Artifact hash、schema/policy version、preview hash 和逐项结果。手工运行固定 `diagnostic|join_occurrence|production`；暂停固定 `drain|finish_causal_chain|cancel`；回填固定 `artifact_recompute|live_refetch`。

### 6.3 不变量测试

对状态机做表驱动、性质和 fuzz 测试：

- 相同命令重放返回相同稳定结果；
- 不同 command ID 仍受公司、Source、baseline、detail、repair 的业务幂等键约束；
- `expected_version`、Checkpoint CAS、Job generation 和 Recipe/Profile 版本独立拒绝陈旧写入；
- Job 不因列表缺失进入删除/下架状态；
- 人工 override 不被后续抓取覆盖；
- diagnostic 永不写业务数据或推进 Checkpoint；
- accepted gap 永不计为 coverage success；
- 任意合法事件序列都不能同时产生两个当前生产 Endpoint 或互相冲突的 Source Assignment。

退出门：纯 model 测试无 I/O、无 wall clock、无随机不稳定；关键状态转换达到分支全覆盖，fuzz corpus 保存已发现反例。

## 7. P2：MySQL Resource、migration 与恢复

执行状态：进行中。Schema ADR、migration/checksum runner、非 root DSN 防护、主要 Resource Repository、DailyRun/全部轻量 SourceOccurrence 的原子截点事务、execution dispatch outbox，以及 Work Center 的绑定游标/索引查询已实现；当前证据记录于 `docs/experiments/atoll-recruiting-p2-acceptance.zh-CN.md`。尚未达到 P2 退出门。

### 7.1 Schema 设计步骤

先以访问模式和事务边界产出 schema ADR，再写 migration。逻辑事实不强制一类一表，但必须支持：

- 规范公司和 Source 键的并发唯一约束；
- append-only Observation、详情版本、Override 和审计血缘；
- Source/Recipe/Profile/Checkpoint generation 的条件更新；
- SourceOccurrence 唯一键和按到期窗口扫描；
- runnable Work 按状态、`not_before`、deadline、priority、capability、origin/Profile 查询；
- RepairIncident 按故障域和签名单飞；
- Artifact 只保存元数据和外部对象引用；
- command receipt 保存稳定响应；
- ledger append 失败后的可重放领域事件意图。
- Work 创建/完成/失败后可恢复、按 capability 定向且由目标 Executor 确认的 execution dispatch intent。

第一版 migration 从全新空库开始，不导入或修改 Staircase 旧表。migration 要有 checksum、升级前检查和备份要求；尚未承诺兼容降级时，回滚使用恢复备份和旧二进制，不编造 destructive down migration。

### 7.2 Repository 合同测试

同一套 Repository contract 至少验证：

- create/get/list/CAS、分页稳定性和事务隔离；
- Work Center 按状态、purpose、trigger、等待原因、Target、发起者和更新时间筛选；倒序 cursor 必须绑定完整 selector，placement 与 Work 同页返回，并为全局及主要 selector 留下 `EXPLAIN` 索引证据；
- Repeatable Read 截点内从持久 Company/Source/Assignment/active Recipe 事实计算完整 eligible 名单，DailyRun、全部轻量 occurrence 和 outbox 要么全部提交、要么全部回滚；
- 两个调度实例并发处理同一日期只能生成一份名单；重放必须返回相同 Source 版本和确定性 due time，改变调度策略或窗口必须冲突；
- 两个并发命令只能一个更新相同版本；
- 事务在“观测、Detail Work 意图、Checkpoint”任一故障点退出后可恢复；
- baseline staging 分块写入、游标失效重扫和 generation finalize；
- 10,000 条基线及其 Detail Work 不依赖单个超大事务；
- connection lost、deadlock、timeout、重复提交、进程 kill 后结果可判定；
- migration 重跑幂等，错误 checksum 拒绝启动；
- 查询均有索引证据和 `EXPLAIN` 记录，不以全表扫描实现日常到期领取。

退出门：在随机测试库上连续运行 100 次 migration+contract suite；无残留数据库、无 root 连接、无明文秘密、无不可恢复半完成。

## 8. P3：Recruiting Actor 控制面

Work 查询现已分离面向人的 operational Work Center 与面向执行面的 runnable 候选视图；前者具备绑定全部筛选条件的稳定倒序分页，并在同页返回 Work placement，后者保留既有 `due_at` 兼容契约。

执行状态：进行中。Company、Source、Work 的人工运维词，以及 Job/DailyRun/Occurrence/System/Capacity 查询已通过真实 Portal 或隔离 MySQL 纵向验收；System/Capacity 以只读一致性事务投影状态、runnable/deadline、活动预算与双 outbox backlog，不引入容量 Actor。Atoll durable timer 已接入可信的每日原子截点名单、按最早 `due_at` 驱动的 Work 渐进物化，以及窗口末自动闭账。`run.join_occurrence|diagnostic|production` 均已接入统一执行队列；日报关闭后的 production recovery 只追加 recovered 事实，不改写历史日报。统一 Attempt 生命周期已按 capability/origin/priority 领取 listing 或 detail Work，具备单活动执行权、不可变 offer、Executor incarnation 和领域 fence；有界 listing page、terminal checkpoint result 及 detail result 的原子事务已通过 MySQL 合同测试。Listing Page Progress 按 Attempt 隔离，Driver 的分类失败与 Failure Artifact metadata 随 Attempt/Work/Permit 在同一事务落库；accept/start/failed/page/completion/detail receipt 也与对应状态事务原子提交。控制面按 Executor fleet 和 capability 创建持久定向 dispatch，未确认投递由 reconcile timer 有界恢复。正常链路、dispatch 首次投递前 Server 退出、completion acknowledgement 丢失、Executor 在第一页后退出，以及业务 result 已提交但 daemon 未处理 response 均已用普通运营员、真实 Server/daemon、Recipe/Artifact Resource、非 root MySQL 和公共只读网站验证。Company import/merge、Recipe rollout batch、Work correct、Source 验证发布及 DailyRun 人工排除等后续控制词已在下文补充闭合；高频执行审计已冻结为 MySQL receipt/Attempt/page/Artifact 与 ledger 终态事件两层，并有零/一 event 合同。失败退避现支持规范 origin×failure class 的精确版本化覆盖，Repair 结案后的有界恢复也已接入自动协调。可信主动 incarnation 失效已使用 Atoll substrate-owned presence/uptime 闭合实现与定向验证；P3 仍需按同一 revision 执行完整退出门。证据记录于 `docs/experiments/atoll-recruiting-p3-acceptance.zh-CN.md`。

进展补充（2026-09-11，站点与错误级重试策略）：extension config 新增最多 10,000 条 `retry_policy_overrides`，每条以规范 HTTP(S) origin 和共享 execution failure class 为唯一键，并完整携带 policy version、最大自动次数、base/max delay 与 throttled delay。解析器拒绝路径、query、userinfo、非 HTTP(S)、未知错误类别、越界参数和规范化后重复键；不提供 wildcard 或 suffix 匹配。`execution.failed` 从数据库读取 Attempt 的不可变 Work placement，按其 origin 与已验证 failure class 选择覆盖，未命中使用全局策略；Repository 继续把实际 policy version 与决定原子写入 Work/receipt/event。单元合同证明大小写/尾斜杠规范化、精确命中产生 60 秒 throttled 延迟、其他错误回落全局，以及四类非法配置 fail closed。证据见 `evidence/recruiting-origin-failure-retry-policy-20260911.json`。

进展补充（2026-09-11，Repair 自动有界续批）：migration 36 为已解决但仍有阻塞成员的 Incident 增加 `recovery_pending` 队列事实及 `(recovery_pending, updated_at, incident_id)` 索引，避免每日运行随历史 Repair 总量退化。`repair.resolve`、手工恢复和自动恢复都在原事务重算队列标志；既有 Recruiting reconcile 每 tick 只选择最老队列项并开放至多 100 个 Work，成功后更新时间使大 Incident 排到队尾，实现跨 Incident 公平轮转。自动批次使用由 Incident/version 派生的确定 command receipt、版本 CAS、聚合事件和每 capability 一个 wake；并发手工恢复或另一个协调者只计作无害冲突。隔离非 root MySQL 合同以 101 项大 Incident 和 1 项小 Incident 跨三次 Repository 重建验证 `100 large → 1 small → 1 large`，最终 102 个 Work 全部开放，恰有三个 receipt、三个 event、三个 dispatch；还用人为陈旧队列位证明协调器会自愈而不制造批次，随后 tick 为 no-op。证据见 `evidence/recruiting-repair-auto-recovery-20260911.json`。

进展补充（2026-09-11，执行审计分层）：容量设计不再悬置 execution offer/page 是否逐条进入 Channel。用户命令、人工决定、分类失败、Work/Attempt 终态和聚合摘要进入 ledger；offer、accept、started、listing page 保留在 MySQL 的 Attempt、原子 receipt、Artifact、page progress 与 Observation，completion/failure 用一个领域事件引用并收束这些高频证据。该边界防止每日 Source×分页×状态转换直接放大 ledger，同时不牺牲 authenticated identity、incarnation、command hash、版本 fence 或 response-loss 重放。非 root MySQL 合同显式证明 page command 零 event intent、terminal completion 恰好一个 event intent；决策见 P2 schema ADR，证据见 `evidence/recruiting-execution-audit-layering-20260911.json`。

进展补充（2026-09-09，替代上一段关于 diagnostic/production 尚未接通的陈述）：`run.diagnostic` 与独立 `run.production` 均已接通 ListingRun、统一 Work/Attempt/Permit/dispatch 和 HTTP Executor。diagnostic 使用 evidence-only 结果事务，不进入 DailyRun，也不写 Job、Observation、Detail Work 或 Checkpoint；production 只接受具备已验证增量契约和已有基线 Checkpoint 的 Source，复用有界 page ingestion，并在 completion 以冻结版本 CAS 当前 Checkpoint。MySQL 合同已覆盖 production 成功发布和并发日常运行先推进水位后的 fence；真实 server/daemon、Greenhouse 公共 API、Recipe KV、Artifact File 与非 root MySQL 旅程中，两种独立运行均如实进入 `waiting_human/quality_rejected`，Checkpoint 保持不变。Artifact 采用每 Attempt 独立同层目录，以适配不支持重复/递归建目录的真实设备驱动，同时不修改 Atoll Resource 语义。

进展补充（2026-09-09，公司导入预览、应用与取消）：`recruiting.company.import` 已建立 Resource-backed 纵向切片。命令原子创建父 Work、`CompanyImport`、receipt/event/dispatch；同一 `recruiting-executor` class 以 `company.import` capability 读取 File Resource、校验原始 SHA-256 和 `company-import.v1` CSV，并以最多 500 项的 envelope 提交。控制面用 batch version、连续 chunk sequence、Attempt incarnation 和 Work acceptance 联合 fence，每个分片独立提交，最终不信任 Executor 自报摘要而从已存项目重算 preview hash。中断后按 item count/sequence 续传；预览成功只让父 Work 进入 `waiting_human(preview_ready)`，逐项审阅可用稳定游标分页。`recruiting.company.import.confirm` 以精确 preview hash/version 启动有界应用协调 Work；每项独立事务创建 Company、子 Work、outcome/event，数据库重复转人工而不回滚其他项；超过一页时同一 Work 持久重投，只续跑 outcome 为空的项，最后 item 已提交而页回执丢失时用空 finalizer offer 恢复汇总。`import.cancel` 先推进 batch fence 并取消旧协调 Work，使在途结果失效，再逐页、逐项取消未开始项。普通用户 Portal、真实 server/daemon、daemon File Resource、非 root MySQL 的 E2E 已覆盖两页 apply、命令重放、部分成功以及预览后取消；隔离 MySQL 另覆盖取消正在执行的批次及陈旧结果拒绝。等待人工项的修复与修复后重新汇总尚未实现。

进展补充（2026-09-11，公司导入人工修复闭环，替代上一段末句）：公开 `recruiting.company.import.item.resolve` 以精确 Batch/Item version 处理已完成批次中的单个 `waiting_human` outcome。`retry` 只重试 immutable preview 中原本 ready 的项，要求运营员先用正常 Company 命令消除外部唯一键冲突；预览即不合法的项不能篡改原输入，只能明确 `skip`，字段纠正必须形成新 Resource/批次。Repository 在同一短事务内锁定 Batch、Item、子 Work 和父 Work，按动作写 Company、Item outcome、Work resolution、receipt/event，再从全部 Item 事实重新聚合；仍有异常时父 Work 不动，最后异常解决后才成功结案。非 root MySQL 8.4 合同验证未修复冲突全事务回滚、并发相同 resolve 一次提交/一次 replay、跳过后继续等待、最终重试后自动结案和原预览详情不变。真实 server/daemon E2E 由普通运营员先通过 `company.update` 释放网站唯一键，再公开 retry 冲突项、skip 无效源行，最终从 2 个等待项收敛到 3 成功/2 跳过/0 等待，输入 File Resource 字节始终不变；没有增加 Worker/Actor 类型。

进展补充（2026-09-11，Work 调度修正）：此前只在产品词表保留、但 manifest/handler 尚未接通的 `recruiting.work.correct` 现闭合为窄语义的调度修正，而不是通用业务数据编辑。普通用户只能对没有活动 Attempt 的 `open` Work 以精确 version 修改 `priority/not_before/deadline_at/profile_id`；Target、purpose、business key、capability、origin 和领域输入保持不变，Work version 与 acceptance fence 同时增加。修正事务原子写 Work/placement、command receipt、`work.corrected` event 和按新 placement 生成的 capability wake；无实际变化、非法时间窗、running/waiting/paused/终态状态或活动 Attempt 均拒绝且不留 receipt。非 root MySQL 合同验证稳定重放、不可编辑字段、活动 Attempt 阻断和 dispatch 一致性；普通运营员真实 Server E2E 已完成 create→correct→replay→pause→resume→cancel→retry，并验证修正后的 placement 可公开读取。业务内容修复继续使用 Company/Source/Recipe/Job/Import 专用命令。

进展补充（2026-09-11，公司逻辑合并与撤销）：公开 `recruiting.company.merge.preview/confirm` 已接通普通运营员边界。预览接受 canonical、1—500 个 alias 和完整 expected versions，冻结规范成员、最新 mapping version、Source/Job/未终态 Work 影响计数并生成 hash；确认事务重新锁定全部 Company/mapping，只追加活动 alias 区间或关闭既有区间，不改写 Company/Source/Job 及历史运行。活动 alias 从未来每日截点排除，`company.get` 展示 canonical，Source 创建与确认使用相同 Company 行锁并明确拒绝 alias；撤销后恢复新增和次日调度。非 root MySQL 合同覆盖稳定重放、陈旧预览零 receipt、原始归属不变以及 merge→reverse 名单变化；普通用户真实 Server E2E 覆盖重启确认重放、阻断新增和撤销。完整复杂拆分与 Source 受控改归属仍是独立后续场景。

进展补充（2026-09-11，Server 重启与 ledger 重复投递）：新增进程级 `TestRecruitingRecoveryAcrossServerRestart`，使用普通运营员、真实 Server、非 root MySQL 和真实 Channel SQLite。测试在领域事件首次交付后只回退应用 outbox checkpoint，模拟 crash 位于 ledger append 与 MySQL delivered 之间；强杀/重启后重复 reconcile 使用相同 event ID/fingerprint，MySQL 再次收口为 delivered，Atoll ledger 仍只有一行。相同 restart 同时验证 Company receipt 和 closed-report production recovery 唯一头重放，原 DailyRun/Occurrence 不变。该证据关闭 P3 点名的 `recruiting_recovery_test.go` 缺口，不替代仍待执行的 Executor 处理中退出、业务结果 response 丢失和 P9 依赖故障矩阵。

进展补充（2026-09-11，业务结果 response 丢失的有界恢复）：Executor result 客户端只对 `Call/Wait` 的模糊传输结果使用原 payload、原确定性 command ID 重试一次；明确业务拒绝和协议错误不重试，第二次仍不确定则返回错误并由持久 dispatch 恢复。单元合同模拟控制面已经处理请求但连接在回包前关闭，断言第一次 pending 被取消、两次 payload 逐字段相同且第二次 receipt acknowledgement 收口；非 root MySQL Detail 合同同时证明业务事实、详情版本、Artifact、event、receipt 与 dispatch 各只有一份。该切片还发现成功 Detail outcome 与通用错误文本复用 `detail` 键导致的客户端解码冲突，现已改为只在 failed terminal 解码错误文本。随后真实 `TestRecruitingLiveDailyRecoveryThroughAtoll` 在一次性 MySQL 8.4 schema 安装非 root fault gate：listing page result 到达触发器后，测试先暂停 daemon，再释放事务直至 receipt/page 提交，最后杀死尚未处理 response 的 daemon；旧 Attempt 自动 expired，新 daemon 用不同 Attempt 完成同一 production recovery，原日报保持 `uncovered=1` 并追加 `recovered=1`。这关闭真实进程回包丢失切点，证据见 `evidence/recruiting-result-response-loss-retry-20260911.json` 和 `evidence/recruiting-live-daily-recovery-20260911.json`。

进展补充（2026-09-11，Executor 处理中退出的真实进程恢复）：opt-in `TestRecruitingLiveBaselinePageRecoveryThroughAtoll` 已重新通过。统一 HTTP Executor 从 Lever 公共只读 API 提交 baseline 第一页 staging/Artifact 后，其 daemon 被 `SIGKILL`；reconcile 将无进展 Attempt 置为 expired、释放 Permit 并把同一 Work 重新排队。第二个 daemon 以新 incarnation/Attempt 从 page sequence 1 重跑；旧/新页面证据均保留，当前 staging 只归新 Attempt，测试结束前未发布 Checkpoint、Observation 或 Job。该证据关闭“Executor 处理中退出”进程切点，紧凑记录见 `evidence/recruiting-live-executor-exit-recovery-20260911.json`。

进展补充（2026-09-11，Atoll presence 加速 incarnation 回收）：Recruiting Actor 在成功 offer 后只跟踪 authenticated concrete Executor ID，并通过 Atoll 公共 `system.member.list` 读取 substrate-owned `present/uptime`；没有增加自报 heartbeat、Worker 类型或 core 修改。首次 present 观测推导保守绑定下界，离线、catalog 消失、重新上线或 uptime 回退只回收严格属于旧绑定的 Attempt；每 tick 的成员 sweep 和 Attempt 回收均受同一 limit 与公平游标约束。MySQL 合同证明 actor/bind-time fence、索引路径、Permit/Work/event/dispatch 原子恢复和活动 Executor 启动恢复；真实 Lever baseline 测试把 stale timeout 设为一小时，旧 daemon 第一页后被 `SIGKILL`，新 daemon 约 104ms 获得新 Attempt并从第一页重跑。presence 只是加速器，查询失败仍由 stale timeout 兜底。证据见 `evidence/recruiting-live-presence-recovery-20260911.json`。

进展补充（2026-09-11，DailyRun 人工控制）：设计审计确认 DailyRun 的 cutoff roster、window、expected count 和关闭 summary 都是不可变覆盖事实，因此不新增通用 `daily_run.update`。公开 `recruiting.daily_run.occurrence.exclude` 以 DailyRun/Occurrence 双 version 只排除窗口内仍为 planned 且没有 Work 的项，保留 occurrence 和当日分母；receipt、`source_occurrence.excluded` event 与状态转换在同一事务提交。窗口到期、日报已关闭、已 queued/running 或跨 DailyRun 的 occurrence 均拒绝。非 root MySQL 8.4 合同覆盖稳定重放、到期后零副作用、DailyRun 逐字段不变，并让人工排除与到期物化真实并发，证明最终只能得到 excluded/无 Work/有 receipt 或 queued/有 Work/无 receipt 两种一致结论。普通用户真实 Server 旅程完成 summary→exclude→server restart→replay→summary，显示 expected=1、excluded=1、uncovered=1、DailyRun version 不变，且没有创建 listing Work。

### 开发顺序

1. 查询：company/source/job/work/daily run/system/capacity；
2. Company/Source 新增、更新、暂停、恢复、归档；
3. Work 创建、重试、取消、resolve；通用 create 不接受 listing/detail，采集型人工运行由带明确模式和执行快照的专用 run 命令创建；
4. timer→DailyRun/全部轻量 SourceOccurrence 的原子截点，随后按 due time 渐进物化 Work，并在窗口末对账；
5. Attempt offer/accept/start/result/fail 和接受条件；
6. 批量导入、预览/确认、逐项 outcome；
7. reconcile handler：长期无进展、Artifact 已上传未接受、事件意图未交付、陈旧 assignment。

Actor handler 不等待网站请求。每个响应包含 correlation、Target、Work、当前版本、状态、下一动作和必要 Resource 引用。错误区分 unauthorised、version conflict、duplicate business key、quality rejected、budget blocked、waiting human 和 internal unavailable。

### 验证

- 通过真实 Portal/WebSocket，以普通非 root 用户执行允许命令；
- 无 capability 用户读取或修改均被 Atoll 拒绝；
- payload 伪造 initiator 无效；
- Actor 重启、server 重启、重复 ledger 交付后状态一致；
- 截点前后新增、暂停、归档都产生规定的 occurrence 结果；
- daily close timer 在窗口结束时只结算一份固定名单；未开始、重试中和仍运行项均有明确异常结果，未完成 Work 被 acceptance version 栅栏，日报与完成事件可稳定重放；
- 每日关闭后恢复只追加 recovered 记录，不改写原日报。

退出门：`e2e/recruiting_journey_test.go` 和 `recruiting_recovery_test.go` 使用真实 server/daemon 进程通过。

## 9. P4：Executor、Recipe 与 Driver

### 9.1 Recipe ABI

先冻结 ABI，再实现站点 Recipe。每次执行输入固定：Target/Endpoint、Recipe Assignment、Checkpoint、Profile 引用、预算许可、Attempt 和接受版本；输出为 Artifact 引用、结构化结果、质量证明和分类失败。

控制面与 Executor 共享的 offer 类型必须位于招聘扩展内部的纯 execution contract 包中，只依赖领域值对象；不得把 MySQL Repository/DAO 类型直接作为 Executor 的编译依赖。Executor 在发出任何网络请求前，将 immutable offer 转换成 Recipe ABI 输入并校验 Work/Attempt acceptance、Company/Source/Assignment/Recipe/Profile fence、BudgetPermit/到期时间、origin、listing/detail discriminator 与载荷互斥关系。任何串线都按不可执行处理，不能依赖结果提交阶段兜底。

Recipe 分 `listing|detail|discovery`。v1 优先使用可验证的声明式 HTTP/JSON/DOM 步骤；需要 JavaScript 或交互时进入受控 Browser/Extension driver。不得默认执行来源不明的任意宿主代码。候选 Recipe 必须经过 schema 校验、静态安全检查、真实样本验证和发布审批。

Listing Recipe 必须显式声明 identity field 与 detail URL field，不能依赖 `detail_url` 等约定字段名猜测。单个外部页面最多产生 500 条唯一 Observation，与控制面单事务页面上限一致；跨页重复岗位只随首次出现的页面提交，内容冲突使分页质量证明失败。Driver 先持久化每页 Artifact，再暴露逐页 URL、下一游标、去重后新增 items 和 terminal 标记。Baseline Executor 必须在抓取下一页前把该页提交隔离 staging，完整质量证明成立后才提交独立 ListingDelta completion；普通增量在通用 Attempt staging 实现前仍先完成质量证明再发布，禁止质量失败后残留 Job。声明式 JSON Recipe 同时支持响应元数据型 offset pagination，以及把 offset/limit query 与 page size 纳入内容哈希的短页终止型 pagination；根数组 collection 必须显式声明。

### 9.2 Driver

- HTTP：超时、响应上限、redirect policy、User-Agent、robots/条款记录、origin 限流、429/403 熔断；
- Browser：隔离 context、页面/内存/时间上限、下载和外部写操作禁用；
- Profile：只传不可解析引用，在授权设备和安全域解析；Cookie、密码、OTP 不进 Channel/AI/普通 Artifact；
- Extension：只负责捕获和人工修复，输出候选 Recipe/Artifact，不直接修改领域数据；
- Artifact：内容哈希、脱敏、大小限制、访问权限、保留期。

进展补充（2026-09-11，公共 Browser Runner）：同一个 `recruiting-executor` Actor class 已可用 `browser.public` capability 构造真实 Chrome/CDP 数据面，不新增 Worker 类型，也不修改 Atoll core。Browser Plan 作为 Recipe ABI 正文的一部分参与内容与兼容性哈希，运行时不能替换；Browser Recipe 只产生一个终态 DOM，不能再叠加第二套 next/offset 分页协议。每个 Session 使用独立 Chrome 进程和临时 profile，CDP Fetch 在 origin 前阻断非 GET/HEAD 请求，并限制公网目标、同源文档导航、下载、popup、导航数、DOM 字节和总时长。DOM 与 attestation 通过原 Artifact/trace 合同进入现有 Listing/Detail/Discovery 结果路径。确定性真实 Chrome 测试证明 POST、popup、download 和跨源文档 redirect 均未到达禁止的目标，robots disallow 发生在页面导航前，缺失 selector 稳定分类为可修复结构错误；高风险矩阵连续 5 轮通过。显式公网测试已访问 `https://example.com`。这完成应用层公共 Browser 执行切片，但不能代替生产环境的 OS/container 出站隔离；设备侧 Profile provider、真实授权 canary 和扩展整包点击门仍是退出门。

Recipe 与 Artifact 只通过 Atoll 已有的公开 `Actor Resource` 接口接入，不新增或修改 core Resource 语义：Recipe 正文是受 Channel 权限保护的小型 KV Resource，offer 只携带 `recipe://`/`artifact://` 不透明引用和预期 SHA-256；Executor 读取后严格解码，并复核 ABI、kind、transport、capability 与内容哈希。原始响应、页面和截图是 File Resource，Executor 在字节上限内流式写入并提交，领域消息和 MySQL 只保存 ResourceID、内容哈希、访问范围与保留策略。Resource 拒绝、缺失、超量或哈希不符均 fail closed，不能降级为把正文塞进 Message、State 或数据库。

进展补充（2026-09-09，Recipe 隔离与 Source 级回滚；2026-09-10 补齐 Listing）：migration 25 为所有当前 `SourceRecipeAssignment` 建立不可变版本历史，此后 validation publish、rollout 和 rollback 都在原业务事务中追加历史。公开 `recipe.inspect` 返回一个不可变 Recipe version 及当前 Assignment 数；`recipe.quarantine` 只以 Recipe state version CAS 隔离 active 版本，并原子保存 receipt/event，不对大量 Source 扇出写入。执行领取已有的 active Recipe fence 立即阻止新 Attempt。Detail 或 Listing Source 均可用 `recipe.rollback` 引用较早历史版本，但实际追加更高 Assignment version，并以 Source/Assignment 双 fence、active target、contract/capability 兼容性共同拒绝陈旧或不安全切换。Listing 回滚额外锁定当前 Checkpoint：若存在，必须精确对应当前 Assignment，随后只重绑定兼容 Recipe 身份并提升 fencing version，frontier、overlap 和最后 occurrence 均不变化；Source 无条件进入 `repairing` 并清除旧校准，重新验证前不参与每日调度。普通用户经真实 server 完成 rollout→inspect→quarantine→Detail/Listing rollback，服务重启后写命令稳定重放；隔离 MySQL 使用独立 `staircase_migrator`/`staircase_runtime` 非 root 账号验证原子性、历史不被改写、版本单调及 Checkpoint 水位不移动。候选 Recipe 的真实执行验证与审批不能用本状态切换替代。

进展补充（2026-09-10，Listing 候选 Recipe 真实验证）：公开 `recipe.propose` 已先补齐候选创建入口：以 ready Source/version/active Endpoint revision 为来源 fence，从 Atoll Recipe Resource 严格读取 Spec，使用与 Executor 共享的 ABI decoder 复核 256 KiB 上限、未知字段、尾随 JSON 和静态安全规则；content hash、兼容性 contract hash 与 scope 均由 Actor 计算，事务只创建 immutable draft、receipt/event。错误客户端 hash 被 `quality_rejected` 且不会占用 Recipe version。随后 `recipe.validate` 把 draft/quarantined 候选推进 validating，并原子创建 evidence-only ListingRun、Work、receipt/event 和 capability dispatch。它冻结真实 ready Source 的生产 Endpoint、当前 Company/Source 版本、候选 Recipe content/contract/execution，以及只用于 fence、不发布的拟议 Assignment version。统一 HTTP Executor 以 `recipe_validation` result kind 保存 page/trace Artifact 和质量证明；结果事务只完成 Attempt/Work/Run、释放 Permit，不写 Source Assignment、Job、Observation 或 Checkpoint。独立 `recipe.approve` 只有在指定 Work 精确匹配候选、恰有一个成功 Attempt、其全部 Artifact 未拒绝且 identity/ordering/pagination 均成立时才发布。隔离 MySQL 合同已验证提案/验证命令重放、Recipe version 冲突回滚、result kind 串线拒绝、零业务副作用和成功审批。普通用户、真实 server/daemon、非 root MySQL 与 Discord 的公开 Greenhouse API 又验证了负向真实样本：可执行候选因实际数据不满足其声明的活动倒序契约而被 `quality_rejected`，Recipe 保持 validating、Assignment 数为零。该旅程同时暴露并关闭了 dispatch purpose 白名单和 Executor result acknowledgement 两个跨进程断链。公开 `recipe.reject` 还要求精确 validation Work 已终结且没有活动 Attempt，才把候选返回 draft；Work 未关闭时明确返回 `waiting_human`。普通用户 server E2E 已覆盖 Resource propose→验证→拒绝→先取消 Work→拒绝成功→重启后幂等重放。Detail/Discovery 候选验证仍待完成。

进展补充（2026-09-10，Extension Capture 提案事实）：`recipe.propose` 现可同时接收权限受控的 Recipe Resource 与可选 Extension Capture Resource。Capture 使用独立 1 MiB 上限的严格 decoder，拒绝未知字段和尾随 JSON；Actor 将 Capture 的 `captured_by` 与消息信封中的真实操作者绑定，并交叉验证 Source ID/version、active Endpoint revision/URL、Candidate 规范 content hash 和 Capture 语义 hash。migration 26 把 capture identity、Resource 引用/哈希、结构化 trace 和 evidence Artifact 引用作为独立、不可变的 Proposal 事实，与 Recipe draft、receipt/event 在同一事务提交；失败不会留下半个 Draft 或占用 Recipe version。`recipe.inspect` 可读回该来源事实，但 Proposal 没有 validate/publish/Assignment/Checkpoint 权限。普通用户真实 server E2E 已覆盖伪造操作者拒绝、成功提案、查询和重启重放；非 root migrator/runtime MySQL 合同已覆盖迁移、原子持久化和幂等。JSON API trace 不再错误套用 CSS 规则，而是必须与已通过 ABI 校验的 Candidate Extraction 精确对应。服务端 Proposal 接入已完成，浏览器侧进展见下一段。

进展补充（2026-09-10，本机 Extension Bridge 与真实 DOM 捕获）：新增独立 `atoll-recruiting-extension-bridge` 客户端适配器和 Manifest V3 插件开发预览，不改变 Atoll `/ws` 的同源 Cookie/Origin 安全边界。Bridge 仅监听显式 loopback IP，以 256-bit 随机令牌接受 `chrome-extension://` Origin；它使用普通用户登录会话查询 Source，从该请求的真实消息信封取得 `captured_by`，再严格构建 Evidence/Recipe/Capture 三个 Resource 并调用公开 `recipe.propose`，不直写 MySQL、不创建 Worker/Actor。浏览器 Draft 不能声明 Source version、Endpoint revision、身份、hash、状态、Assignment 或 Checkpoint。Listing 捕获必须精确匹配 active Endpoint；Detail 捕获另以样本 Job ID 让 Bridge 通过公开 `job.get` 读取 Job version/detail URL，Actor 与提案事务再次复核 Source 归属、版本和 URL，Detail Recipe scope 由该详情页 host 推导。普通用户真实 Server + 非 root MySQL E2E 已跑通 Listing/Detail Bridge→Resource→Draft→inspect；Chrome 活体测试使用与内容脚本相同的内核访问 Discord 公开 Greenhouse 列表及其第一条真实详情页，分别生成 Listing 与 Detail Draft 并通过 Go 侧严格 ABI。该真实页只验证 DOM 捕获机制，不证明活动倒序契约，也不代替 Detail 候选执行验证。当前机器的官方 Google Chrome 144 不加载命令行指定的 unpacked extension，因此整包自动加载/点击门仍缺 Chrome for Testing 或 Chromium 环境；不得以放宽 Atoll Origin 检查代替。Detail/Discovery 候选执行验证、Browser Broker/Profile 和需要交互的网站仍待完成。

进展补充（2026-09-10，Detail 候选 evidence-only 样本验证）：`recipe.validate` 已按候选 kind 分流，Detail 必须显式引用同 Source 的真实 `sample_job_id`。Actor 从 Atoll Resource 重新严格读取候选 Spec、复算 content hash 和预期提取字段数；事务将候选推进 validating，并创建独立 `RecipeSampleValidation`、统一 Work、定向 dispatch，而不是把详情语义塞进 ListingRun。验证冻结 Company/Source、当前 Detail Assignment、候选 Recipe、样本 Job version/detail URL 和拟议 Assignment；Attempt 另带 sample version fence，领取、accept、start 和结果提交均会重锁并拒绝漂移。Executor 仍是同一个 class，按 offer kind 运行普通 Detail Driver，但只回传 response/trace Artifact、单记录证明、字段数和规范内容 hash；结果事务不会创建 `JobDetailVersion`、不会推进 Job generation、不会改 Assignment。审批要求指定 Work/run、恰好一个成功 Attempt、精确两个未拒绝证据、字段数与冻结 Spec 一致后才发布候选；拒绝同样支持 Detail run，且不能遗弃活动 Attempt。migration 27 与非 root MySQL 8.4 全套 race 合同已通过，并断言结果重放稳定、Permit 释放、Recipe 仍 validating、Assignment 不变、详情版本为零。普通用户进程 E2E 又由真实 Atoll server/daemon 从 MongoDB 的 Greenhouse board 动态选择一个当前岗位，执行 Detail candidate→response/trace→approve；最终候选 active，但当前 Assignment 仍指向旧版本且 JobDetailVersion 为零，证明“验证”和“发布/rollout”没有串线。紧凑证据已升级为 `evidence/recruiting-live-recipe-validation-20260910.json` v2。Discovery 候选执行验证仍待完成。

进展补充（2026-09-10，Company 级 Discovery Recipe 提案）：Discovery Recipe 不属于任何已知 Source，它的种子是 Company 官网。`recipe.propose` 因此新增 company target 分支，以 Company version、active control 和规范官网 host 作为 provenance/scope fence，拒绝 Source endpoint revision 和 Extension Capture 字段；Actor 仍从 Atoll Resource 严格解码并自行计算 content/contract hash，事务只创建 draft、receipt/event，不改变 Company，也不创建假 Source。隔离 non-root MySQL 合同验证了 scope/version、重放和零 Source 副作用；普通用户真实 server E2E 又验证了 Company 提案与伪 Source fence 拒绝。下一步是独立的 Discovery evidence-only run/result：它可以复用统一 Discovery Driver，但不能复用会保存候选 Source、在零候选时改变 Company onboarding 状态的生产 `source_discovery` 结果事务。

进展补充（2026-09-10，Discovery 候选 evidence-only 验证）：`recipe.validate` 的 Company 分支现把 draft/quarantined Discovery Recipe 推进 validating，并原子创建通用 `RecipeSampleValidation`、Work、receipt/event 和 capability dispatch。run 与 Attempt 只冻结 Company/version、官网 URL、候选 Recipe 和预期字段数；没有 Source/Assignment，也不伪造生产 discovery generation。Executor 仍是唯一一个 Recruiting Executor class，复用 Discovery Driver，但结果类型是 `recipe_sample_validation`：只提交 response/trace、0—500 候选计数、字段数和规范内容 hash，不把候选正文交给生产结果事务。零候选可以作为一次成功执行的负证据收口，但审批至少要求一条候选；任何 Company/Recipe 漂移、字段数不符、Artifact 缺失或串线都会 fail closed。migration 28 与隔离 non-root MySQL 8.4 合同已经证明成功审批和零候选拒绝都不改变 Company、不创建 Source/SourceDiscovery/Candidate。普通用户经真实 Atoll server/daemon 又在 MongoDB 公开 careers 页完成 Company→propose→validate→approve；验证结果仅有 response/trace，Company version/onboarding 未变，Source、生产 Discovery 与 Candidate 均为零。紧凑证据已升级为 `evidence/recruiting-live-recipe-validation-20260910.json` v3。

兼容性补充：双页面围栏以 `recruiting.extension-capture.v2` 发布；严格解码继续接受原始 v1 Listing 形状，并沿用 v1 的原始哈希输入，已经保存的不可变 Capture Resource 及命令可稳定重放。

### 9.3 本地确定性站点

建立可编程测试服务器，至少模拟：

1. 带可靠更新时间的倒序分页；
2. 无时间字段、只靠有序 frontier keys；
3. 同一时间跨页、置顶广告和分页漂移；
4. 新岗位、历史岗位更新置顶、边界消失；
5. SPA/API、redirect、空结果、字段异常和结构变化；
6. 429、403、5xx、慢响应、登录过期、验证码标记。

退出门：所有 Recipe 可离线重放；同输入和 Recipe 版本得到相同规范结果；Driver 不执行投递简历等外部写操作；旧 Attempt 结果只能留下 rejected Artifact。

## 10. P5：公司接入和首次基线纵向切片

执行状态：进行中。显式 Source Discovery generation、受 Recipe/Company/Attempt fence 约束的一页有界真实执行、证据 Artifact、0/1/N 候选分页，以及逐候选接受/拒绝已经进入 Recruiting 扩展；接受候选会在同一事务创建 `candidate` Source，跨 Company 的规范 Source key 冲突会完整回滚并交给人工判断。零候选结果会在确认公司没有非归档 Source 后，把 Company 与 Discovery/Work 在同一事务收口为 `blocked_no_sources`；已有其他 Source 时不误判，命令重放不重复推进。MongoDB 公共 careers 页面已经通过普通用户、真实 server/daemon、非 root MySQL 和 HTTP Executor 完成 `Company → discovery → Candidate → 人工接受 → Source(candidate)` 纵向验证。`source.validate` 现会原子推进 Source、冻结 Candidate Endpoint/active Listing Recipe 和版本围栏、创建专用 `source_validation` Work 与 validation run，并复用统一 Executor 执行 Listing Recipe；结果只保存页面/trace Artifact 与质量观测，不写 Job、Observation 或 Checkpoint。该链路已继续在 MongoDB 的公开 Greenhouse API 实跑：Work/Attempt 成功、page/trace 证据落盘，但真实数据违反活动倒序契约，因此 Source 正确进入 `invalid`，业务事实保持为零。Endpoint 或 Recipe 改版会排除旧 validation Work并要求新 run；瞬时执行失败按同一失败策略和不可变 run 延迟重试，质量违反则作为业务结果进入 `invalid`。Source validation publication 是其后的独立证据闸门：只有目标 Source Work 所属、未拒绝且非 failure-only 的 Artifact，才能随 active Listing Recipe、Assignment、四项校准结论、命令 receipt 和事件一起原子发布。可执行 baseline generation 已冻结 Company/Source/Recipe/Assignment/Endpoint 与 Checkpoint 策略，复用统一 Executor 做有界 page staging，并在 terminal completion 原子建立首个 Checkpoint。finalized staging 由 Recruiting Actor 现有 reconcile 按版本化 source-job cursor 每批至多 500 条物化为 Job/Detail Work；岗位事实、Work、游标和聚合 capability dispatch 同事务提交，并发协调循环及命令重放不会重复有效结果。该当前生产路径已用 10,000 条 staging 验证为 20 个短事务，精确产生 10,000 个 Job、Detail Work 和成员账本；两个 Executor 目标只产生 40 个按页聚合 dispatch。baseline 成员账本把每个详情成功与 generation CAS 原子核算；所有投产 Source 的最新 baseline 成功后，独立有界 reconcile 推进 Company ready，避免万级详情争用 Company 行。隔离 MySQL 已覆盖 validation create/offer/result/evidence publish、修复围栏/重试、baseline start/offer/page/completion、并发及万级物化、详情成功与人工接受缺口核算、Company ready 和 replay。满足生产增量契约的真实站点完整 validation/baseline 仍未完成，因此尚未达到 P5 退出门。当前证据见 `docs/experiments/atoll-recruiting-p5-acceptance.zh-CN.md`。

进展补充（2026-09-09，零 Source 真实进程）：`TestRecruitingLiveZeroSourceDiscoveryThroughAtoll` 由登录用户在 Home Channel 添加 Company 并启动 discovery，真实 daemon/统一 HTTP Executor 只读访问 IANA `https://example.com/`。Discovery Recipe 的 collection 合法得到空集合而非 parse failure；结果事务完成 Attempt/Work/Discovery、保存 response Artifact，并把没有任何既有 Source 的 Company 推进为 `blocked_no_sources`。测试同时断言 Source=0、candidate=0、阻塞事件恰好一条、Artifact metadata 与 daemon File Resource 字节均存在。该稳定公开页面仅作为无招聘入口 fixture，不代表业务目标公司。

进展补充（2026-09-09，baseline 中断隔离）：migration 20 为 staging 行增加 `attempt_id`，并让 finalized BaselineGeneration 冻结唯一成功的 `listing_attempt_id`。页间退出后新 Attempt 从第 1 页重扫；completion 只统计当前 Attempt，物化也只读取被冻结 Attempt 的行，旧 Attempt 的独有岗位键不能混入基线或 Checkpoint。隔离 MySQL 合同已用“旧 Attempt 留 1 行、重试 Attempt 产生 2 个不同岗位”的不利场景证明最终只物化后者；10,000 条 legacy/fixture staging 路径仍保持 20 个短事务。Executor 现改为 Artifact durable→解析→逐页提交→下一请求，而不是完整扫描结束后批量回传；提交后释放该页完整 item，只保留固定大小内容哈希的 identity 去重表、frontier、计数和轻量页/Artifact 引用，重复岗位内容变化在释放原文后仍可判定 pagination 不稳定。opt-in `TestRecruitingLiveBaselinePageRecoveryThroughAtoll` 使用普通用户、真实 server、两个先后启动的 daemon incarnation、非 root MySQL 和公开 Lever 招聘 API：Attempt A 的第 1 页落入 page progress/staging 后强杀进程，reconcile 将其置为 expired 并把 Work 转为 waiting_retry；Attempt B 再次从 `skip=0` 提交第 1 页。两个 Attempt 各自只有 page sequence 1，最终用户取消不产生 Checkpoint、Job 或正式 Observation，两个原始页面 Artifact 字节均存在。该 fixture 只证明分页/恢复机制，不作为目标 Source 的增量契约校验证据。

进展补充（2026-09-09，baseline 用户取消）：通用 `recruiting.work.cancel` 对 `baseline_listing` 进入领域感知事务，而不是只改 Work 行。运行中取消会原子终结 Baseline、拒绝 Attempt、释放非 root MySQL 中的 Permit/全部预算维度、追加 work/baseline 事件和容量 wake；在途迟到 page/detail/Failure 只保存 rejected Artifact，不产生 Checkpoint。同一命令重放保持所有版本和计数不变，后续可创建更高 baseline generation。检查真实进程旅程时发现 `baseline.start` 未列入首次 capability dispatch 白名单；现已在招聘扩展中补齐并以目的闭集单测防回归。隔离 MySQL 合同及 `TestRecruitingBaselineCancellationThroughAtoll` 覆盖确定性的用户消息、重放、server 重启、更高 generation 和 queued 取消。opt-in `TestRecruitingLiveBaselineCancellationThroughAtoll` 进一步启动真实 server/daemon/Executor：先以 MongoDB 公开 Greenhouse API 只读诊断预热同一 origin，再在 Baseline Attempt 已 running、第二次真实 HTTP 读取等待 origin budget 时由用户取消；daemon 的迟到分类 Failure 被持久化为 rejected Artifact，Work/Baseline/Attempt/Permit 未复活，Checkpoint、Observation、Job、staging 和活动预算保持为零。测试中的 ready Source 是明确标注的切点 fixture，不冒充真实增量资格验证。

进展补充（2026-09-09，baseline 详情人工修复）：三岗位合同先完成一个详情，让第二个 `parse_error` 进入 `waiting_human`；用户拒绝 accepted gap，以 `terminated` 关闭旧 Work 后，Baseline 核算数和成员 pending 状态保持不变。随后 `work.retry` 在创建新因果 Work、receipt/event/dispatch 的同一事务重绑成员 `detail_work_id`；新 Attempt 提交真实 DetailVersion 后，该成员核算为 succeeded 且不增加 exception。第三个失败项由用户明确接受缺口，最终 Baseline 为 `completed_with_exceptions`、恰有一个 accepted gap，Company ready。隔离 MySQL 合同已覆盖；`TestRecruitingBaselineDetailRepairThroughAtoll` 又以真实 server、登录用户 Home Channel、WebSocket Message 和 Recruiting Actor 覆盖一岗位 baseline 的 `parse_error → waiting_human → terminated → causal retry → detail succeeded → Company ready`，并验证 retry 命令重放及成员重绑。该测试由仓储层模拟 Executor 生命周期，真实 daemon 提交失败后由普通用户修复的进程切点仍待完成。

按以下顺序交付一个真正可用的切片：

```text
普通用户新增 Company
→ 创建 bootstrap/source discovery Work
→ 发现 0/1/N Candidate Source 并逐项确认/拒绝
→ 验证 Endpoint、Listing Recipe 和增量契约
→ baseline generation 分页扫描与 staging
→ listing finalize、首个 Checkpoint、幂等 Detail Work
→ 详情版本提交或明确缺口
→ Company ready
```

必须覆盖重复公司官网提示、规范 Source key 冲突、零 Source、多个 Source、验证失败、1 万岗位、分页中断、详情部分失败和用户取消。第二次校准分别记录 identity、pagination、ordering、update-retop 的 `verified|unverified|violated`；没有真实更新样本时不得宣称 update-retop 已验证。

退出门：至少一个允许访问的真实公开站点完成 discovery→baseline→detail，产物含 URL、Recipe 版本、Artifact、Checkpoint 和可追踪 Work；同一命令重放不改变岗位数。

## 11. P6：每日增量主链路

实现：

- 每日截点和 SourceOccurrence 渐进物化；
- 从列表顶部到旧活动边界并完成安全重叠；
- 同一活动时间组完整扫描；
- 新岗位和历史更新岗位产生不同 refresh generation；
- 列表观察、Detail Work 意图和 Checkpoint 的事务/CAS；
- 详情失败不回滚列表 Checkpoint，旧可用详情继续可见；
- 边界缺失、排序违约、空列表和扩大扫描预算超限不推进 Checkpoint；
- diagnostic、join occurrence、production 与 timer 并发时的统一 CAS；
- Daily Run 的 coverage、exceptions、uncovered 和 recovered 投影。

连续日测试至少运行 D0 baseline、D1 无变化、D2 新岗位、D3 历史更新置顶、D4 同时间跨页、D5 边界消失、D6 修复恢复。每一日注入重复消息、Actor/Executor 退出和数据库回包丢失。

执行状态：进行中。隔离非 root MySQL 8.4 合同 `TestDailyIncrementalD0ThroughD6PreservesBoundaryAndRefreshInvariants` 已建立首个连续旅程：D0 不是伪造 Checkpoint，而是实际创建并运行 baseline Work/Attempt，证明 staging 在 finalize 前不可见，随后物化 Job/Detail Work、完成全部详情成员并把 Company 推进 ready；D1 的无变化重叠只增加 Observation，不重复产生有效 Detail generation；D2 只为新岗位创建一项详情 Work并接受详情版本；D3 对重新置顶的历史岗位保留同一 Job 并推进 refresh generation，新详情解析失败进入 `waiting_human` 后，已提交的列表 Checkpoint 不回退，上一 refresh generation 的可用详情版本和内容哈希继续保留。该失败使用公开执行命令事务，相同命令在“数据库已提交、Atoll 回包丢失”后重送只返回原回执，不重复故障事实；D4 只有完整消费跨页的同活动时间组后才提交新边界，并注入“安全扫描完成、第一页已提交、Executor 退出”的恢复切点：reconcile 将旧 Attempt 置为 `expired` 并释放 Permit，迟到页只留下 rejected Artifact，新 incarnation 从 page sequence 1 重扫；重复第一页保留两条真实 Observation，但岗位 E 仍只有一个 Job 和一个有效 Detail generation。D5 在 `max_pages` 内找不到旧边界时，Driver 的质量证明先失败，页面不发布，Checkpoint、Job 和 Observation 均不改变，Work 携证据进入 `waiting_human`；D6 由用户 `terminated` 原生命周期并创建因果 Retry Work，SourceOccurrence 原子重绑，新 Executor incarnation 从第 1 页重扫并恢复同一 Checkpoint 谱系。成功日均重放 plan、page、completion，并重复运行定时物化协调；最终断言 5 个 occurrence 全部完成、Checkpoint 从 1 精确推进至 6、8 个 Job、9 个 Detail Work、26 条已接受 Observation、14 条 Attempt 级 page progress，失败 Work/重试 Work 的终态及预算归零一致。尚未完成“每一日”全部 Actor/Executor 退出和数据库回包丢失排列，也未以真实站点证明历史更新重新置顶，因此 P6 退出门仍未关闭。

进展补充（2026-09-09，日报缺口与补偿；2026-09-11 完成 live execution）：`DailyRunProgress` 现显式返回 `uncovered` 和 `recovered`；前者始终以截点冻结分母减去原 occurrence 的 listing success 计算，后者独立统计成功的补偿 ListingRun，修复不会美化历史日报。`recruiting.run.production` 可选冻结 `recovery_of_occurrence_id`，Repository 在创建事务反查同 Source、原 occurrence 为 exception/excluded、DailyRun 已关闭；migration 21 的外键和唯一键保证一个缺口只有一条可重试恢复谱系。补偿完成与 Checkpoint、Work/Attempt、结果回执及 `daily_occurrence.recovered` 事件原子提交。隔离 MySQL 合同覆盖创建回包重放、Recruiting Actor 仓储实例重建、成功补偿、completion 重放、原 DailyRun/Occurrence 不变、`uncovered=1/recovered=1` 并列，以及第二条恢复谱系全事务拒绝。领域规则同时修正为：存在 `excluded` 的日报必须是 `completed_with_exceptions`，不能冒充完整 coverage。E2E `TestRecruitingOperatorCreatesDailyRecoveryWithoutRewritingClosedReport` 又从普通登录用户经真实 Atoll server 创建补偿，在 server 重启后重放同一命令、拒绝第二条谱系并验证失败事务不残留 Work；命令创建阶段仍保持原日报 `uncovered=1,recovered=0`。opt-in `TestRecruitingLiveDailyRecoveryThroughAtoll` 进一步由普通用户、真实 Server/daemon/统一 Executor 只读访问 AcuityMD 公开 Greenhouse board：先冻结当前真实岗位 ID 为 `frontier_keys` Checkpoint，关闭原 occurrence 为 exception，再通过公开 production recovery 完成 page/completion。2026-09-11 实测 16.03 秒通过，历史 DailyRun/Occurrence 逐字段相等，`uncovered=1,recovered=1` 并列且补偿事件恰好一条。生产列表发布必须已有 active Detail Assignment；live fixture 明确提供真实 Detail Recipe，而不是绕过该领域前置条件。紧凑证据见 `evidence/recruiting-live-daily-recovery-20260911.json`。

退出门：所有成功日只扫描必要增量和重叠，不抓取历史全集；任何故障排列均为零漏失的已接受 Observation、零陈旧 Checkpoint 覆盖、零重复有效 Detail generation。

## 12. P7：运维、修复与人工参与

### 开发项

- Work Center 查询和操作：筛选、下钻、领取、暂停、恢复、取消、纠正、重试、resolve；
- Company/Source control、readiness、health 三类状态分别展示；
- `failure_domain=origin|recipe_version|profile|single_target` 和 RepairIncident 单飞；
- Recipe quarantine、候选验证、逐 Source Assignment、canary、rollout、rollback；
- Profile 一次性安全修复会话、设备绑定、超时、权限和审计；
- Observation/verified detail/CuratedOverride 的字段来源与撤销；
- 批量导入、批量纠正、历史回填和 Recipe 发布的 preview hash、逐项结果与续跑；
- 公司逻辑合并映射、Source 谱系和受控拆分预览；首版不做普通物理迁移；
- notification 只发聚合摘要，不为共享故障制造大量人工提醒。

### 验证

- 一个 Recipe 版本令 1,000 个 Work 同时失败，只创建一个 RepairIncident/Repair Work；
- 修复发布后分批恢复，不能瞬时冲垮 origin/Profile；
- Profile 无权限用户只能看到认证阻塞，日志、消息和 AI 上下文找不到秘密；
- 人工 override 在后续每日抓取后仍有效，撤销后正确回到 verified detail；
- 批量操作中断后只续跑未完成项，父级 outcome 与逐项事实一致；
- 归档不删除历史岗位，恢复先验证再 catch-up；
- accepted gap 结束人工队列但 Daily Run 仍为 completed_with_exceptions。

退出门：普通运营员可以只通过 Atoll UI/公开消息完成一天的查看、修复和结案，不需要数据库写权限或 root 账号。

执行状态补充（2026-09-09，首个 Detail Recipe 修复切片）：公开词 `recruiting.recipe.rollout` 已允许普通用户把一个 Source 的既有 Detail Assignment 切换到已 active 的兼容 Recipe。Source 与 Assignment 各自使用独立 expected version；Repository 在同一事务重新锁定 Source、当前 Assignment、旧/新 Recipe，重新推导投影，并原子提交 Assignment、Source、command receipt 与 `source.detail_recipe_rolled_out` 事件。为保持已创建 Retry Work 的 placement 语义，首版拒绝改变结果 contract 或 Executor capability，也明确不接受 Listing/Discovery Recipe。隔离 MySQL 合同已覆盖并发发布仅一个获胜、获胜命令稳定重放、能力不兼容时所有事实回滚。新增 opt-in `TestRecruitingLiveDetailRecipeRepairThroughAtoll`：测试运行时从 MongoDB 公开 Greenhouse board 选择仍存在的岗位，真实 daemon 首次以错误 JSON pointer 读取该详情并保存 response/failure Artifact、进入 `waiting_human(parse_error)`；普通用户经 Home Channel 依次 `resolve(terminated) → recipe.rollout → work.retry`，同一 daemon 使用修复版 Recipe 再次读取并成功提交唯一 DetailVersion，Baseline 成员重绑且无 exception，Company 最终 ready。第一次增强证据断言揭示 FailureReport 只登记 failure 摘要、遗漏已写入 Resource 的原始 response metadata；现已以向后兼容的可选 `artifacts` 列表修复，所有 supporting Artifact 必须同 Work/Attempt、ID 唯一并随失败事务一起入库。增强后的独立旅程耗时 45.59 秒，随后纳入全套再次通过耗时 45.76 秒。尚未完成 Recipe 候选创建/验证、quarantine/rollback 和批量 canary，因此 P7 仍为进行中。

执行状态补充（2026-09-09，共享故障单飞与查询）：所有被版本化失败策略送入 `waiting_human` 的执行失败，现会在同一事务创建或加入 `failure_domain + failure_signature + failing_version` 的 RepairIncident，并把受影响 Work 指向唯一 Repair Work；仍可自动退避的瞬态失败不制造人工单。故障域和 failing version 由控制面从 Attempt/placement 冻结事实推导，Driver 只产生字符受限的稳定阶段签名。成员使用规范化关联表，Incident state 不嵌入无界列表；只有首个失败追加一条 `repair.opened` 聚合事件。隔离非 root MySQL 8.4 合同以 32 路并发提交 1,000 个同 origin 失败，验证恰有 1 个 Incident、1 个 Repair Work、1,000 条成员关系、1 条聚合事件，全部 Attempt failed、Permit released、Work waiting_human 且阻塞指针一致，命令重放不增加任何事实。测试首次揭示 InnoDB 同键循环死锁，现以同 key 的 Repository 有界哈希锁、数据库唯一键以及 context 有界 1213/1205 事务重试分层收敛。普通运营用户现可通过 `recruiting.repair.list/get` 查看按状态 seek 分页的共享事件、唯一 Repair Work、实时成员计数，以及按 Work ID 稳定分页的受影响任务；游标与筛选条件绑定，Incident 内的种子数组不被误当作完整成员集合。公开 Server E2E 已验证两页下钻和重启后重读；真实网站 Detail 修复 E2E 也加入失败后公开查询断言。生命周期与有界恢复见下一段；Recipe quarantine/rollback 和 Profile 修复仍待完成，因此 P7 退出门未关闭。

执行状态补充（2026-09-09，Repair 验证、结案与有界恢复）：普通运营员现可在公开消息边界执行 `recruiting.repair.validation.begin → recruiting.repair.resolve → recruiting.repair.recover`。开始验证必须引用一个已由 Executor 成功完成、且因果源 Work 属于该 Incident affected 集合的 Retry Work；普通用户文本不能伪造成功证据。该事务同步把 Repair Work 推进到 running；resolve 再次验证证据，原子解决 Incident、完成唯一 Repair Work 并释放活动单飞键。相同 key 后续回归创建新的历史 Incident，同时并发回归仍只保留一个活动事件。恢复命令按 Incident version 串行，每批至多 100 个仍被该 Repair Work 阻塞的 Work，保留失败 Attempt/分类事实并清除运行阻塞；每个 capability 只投递一个初始 wake，实际 offer 继续受全局、capability、origin、company 和 Profile 预算限制。公开 Server E2E 覆盖无效证据拒绝且零状态变化、完成 canary、过早恢复拒绝、resolve、单条恢复、命令重放和 Server 重启；真实网站 Detail 修复旅程在成功访问修正版 Recipe 后也完成 Incident validation/resolve。另一个非 root MySQL 合同创建 201 个 affected Work，以 1 个 canary 后严格执行两个 100 条恢复事务，验证首批重放不重复开放、最终 `recovered_work_count=200`、无 waiting member 且活动键已释放。Recipe 候选创建、quarantine/rollback、Profile 安全修复以及全自动分批协调仍待完成，因此 P7 退出门仍未关闭。

执行状态补充（2026-09-10，岗位字段人工修正）：公开 `recruiting.job.correct` 已把既有 `CuratedOverride` 领域/存储能力接入普通运营员消息边界。set/clear 同时冻结 Job version 与 override-head identity/version，客户端不能自报操作者；命令事务先锁定真实 Job，再追加不可变 override version、移动 head、保存稳定 receipt 和不含字段值的审计事件，原始 Job、ListingObservation 和 DetailVersion 都不被改写。值为非 null、有效且至多 64 KiB 的 JSON，字段名有界且拒绝控制字符。`recruiting.job.correction.get` 返回指定 Job+field 的当前 head 和至多 100 条历史。隔离非 root MySQL 合同验证了原子性、陈旧 head 回滚、set/clear 与重放；普通用户真实 server E2E 又验证后续新抓取推进 Job 版本但人工覆盖仍生效、旧浏览器页被版本栅栏拒绝、撤销后恢复底层事实优先级，以及 server 重启后命令重放与两版历史一致。该切片不实现岗位下架，也不把人工修正反写成抓取事实。

执行状态补充（2026-09-10，Profile 认证失败熔断）：统一 Executor 提交 `auth_expired`/`captcha` 后，控制面现会在失败命令事务内同时保存失败 Artifact、关闭 Attempt、释放 Permit、把 Work 关联到单飞 RepairIncident，并将仍匹配 Attempt 接受版本的 Profile 从 `ready` 推进到 `repairing`。该转换只改认证状态和版本，opaque Secret 引用、授权设备不进入事件且保持不变；派发候选查询会跳过非 `ready` Profile，避免队首故障任务遮挡其他可运行任务。两个已在途、绑定同一旧版本且稳定签名相同的 Attempt 可依次保存失败并加入同一 Incident，但 Profile 只推进一次、只产生一条 `profile.repair_required` 事件；后来到达的旧失败不会再次熔断已更新 Profile。`budget_revoked` 不会被误判为认证失效。隔离非 root MySQL 合同已覆盖命令重放、共享加入、秘密/设备引用不变和后续领取阻断。一次性安全修复会话、Executor 验证恢复和普通用户公开 Profile 运维接口仍未实现，因此 P7/S12 尚未完成。

执行状态补充（2026-09-10，Profile 设备绑定修复协议）：在不修改 Atoll `protocol/runtime/lib/platform/registry` 的前提下，Recruiting 扩展已增加公开 `recruiting.profile.get`、`recruiting.profile.repair.begin`，以及统一执行协议中的 `profile_repair`/`profile_verification` offer/result。普通用户只能创建 expiring session 和读取脱敏投影；控制面将 Work 精确投递到 Profile 绑定的 Tool Actor，错误设备看不到候选，且普通用户不能调用执行接口。设备提交仅包含轮换后的 opaque `secret://` 引用和 redacted validation Artifact，原子推进 Profile 到 `verifying` 并创建独立 canary Work；canary 通过后 Profile 仍被栅栏，只有既有 `repair.validation.begin → repair.resolve` 才在结案事务中恢复 `ready`。交互/验证失败不会递归制造 RepairIncident，过期会话由既有 reconcile 有界取消并释放活动键。Repository MySQL contract 覆盖授权领取、错误设备拒绝、重放、秘密不进入响应/事件、两阶段验证/结案、无预算控制 Work 和过期回收；公开 Server E2E 覆盖普通运营员发起、重放、权限拒绝、脱敏查询及重启恢复。设备侧浏览器扩展/本地 broker 的交互 UI 和真实登录站点 canary 尚未接入，因此此切片完成的是控制面协议与持久化闭环，P7/S12 仍不宣称完成。

执行状态补充（2026-09-10，Profile 本机 Broker 与站点 canary）：同一个 `recruiting-executor` class 现可按 `browser.profile.repair` capability 调用 loopback-only Bridge 的 `/profile-task`；Executor 与插件分别使用不同的强令牌，令牌只从 `0400/0600` 文件读取，Profile Artifact 强制为 redacted。Bridge 在插件暂时离线时最多保留 8 个有截止时间的请求，插件重连后重投；请求、结果均绑定 Attempt、会话、安全域、Recipe ID/version/content hash/contract hash 和最小记录数。插件必须先位于相同 HTTPS 安全域，随后导航到冻结的精确 canary URL并运行站点专用 Browser Recipe；纯逻辑测试证明只返回完整记录数，不返回提取值，低于阈值会立即形成负向结果而非等待超时。Executor 在 accept/start 前通过 Atoll Resource 解析并复核 Recipe hash/contract，成功后只保存结构化脱敏验证证据。定向 race、Bridge/扩展测试和非 root MySQL 8.4 Profile 合同已通过。仍缺 Chrome 整包自动化、真实授权登录站点 canary，以及能让日常 Browser Driver 解析新 `secret://chrome-profile/...` 引用的设备侧 Profile provider；因此该切片推进了 S12，但 P7/S12 仍未完成。

执行状态补充（2026-09-11，Source/Profile 日常路由前置闭环）：migration 37 新增 Listing/Detail 独立、可版本化且不含 Secret 的 `SourceProfileBinding` 当前表与历史表；公开 `recruiting.source.profile.bind/unbind` 以 Source/Binding 双版本、当前 active Assignment、Profile ready、安全域和授权 Tool device 为事务围栏。Listing 换 Profile 会使 Source 进入 `repairing` 并要求重新校准，Detail 换 Profile 不改 Listing 结论；仍使用 `browser.recipe` 时禁止解绑。每日截点只选择绑定一致且 Profile ready 的 Browser Source，把 Profile ID 冻结到 occurrence，Work 继承该 ID，dispatch 无视普通 fleet 候选而定向到 Profile device；Offer 仍在事务内复核 authenticated concrete Executor，猜中 Profile ID 的错误设备不会留下 Attempt。列表页派生详情 Work 时会读取独立 Detail binding，并在同一结果事务定向唤醒对应设备，允许列表/详情分置。Source validation 默认复用持久 Listing binding，Repository 再次锁定 binding/Profile。非 root MySQL 8.4 合同已证明命令重放、历史、Secret 不泄漏、每日快照、错误设备零副作用、正确设备领取，以及 HTTP 列表向另一设备上的 Browser Detail 路由。设备侧日常 Profile provider 与真实授权站点仍未完成，因此 P4/P7/S12 继续保持进行中。

执行状态补充（2026-09-11，设备侧日常 Profile Provider 与修复供给闭环）：统一 Executor 已正式接受 `browser.recipe` capability，并在授权设备内用 owner-only JSON 注册表解析 `profile://recruiting/... + Profile version` 到持久 Chrome profile 目录。注册表逐次重读、拒绝符号链接/宽权限/重复 ID/版本或域不符；每个 Profile 使用进程内加跨进程文件租约，Chrome 完整退出并释放目录后才开放下一 Session。受控 CDP 的 GET/HEAD、同源文档、robots、下载/popup/写请求限制与 `browser.public` 共用，但 Profile DOM 在设备内先删除表单、脚本、iframe、事件属性和敏感 URL 参数，Executor 又强制 `artifact_redaction=redacted`。修复插件连接必须同时提交 registry 中独立 SHA-256 所对应的明文 Profile 绑定令牌，Bridge 只把任务交给匹配 Profile 的连接，并在修复 canary 成功后以旧 ready→verifying、验证 canary 成功后以 verifying→下一 ready 的明确版本围栏原子替换同一 registry；数据库回包丢失重试幂等，跨域和陈旧轮换失败关闭。插件连接持有与每日 Runner 相同的跨进程目录租约，二者不能同时打开凭证目录。真实 Chrome 测试连续启动两个独立 Session，证明第二次复用第一个 Session 持久化的 Cookie、DOM 已脱敏、租约有效且目录未被临时清理；配置/注册表/Bridge 负向测试覆盖错误绑定令牌、陈旧版本、跨域、跨进程并发和权限。Chrome for Testing 153 整包测试又从真实 Manifest V3 启动 Service Worker、打开 popup 并点击完成受管 Profile 配对，不再用内容脚本内核替代插件。仍缺第三方真实授权登录站点 canary和生产部署 OS/container 出站隔离，因此 P4/P7/S12 仍不宣称完成。

执行状态补充（2026-09-10，Listing Recipe 安全历史回滚）：`recruiting.recipe.rollback` 现同时接受 Listing 历史 Assignment，但不把“同 contract hash”误当作可以立即恢复生产。Actor 与 Repository 双重要求目标为同 kind、active、同 contract hash、同 Executor capability；Source/Assignment/Recipe/Checkpoint 均在事务中重新锁定。已有 Checkpoint 必须精确绑定回滚前的当前 Assignment，随后仅把 Recipe ID/version 重绑定到历史版本并提升 Checkpoint fence；frontier、同时间边界、overlap、最后 occurrence 均保持原值。回滚追加单调的新 Assignment version，并把 Source 置为 `repairing`、清除旧四维校准、复制生产 Endpoint 为 Candidate，重新验证前不会被每日截点选入。隔离非 root MySQL 合同验证原子重放与水位不移动；普通用户真实 server E2E 验证 Detail/Listing 回滚、操作顺序安全和 server restart 后重放。S10 的单 Source 历史回滚切片已闭合；候选灰度 rollout、批量 canary 和不兼容迁移仍未完成。

执行状态补充（2026-09-10，Listing Recipe 逐 Source 灰度）：公开 `recruiting.recipe.rollout` 已从 Detail 扩展到 Listing，但仍坚持一个命令只切换一个 Source。目标必须是已经 active 的同 kind Recipe；Repository 在锁内再次验证 scope、contract hash 和 Executor capability 与当前 Recipe 完全兼容。若 Source 已有 Checkpoint，事务提升其 fencing version并重绑定 Recipe ID/version，但保持 frontier、同时间组、overlap 和最后 occurrence 不变；Assignment 历史、Source 投影、receipt 与 `source.listing_recipe_rolled_out` 事件在同一事务提交。Source 随即进入 `repairing` 并清空旧校准，操作者必须完成 Source validation 才能恢复每日调度。非 root MySQL 合同覆盖带 Checkpoint 的 rollout→rollback 水位不移动；普通用户真实 server E2E 覆盖 Listing rollout、历史 rollback 及两条命令在 server restart 后重放。该能力是批量升级协调器可复用的安全逐项原语，不等于批量 canary 已完成。

执行状态补充（2026-09-10，Recipe 批量灰度领域与 schema 基础）：新增纯领域 `RecipeRolloutBatch`/item 状态机和 migration 30。批次采用持久父 Work、规范化成员、确定性 canary 顺序、每块/每 wave 至多 500 项；`previewing` 可按 chunk sequence 从中断处续跑，最终 preview hash 绑定输入 Artifact、目标 Recipe 和每个 Source/Assignment 版本。确认后先只开放 canary，所有成员重新验证成功才开放下一 wave；等待验证不推进版本，任一失败把固定 wave 原地暂停，不能扩散到后续 Source，恢复必须显式执行。父 Work 不进入 Executor runnable 队列，不增加 Worker 类型。当前提交只冻结领域/schema 契约；Resource 预览、Repository 协调事务、公开查询/控制词与 reconcile 尚待后续切片接通，因此不把 S22 标记完成。

执行状态补充（2026-09-10，Recipe 批量灰度持久预览与公开控制）：Repository 已接通批次创建、最多 500 项的连续 preview chunk、数据库事实重算 preview hash、成员 seek 分页、确认启动和取消。migration 31 追加持久 schema/policy version，并将二者纳入 preview hash；不改写已发布的 migration 30。公开 `recipe.rollout.batch/get/items/confirm/cancel` 已接入普通用户消息边界，输入是严格的 `recipe-rollout-sources.v1` JSON KV/File Resource，最多 20,000 个唯一规范 Source ID；Actor 验证原始字节 SHA-256、未知字段、尾随 JSON 和 Resource 内外 schema 一致性，再按确定性 canary 顺序分块。创建只接受无 Executor capability 的父控制 Work；预览逐块锁定 Batch，并按 Source ID、当前 Assignment/Recipe 和目标 Recipe 的稳定次序取锁，逐项重新验证 Source 为 active/ready/healthy、当前与目标 Recipe 的 kind/scope/contract/capability 完全兼容且版本不同。任一不兼容成员会使整块回滚；完成后父 Work 原子进入 `waiting_human(preview_ready)`。确认只启动父 Work 和批次并开放 canary，不在一个 20K Source 大事务里切换 Assignment，也不生成 Executor dispatch。隔离非 root MySQL 8.4 合同覆盖乱序拒绝、不兼容整块回滚、命令重放、最终 hash、稳定分页、启动及活动批次键释放；普通用户真实 server E2E 覆盖 Resource 预览、分页、确认、取消和 server restart 后重放，并断言目标 Assignment 零变化。逐项 rollout、验证观测、wave 自动推进与失败暂停仍待后续 reconcile 切片，因此 S22 仍未完成。

执行状态补充（2026-09-10，Recipe 批量灰度逐项证据账本）：migration 32、领域状态机和 Repository 已保存 applied time、Source/Assignment applied fence、validation Work/run 与 validated time，并从一致数据库快照重算当前活动 wave 的成员状态计数。成员不相信协调器自报的“已应用”版本，而是在父批次/成员锁后重新锁定 Source 与当前 Assignment，精确核对目标 Recipe、投影、版本和生效时间；伪造事实及并发第二赢家均被拒绝。失败项显式重试会按失败发生在切换前或切换后分别回到 pending 或 awaiting validation，后者不重复产生 Assignment version。普通 cancel 只能安全关闭尚未应用成员的运行批次；已有应用事实时必须等待显式回滚。完整非 root MySQL 8.4 contract 已通过。

执行状态补充（2026-09-10，Listing Recipe 批量 canary 纵向执行）：migration 33 先持久化 apply request 的稳定业务时间，Actor reconcile 随后以确定性 command ID 调用既有逐 Source Assignment 原语；即使进程退出在 Assignment 提交与成员登记之间，重启仍读取数据库目标事实并继续，不能重复产生 Assignment version。Listing 成员会创建真实 `source_validation` Work/run 和既有 Executor dispatch；成功 Attempt 的质量结果、page/trace Artifact 仍经过原 Source publication 证据闸门。对已经选择目标 Recipe 的重校准只更新 Source assessment，不再生成一次同 Recipe Assignment。隔离非 root MySQL 8.4 纵向合同跨 Repository 重建依次验证 plan→apply→validation、模拟 Executor 成功、自动发布、成员/父 Work/批次完成、Assignment 历史恰为旧版与目标版两条；完整 store 126.515 秒及 Actor 合同 1.197 秒通过。Detail canary、失败 wave 的显式 rollback/resume、多 wave 与进程级 Server/Executor E2E 仍未完成，因此 S22 继续保持未完成。

执行状态补充（2026-09-10，失败 wave 暂停与显式恢复）：数据库从活动 wave 的规范化成员事实重算失败数，任一失败会原子把 Batch 与父 Work 推进到 `paused`/`waiting_human(rollout_failed)`，不会开放下一 wave。公开 `recipe.rollout.batch.resume` 重新锁定整个固定 wave，并要求每个失败成员关联的旧 validation Work 已由用户终结；随后在一个至多 500 项的事务中按失败阶段恢复成员、启动父 Work 并清零批次失败计数。已经 applied 的成员回到 awaiting validation，Assignment fence 保持不变；切换前失败才回到 pending。每次恢复以成员 version 派生新的 validation generation，并把该代 Source version 与 Work/run 一起登记，防止复用旧终态 Work 或旧 command receipt；当前 Source snapshot 用 CAS 锁定，已应用 Assignment version 继续作为不变围栏。跨 Repository 的非 root MySQL 纵向合同已实际执行首次质量失败→Source invalid→wave 暂停→显式恢复→新 Work/Run→验证成功→批次完成。命令 receipt/event 与恢复原子提交并可重放。部分应用批次仍不能直接 cancel；显式 rollback 见下一段。

执行状态补充（2026-09-10，Listing 批量失败全前缀回滚）：公开 `recipe.rollout.batch.rollback` 只从暂停批次启动，把 `active_through` 冻结为 rollback 上界；此前成功 wave、当前失败 wave 中已应用成员和未应用成员均在同一规范化账本中有明确结论，未来成员不生成 Assignment。Actor 以 ordinal 逆序、每轮最多 500 项推进，逐 Source 复用既有 Assignment/Checkpoint 原子切换，把预览冻结的旧 Recipe 追加成新的 Assignment version，再创建独立 rollback validation generation。成功证据仍经过 Source publication 闸门；全前缀成功/跳过后批次进入 `rolled_back`、父 Work 以 `terminated` 结案并释放 active scope。rollback 自身失败会进入 `rollback_paused/waiting_human(rollback_failed)`，同一个公开 `batch.resume` 在旧失败 Work 终结后恢复该阶段。Repository 对暂停时仍处于 `applying` 的成员重新检查数据库：目标 Assignment 已提交则补齐 forward evidence，尚未提交则登记 `rollback_before_application` 后跳过，漂移状态拒绝。migration 34 保存 rollback 状态、Assignment/Source fence、validation Work/run 和结果时间；非 root MySQL 纵向合同跨 Repository 重建跑通两次目标验证失败、显式 rollback、旧 Recipe 新验证、Source ready、Assignment 历史 old→target→restored 和最终批次结案。Detail rollback canary、多 wave 容量和真实进程 E2E 仍待完成，S22 继续保持未完成。

执行状态补充（2026-09-11，Detail Recipe 批量 canary）：Detail 成员复用同一批次/wave 和逐 Source Assignment 原语，但不伪造 Listing 重校准。切换后 Actor 从该 Source 的 `available` Job 中按稳定 ID 选择一个真实样本，创建 mode=`rollout` 的 evidence-only Recipe sample validation Work；active Recipe、当前已切换 Assignment、Company/Source/Job version、URL origin 和 BudgetPermit 共同 fence Executor offer/result。结果只保存 response/trace Artifact、记录数、字段数和规范化哈希，不创建 DetailVersion，也不改变 Job refresh generation。候选 Recipe 原有 validating 模式继续要求精确字段数；rollout 模式把字段数解释为至少一个字段的生产烟测下界，真实 Executor 仍报告实际 Recipe 字段数。跨 Repository 的非 root MySQL 旅程先用旧 Recipe 产生真实可用 Job，再执行批量 plan→Assignment apply→Detail sample Work/Attempt/result→wave 完成，并断言样本 Job 与既有 DetailVersion 全程不变。公开 resume/rollback 消息路由遗漏也已补齐。多 wave 容量和真实 Server/Executor 进程 E2E 仍待完成，S22 继续保持未完成。

执行状态补充（2026-09-11，Detail Recipe 整批回滚）：rollback 协调器现按 Batch kind 选择验证策略，Assignment 恢复、逆序完整前缀、成员 fence、暂停/恢复和父 Work 结案仍与 Listing 共用；Detail 恢复不清空 Listing assessment、不移动 Checkpoint，也不把 Source 置为 repairing。每个已应用成员追加指向预览冻结旧 Recipe 的更高 Assignment version，并以该恢复 Assignment 选择当前稳定 Job，创建全新的 mode=`rollout` sample validation Work/run。非 root MySQL 纵向旅程覆盖第二个 Detail 目标的真实验证失败、Work 人工终结、显式 batch rollback、old→target→failed-target→restored 的单调 Assignment 历史、旧 Recipe 样本验证和最终 `rolled_back`；验证前后的 Job、refresh generation 与 DetailVersion 精确相等。多 wave 容量和真实 Server/Executor 进程 E2E 仍待完成，S22 继续保持未完成。

执行状态补充（2026-09-11，20,000 Source 与多 wave 容量）：新增非 root MySQL 容量合同真实写入 20,000 个 ready Source、当前 Assignment 和不可变历史，以 40 个各 500 项的生产 Repository 短事务建立完整预览，随后确认只开放 10 个 canary。数据库事实模拟 canary 成功后只开放 500 项 wave；前 499 项成功时不推进，第 500 项成功后才开放下一固定 500 项。测试首次真实跨第二 wave 时发现 reconcile 事件错误归属到版本不变的父 Work，触发唯一键冲突；现改为归属版本单调的 `recipe_rollout_batch`，父 Work 只表达运行控制。成员查询也排除仍在执行/退避的 validation Work，终态后重新纳入；20K 合同明确验证第 511 项运行时第 512 项仍能取得配额、第 511 项转人工后会被优先收口。Actor 对本轮活动 Batch 使用确定性预算均分，总处理数仍不超过 tick limit。增强合同 67.034 秒通过并以正式 cancel 释放测试 scope。真实 Server/Executor 进程 E2E 仍待完成，S22 继续保持未完成。

执行状态补充（2026-09-11，S22 真实进程闭环）：新增 opt-in `TestRecruitingLiveDetailRecipeRolloutBatchThroughAtoll`，由普通运营员经公开消息上传固定 Source Resource、审阅并确认批次；真实 Atoll Server、durable reconcile timer、daemon 和唯一 `recruiting-executor` class 动态读取 MongoDB Greenhouse 当前公开岗位详情。两个 Source 严格先运行 1 个 canary，再开放 1 个 wave 并完成；随后对单 Source 发布故意缺字段的 active Recipe，真实 Executor 失败后 Batch/父 Work 停在 `paused/waiting_human`，未向未来成员扩散。运营员公开终结失败 validation Work 并执行整批 rollback，控制面恢复冻结的旧 Assignment，再由同一 Executor 对同一公开详情页生成新的 evidence-only 成功证据，Batch 最终为 `rolled_back`。测试逐字段比较执行前后 Job，并核对 DetailVersion 数量不变、Assignment 历史分别为 current→target 和 current→target→bad→restored；2026-09-11 实测 25.20 秒通过。此旅程与 20K MySQL 容量合同共同关闭 S22 自动化退出门；真实站点长期契约仍按 Live Smoke/Nightly/Weekly 分层持续观察。紧凑证据见 `evidence/recruiting-live-recipe-rollout-batch-20260911.json`。

执行状态补充（2026-09-11，Profile 首次登记）：新增公开 `recruiting.profile.register`，补上此前只能预置数据库事实的运营缺口。普通运营员提交 Profile ID、安全域、授权 Tool Actor 和 canary Recipe Resource；Actor 重新读取 Resource、执行严格 ABI 解码、重算 content/contract hash，并限定 exact HTTPS 域、`browser.profile.repair` 和 Listing/Detail kind。SecretRef 由控制面按本地槽位约定生成，客户端不能提交 Cookie、密码、OTP 或 SecretRef。单一 MySQL 事务把新 Profile 保存为不可调度的 `repairing@v2`，创建零 affected production Work 的 `profile_unprovisioned` Incident、唯一人工 Repair Work、稳定 receipt 和两条 outbox 事件；任何冲突全部回滚。公开 Server E2E 已改为真实 Resource→register→repair.begin→重启查询，不再直写 Profile/Incident 种子；非 root MySQL 合同验证原子重放、秘密/设备不进入响应和事件、且没有伪造受影响抓取任务。设备 registry 初始 `v1` 仍是 owner-only 本地部署步骤；真实第三方授权账号和生产出站隔离仍是外部准入门。

执行状态补充（2026-09-11，Profile 本地槽位初始化）：`atoll-recruiting-extension-bridge` 新增只执行一次即退出的 `--provision-profile` 模式，把设备侧手工拼 registry 收敛为正式部署动作。命令创建/收紧 owner-only Chrome user-data-dir，产生 256-bit 随机绑定令牌到一个全新的 `0600` 文件，registry 只持久化 SHA-256；标准输出只返回 Profile/version 和两个文件路径，不打印令牌。初始版本固定为 `v1`，同 ID 相同事实可重放，不同事实或已存在 token 文件 fail closed。Provision 与修复/验证的 `AdvanceVersion` 共用 registry sidecar `flock`，再以临时文件、fsync、原子 rename 和目录 fsync 发布，避免独立 Bridge/Executor 进程丢更新。单元与 race 测试覆盖认证、确定性排序、精确重放、冲突拒绝及现有 repair bundle 兼容。

## 13. P8：25 场景验收矩阵

每个场景保存独立测试记录：前置数据、用户身份、命令、预期状态转换、注入故障、用户可见结果、数据库断言和 ledger/Artifact 因果链。

| ID | 场景 | 必须自动化的核心断言 |
|---|---|---|
| S01 | 单个公司新增 | command replay、业务去重、0/1/N Source |
| S02 | 批量导入公司 | 文件 hash、重复行、部分失败、取消续跑、人工 retry/skip、父级事实重聚合 |
| S03 | Source Discovery | 候选证据、拒绝、generation、登录阻塞 |
| S04 | 人工维护 Source | canonical key、草稿验证、原子发布/回滚 |
| S05 | 首次全量 | 10K 分页、staging、崩溃恢复、阶段完成 |
| S06 | 第二次校准 | 四项独立结论，无更新时保持 unverified |
| S07 | 每日列表增量 | frontier+overlap、同时间组、Checkpoint CAS |
| S08 | 新岗位详情 | 详情幂等、跨 Source 映射、失败不退边界 |
| S09 | 变化岗位详情 | refresh generation、内容 hash、旧版本保留 |
| S10 | Listing 修复 | candidate Assignment、灰度、兼容矩阵、回滚 |
| S11 | Detail 修复 | 共享失败单飞、积压有界恢复、单岗位特例 |
| S12 | Profile 修复 | 秘密隔离、共享熔断、旧 Attempt fencing |
| S13 | Company 更新 | 稳定 ID、官网复核、并发更新和回滚 |
| S14 | Company 合并/拆分 | preview hash、逻辑映射、历史引用不改写 |
| S15 | Company 暂停/恢复 | 三种 pause mode、截点竞态、一次 catch-up |
| S16 | Source 暂停/恢复 | 其他 Source 不受影响、Checkpoint 保留 |
| S17 | Source redirect/改归属 | identity 判据、谱系、cutover、兼容性 |
| S18 | Company 归档/合规删除 | 逻辑归档恢复；硬删除只走 M5 流程 |
| S19 | Source 归档/恢复 | 历史保留、相同 URL 提示恢复、重新校准 |
| S20 | 数据纠正与重算 | override 优先级、plan hash、派生血缘 |
| S21 | 历史回填 | 两种模式、缺口报告、不推进 Checkpoint |
| S22 | Recipe 批量升级 | Assignment canary、逐项发布、失败回滚 |
| S23 | 临时手工运行 | 三种 run mode、timer 并发、预算与取消 |
| S24 | 重试/人工结案 | 失败分类、策略快照、resolution、recovered |
| S25 | 10K/20K 日常运行 | 全部 occurrence 有结果、详情放大、无热点失控 |

执行状态补充（2026-09-11，S21 有界历史回填预览）：migration 38 和纯领域状态机已经建立独立 Backfill/Item/Output 血缘，公开 `backfill.create/get/items/confirm` 接通普通用户控制边界。`artifact_recompute` 按请求半开时间范围枚举每个已接受 DetailVersion，同一 Job 的多个历史版本不会折叠，并冻结原始 DetailVersion、Artifact 与 observed time；`live_refetch` 按范围内 Listing Observation 选择唯一 Job，不携带历史输入且不能声称历史快照。预览复用现有 Recruiting Actor durable reconcile timer，每个活动 Backfill 每 tick 最多推进 500 项；滚动 SHA-256 同时绑定请求事实和全部冻结项，不需在最终确认时把全量成员载入内存。最后一块与父 Work `waiting_human(preview_ready)`、内部 receipt/event 原子提交；确认要求 Backfill/Work 双 version 与精确 preview hash。非 root migration/runtime MySQL 8.4 合同已验证同 Job 两个历史版本、实时选择、seek 分页、确认状态和 Checkpoint 行数不变；Atoll 核心冻结检查通过。执行、运维和取消进展见下一段；真实 Server/Executor 网站旅程仍待完成，因此 S21 继续保持进行中。

执行状态补充（2026-09-11，S21 执行、结果与有界运维）：migration 39—41 把预览成员进一步冻结到 Company/Source/Job/Recipe/Profile/原始 Artifact 围栏，并把 Backfill confirmation version 纳入 Attempt 接受契约。`artifact_recompute` 只从冻结的 Atoll response Resource 离线重放 Detail Recipe，输出 derived Artifact；`live_refetch` 复用既有 Detail Driver 访问当前页面，但结果进入独立 BackfillOutput，二者都不更新 Job、DetailVersion 或 Checkpoint。统一 Executor 仍只有一个 class，回填只是新的 Work purpose/offer kind；预算在原 global/capability/origin/company/Profile 维度之外增加 `workload=backfill` 子上限，避免历史任务挤占每日增量。非 root MySQL 合同已经逐项执行两条历史版本和一次实时重取，验证 Output/Artifact 血缘、命令重放、结果 hash、父子聚合、独立预算释放，以及当前 Job/Checkpoint 零变化。

人工处置现通过 `backfill.item.resolve` 支持 `retry|accept_gap`：retry 终结旧 waiting-human Work 并创建有因果链接的新 Work，永不重开旧生命周期；预队列版本围栏失败禁止复用已经过时的预览，只能接受缺口或重新建批次。`backfill.pause/resume` 使用 drain 语义，暂停时不产生新物化/offer，但已运行子 Attempt 可按原确认围栏结算。`backfill.cancel` 先提交 `canceling` 栅栏，再由既有 durable reconcile timer 每轮最多 500 项、每项小事务取消 pending/queued/failed Item，拒绝活动 Attempt、释放 Permit，全部收口后才取消父 Work；两项、每页一项的合同证明首批不会提前宣称完成，运行中取消和迟到结果 rejected Artifact 也有覆盖。`backfill.outputs/output.get/gaps` 分别提供元数据分页、最多 1 MiB 的单条正文和失败/accepted-gap 审计报告，避免列表响应装载无界结果。当前尚缺普通用户真实 Atoll Server/Executor 对公开网站的两模式纵向旅程以及最终全套回归，因此 S21 仍为进行中。

S01—S24 至少有 model/Repository/actor 测试中的一种自动化覆盖，并有一个端到端用户旅程覆盖其关键交互；不可安全施加给第三方的并发和故障全部在本地 fixture 或测试数据库中完成。

## 14. P9：容量、可靠性和生产准入

执行状态（2026-09-11）：新增版本化 workload manifest、`make recruiting-capacity` 入口和真实 MySQL 8.4 容量合同。L0—L4 已在 4 vCPU/15 GiB 参考机、隔离非 root 数据库和有界 tmpfs 上全部通过；L2 实际写入 10,000 Company、20,000 Source、20,000 Occurrence、20,000 Listing Work 与 40,000 Detail Work，L3/L4 各写入 400,000 Detail Work，L4 另将其中精确 40,000 条推进为 `waiting_retry`，数据库终态精确为 380,000 `open` 与 40,000 `waiting_retry`。测试验证分桶、500 条渐进物化、业务键唯一和 `capacity.status` 每状态 5,000 行扫描上限的精确/截断语义。L2 又分别按 8 小时/8 tick、24 小时/24 tick 和 96 分钟/8 tick 运行；96 分钟承载与 8 小时相同的日工作量，构成 5 倍到期速率。三者的计划/物化/总耗时分别为 14.160/53.345/114.296 秒、14.235/52.656/113.918 秒和 14.219/52.878/113.399 秒。L3 为 14.206/53.379/344.639 秒；加强状态断言后的 L4 为 13.981/53.822/347.350 秒。详情放大、重试积压与到期压缩没有反向放大日切计划时间。该结果证明数据库计划/物化路径可承载目标规模，但不包含真实网络执行吞吐，不能据此宣称 P9 完成；Executor/Artifact/ledger 指标、外部执行 5 倍峰值、故障矩阵、备份恢复和三层真实网站持续验证仍是退出项。机读证据见 `evidence/recruiting-capacity-l0-l4-20260911.json`。

备份恢复进展（2026-09-11）：新增 `make recruiting-backup-restore`，在一次性 MySQL 8.4 上以 migration/runtime 分离的非 root 身份建立源库和全新恢复库。源库通过正式 Repository 原子写入 Company、Work、两个稳定 receipt、两个领域 event intent 和一个 execution dispatch；脚本用一致性快照逻辑备份，恢复后在另一个测试进程中只以 runtime 身份核对领域状态 JSON、两类 outbox、完整 migration ledger 和命令重放，且再次证明 runtime 无 DDL 权限。正式演练的 90,553 字节 dump 在 394 ms 内恢复，端到端 9.634 秒；凭证未进入 dump。该合同关闭“能否从干净库恢复控制面因果”的机制缺口，但不替代生产数据量 RTO/RPO、加密异地保留、binlog 时间点恢复、Atoll ledger 和 Artifact provider 的联合恢复演练。证据见 `evidence/recruiting-backup-restore-20260911.json`。

### 14.1 负载模型

至少运行以下可复现档位：

| 档位 | Company | Source | 详情放大 | 用途 |
|---|---:|---:|---:|---|
| L0 | 100 | 200 | 0/Source | 快速回归 |
| L1 | 1,000 | 2,000 | 2/Source | 索引和并发调优 |
| L2 | 10,000 | 20,000 | 2/Source | 正常目标负载 |
| L3 | 10,000 | 20,000 | 20/Source | 400K Detail Work 放大场景 |
| L4 | 10,000 | 20,000 | 20/Source + 10% 重试 | 故障与恢复压力 |

分别测试 24 小时平铺、8 小时窗口和 5 倍峰值。HTTP/Browser 比例、执行时长、Artifact 大小和失败分布写进 workload manifest，不使用无法复现的“模拟大量数据”。

### 14.2 必测指标

- SourceOccurrence：expected、materialized、started、checkpoint committed、exception、missing；
- 队列：最老 runnable age、deadline miss、按 capability/origin/Profile 的积压；
- Executor：利用率、P50/P95/P99、退出/过期、拒绝的陈旧结果；
- MySQL：QPS、事务时间、锁等待、deadlock、连接池、索引命中、库大小；
- ledger：消息/日、字节/日、Channel replay/restart 时间；
- Artifact：对象数、字节、上传失败、脱敏失败；
- 业务：新增/更新、详情成功、accepted gap、边界缺失、排序违约；
- 修复：故障域数量、受影响 Work、单飞命中、恢复速率和告警数量。

正确性门槛是绝对条件：所有 expected occurrence 均有可解释结果；零已接受事实丢失；零陈旧覆盖；零重复有效业务结果；预算和 Profile 并发零越界。吞吐和延迟在首次 L2 基线后冻结为参考环境 SLO，未记录硬件、版本和 workload manifest 的数字不得成为架构依据。

如果单 Recruiting Actor 或单 Channel 达不到目标，按以下顺序处理：缩短事务、修正索引、减少进度消息、使用紧凑 batch envelope、增加 Executor 实例。仍失败则形成容量阻塞报告；不修改 Atoll core，不擅自改变 Channel/Actor 架构。任何产品内部路由或分片方案必须另行更新产品设计并获得确认。

### 14.3 故障注入

在以下每个切点 kill 进程或断开依赖：Work 创建前后、Attempt offer/accept 后、外部请求完成前后、Artifact 上传前后、Observation 写入、Detail 意图生成、Checkpoint CAS、领域事件 append、日报关闭、migration 中断和备份恢复。

还要注入 MySQL deadlock/只读/连接耗尽、Atoll 暂时不可用、Executor incarnation 更换、timer 重复/延迟、Recipe 发布竞态、Profile 修复竞态、429/403/5xx、Browser crash 和 Artifact provider 不可用。

每次注入必须得到一种确定结果：未发生、已完整提交、被 fencing 拒绝、或可由 reconcile 完成；禁止出现需要手工改数据库才能解释的状态。

## 15. 真实网站验证计划

### 15.1 安全边界

- 只访问公开招聘信息或明确授权账号；
- 默认 origin 单并发、保守间隔，遵守条款、robots、429 和 Retry-After；
- 不绕过验证码、登录、付费墙或访问控制；
- 不执行投递、注册、发信等第三方写操作；
- 并发、崩溃、重复提交和数据库故障只对本地 fixture 施加；
- 真实 Cookie/凭证只进入授权 Profile provider，不保存到 Git、Message、普通 Artifact 或测试报告。

### 15.2 三层运行

| 层级 | 频率/规模 | 内容 | 失败处理 |
|---|---|---|---|
| Live Smoke | 每次 Recipe/Driver 变更，1—3 站 | discovery、首屏/少量分页、1—3 个详情、diagnostic | 阻断相关 Recipe 发布 |
| Nightly Canary | 每晚，5—10 站 | 已发布 Recipe、边界证明、字段质量、限流 | quarantine 对应 Assignment，聚合告警 |
| Weekly Coverage | 每周，20—50 站 | 站型、地区、语言、分页、Profile、完整基线抽样 | 形成覆盖与风险报告 |

执行进展（2026-09-11）：`make recruiting-live-nightly`、`make recruiting-live-weekly` 及版本化 `recruiting-live-sites.v1` 清单已经落地。分层 runner 只接受 `greenhouse|lever` 两种声明式 provider、精确 HTTPS URL 和 RFC3339 复核时间；每个目标固定 GET、单页、2 MiB、robots、合规证据和目标间 2 秒串行间隔，任一失败保存独立报告后继续，整批最终以非零状态阻断。首次 Nightly 在干净提交 `e4ff0ebb` 上真实运行 5 站，全部完成网络与 Artifact 诊断，共观察 711 个岗位。首次 Weekly 的 20 目标全部执行，准确发现 Highspot、Plaid 两个旧 board token 返回 404 并使整批失败；经独立验证后以 Reddit、Duolingo 替换，再在干净提交 `ab2292bc` 上重跑，20/20 完成诊断，共观察 3,967 个岗位。19 个 Greenhouse 快照均因实际活动时间非倒序而被质量闸门拒绝，Palantir Lever 的 100 条有界样本通过快照质量；全部目标仍因 update-retop 无历史证据而保持 `production_incremental_eligible=false`，没有提交 Checkpoint。该结果关闭 Nightly/Weekly 工具和首次运行门，并证明站点失败隔离、报告聚合及清单修复闭环，不把“可访问”混同为“可可靠增量”。机读证据见 `evidence/recruiting-live-nightly-20260911.json` 与 `evidence/recruiting-live-weekly-20260911.json`。

候选样本沿用产品设计：腾讯/百度等大型 SPA，爱奇艺/网易等独立站，Moka/飞书招聘/北森等 ATS，ASML/Greenhouse 等国际站，以及当前无职位的官方入口。实际 URL、允许访问方式和确认时间必须版本化，不依赖固定岗位总数。

每次保存：站点与 origin、时间、Recipe/Assignment/contract hash、HTTP 状态、trace、分页和身份统计、边界证明、字段/去重统计、必要的脱敏截图、预算决定、失败分类和人工抽样结论。

“历史岗位更新后重新置顶”不得通过修改第三方网站强制制造。只有观察到真实更新、站点提供正式契约，或在获授权的测试租户中完成可控实验后，才能从 unverified 变成 verified；在此之前该 Source 不进入“严格可靠增量”集合。

## 16. 自动测试命令与 CI 分层

计划在实现过程中提供以下稳定入口：

```bash
# 招聘纯领域与 actor 快速测试
go test -race ./drivers/tools/recruiting/... ./drivers/tools/recruitingexecutor/...

# MySQL contract/integration；连接到本次创建的隔离测试库
go test -tags recruitingintegration -race ./drivers/tools/recruiting/store/... -count=1

# 招聘真实进程 e2e
go test ./e2e -run '^TestRecruiting' -count=1 -v -timeout 20m

# 显式访问真实 Greenhouse 公共 API 的 server/daemon 全链路
./scripts/recruiting-live-e2e.sh

# 全仓回归与架构边界
make test
make lint
make test-full
make e2e-loop

# 显式、低频、可审计的外部验证
make recruiting-live-smoke
make recruiting-live-nightly
make recruiting-live-weekly
make recruiting-capacity
make recruiting-backup-restore
```

CI 层级：

1. PR fast：boundary check、format/vet、纯 model、actor、fixture、Repository contract；
2. PR merge gate：`make test-full`、招聘 e2e、migration from empty、race；
3. nightly：真实 MySQL、进程 kill、Nightly Canary、中型 L1；
4. weekly：Weekly Coverage、L2/L3、备份恢复；
5. release：全部 25 场景、L4、权限/秘密扫描、升级与回滚演练。

第三方网站暂时不可用不等同于代码失败：先由保存的 Artifact 和 fixture replay 判定代码回归或站点变化。真实站点失败可以令 Live job 进入 quarantine，但不能被静默忽略。

## 17. PR 切分与完成标准

建议按以下可独立回滚的 PR 顺序交付：

1. 边界脚本、目录骨架和 P0 探针；
2. 领域类型、状态机、命令 schema 和不变量测试；
3. MySQL Repository 接口、schema ADR 和 migration；
4. command receipt、CAS、Work/Attempt、Artifact 和恢复意图；
5. Actor 查询/控制词和权限 e2e；
6. Executor 协议、HTTP Recipe、fixture server；
7. Browser/Profile/Extension adapter；
8. discovery 和 Source validation；
9. baseline generation 和详情同步；
10. SourceOccurrence、Daily Run 和增量 Checkpoint；
11. Work Center API/UI；
12. RepairIncident、Recipe rollout/rollback 和 Profile 修复；
13. 数据纠正、回填、逻辑合并和归档恢复；
14. 25 场景、真实网站、容量、安全和运行手册。

每个 PR 的 Definition of Done：

- 关联产品场景和不变量；
- 生产代码、负向测试、重启/重复测试和文档同步提交；
- 新 Message/Recipe/schema 有版本及兼容说明；
- 无冻结目录修改，`git diff --check`、boundary check、相关 `go test -race` 和 `make lint` 通过；
- 无 root 数据库连接、无秘密、无未经脱敏的真实 Artifact；
- 失败和未完成项明确记录，不以“后续补测试”关闭正确性问题。

## 18. 发布、回滚与运行准备

发布顺序：本地 fixture → 隔离 MySQL → 单 Source diagnostic → 单 Source production → 10 Source canary → 100 → 1,000 → 10,000 Company。每级至少观察一个完整运行窗口，前一级未通过不得扩大。

发布前必须具备：

- migration 备份、恢复点和旧版本读取验证；
- Recipe/Assignment 一键 quarantine 与逐 Source rollback；
- 停止产生新 occurrence、drain/cancel Work 和保护 Checkpoint 的运行命令；
- origin/Profile/Browser 紧急熔断；
- Daily Run 未覆盖清单、告警接收人和人工值班流程；
- MySQL、Artifact、Atoll ledger 的独立备份与恢复演练；
- 数据保留、访问审计、Profile 秘密轮换和合规删除程序。

应用回滚不得回滚或删除已接受的招聘事实。旧二进制只有在 schema/Message/Recipe 兼容检查通过后才能启用；否则停止新调度、保留证据，并执行前向修复。Recipe 回滚按 Source Assignment 和对应 Checkpoint lineage 进行，不兼容时重新校准。

## 19. 风险与决策门

| 风险 | 当前处理 | 决策门 |
|---|---|---|
| Atoll v0.06 非 data-safe | 招聘数据在独立 MySQL；Atoll ledger 仍需备份演练 | 未证明升级/恢复前不得生产承诺 |
| Jobs/审批/配额尚未通用实现 | 作为 Recruiting 领域能力实现 | 不向 core 反向抽象 |
| Agent 调用 actor 工具链尚未完全暴露 | UI/直接 Message 可先完成确定性闭环 | P0 验证，不通则不假装 Agent 闭环 |
| 单 Recruiting Actor/Channel 吞吐 | 短事务、Resource 数据面、紧凑消息、容量实测 | L2/L3 后决定，禁止先改 core |
| 详情重叠放大到 400K Work | 量测、预算、分批派生和 backpressure | 超预算不推进或转人工，不静默漏采 |
| 第三方页面变化和访问限制 | Recipe 版本、真实 canary、熔断、人工修复 | 禁止绕过访问控制 |
| 更新置顶无法短期观测 | 保持 unverified | 有真实证据后才能标 verified |
| MySQL 与 ledger 非原子 | 可重放领域事件意图和 reconcile | 所有 crash cut 通过后放行 |
| Browser/Profile 秘密泄露 | 安全 provider、设备域、脱敏和权限测试 | secret scan/越权测试零发现 |

## 20. 最终交付清单

- 产品设计、开发计划、schema ADR、Recipe ABI、消息契约；
- Recruiting Actor 和唯一 Executor actor class；
- MySQL migration、Repository、备份恢复与数据保留手册；
- HTTP、Browser、Profile、Extension 和 Artifact adapter；
- Company/Source/Job、基线、增量、Work/Attempt、修复和人工运维能力；
- Work Center UI 与权限矩阵；
- S01—S25 验收报告及自动测试映射；
- Live Smoke/Nightly/Weekly 版本化样本和报告；
- L0—L4 容量报告、故障注入报告和参考环境 SLO；
- 上线、监控、值班、quarantine、回滚和灾难恢复 runbook；
- 核心冻结检查报告，证明 Atoll 核心代码和架构零修改。

只有上述清单都有可复现证据，并且 P9 正确性门槛全部满足，Atoll Recruiting 才可以从“产品与架构可行”转为“可生产运行”。
