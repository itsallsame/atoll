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

执行状态：进行中。Company、Source、Work 的首批人工运维词，以及 Job/DailyRun/Occurrence/System/Capacity 查询已通过真实 Portal 或隔离 MySQL 纵向验收；System/Capacity 以只读一致性事务投影状态、runnable/deadline、活动预算与双 outbox backlog，不引入容量 Actor。Atoll durable timer 已接入可信的每日原子截点名单、按最早 `due_at` 驱动的 Work 渐进物化，以及窗口末自动闭账。首个专用人工运行词 `run.join_occurrence` 已通过普通用户 Portal 与 MySQL 合同：它以 occurrence version 把一个尚未到期的 planned 项原子物化进同一执行队列，并与 timer 物化并发收敛。统一 Attempt 生命周期已按 capability/origin/priority 领取 listing 或 detail Work，具备单活动执行权、不可变 offer、Executor incarnation 和领域 fence；有界 listing page、terminal checkpoint result 及 detail result 的原子事务已通过 MySQL 合同测试。Listing Page Progress 已按 Attempt 隔离，并验证部分页面后崩溃的新 Attempt 可从第 1 页重跑且 completion 不混入旧页；Driver 的分类失败与 Failure Artifact metadata 也可随 Attempt/Work/Permit 在同一事务落库。accept/start/failed/page/completion/detail command receipt 已与对应状态事务原子提交，请求哈希绑定 authenticated Executor，并通过并发接受、稳定重放和 command ID 冲突合同。分类失败已使用版本化配置执行有界退避，持久化 Attempt 计数和下一次领取时间，确定性/不可重试/耗尽错误进入 `waiting_human`，对应协作事件也与失败事务原子提交。不含轮询/调度策略的单次 Offer 编排已串起 accept/start、Recipe Resource、HTTP Driver 和结果/失败控制消息，并挂到默认关闭、严格配置、每次仅执行一份 Work 的 Executor wake 入口。控制面现已按 extension config 的 Executor fleet 和 capability 创建持久定向 dispatch；每日渐进物化、人工新增/重试以及 Attempt 成功/失败/未来重试均在对应业务事务中生成 wake，Executor 完成或 idle 后以 authenticated identity 确认，未确认投递由现有 reconcile timer 有界恢复。人工重试每日 listing Work 时，未终结 occurrence 现与新因果 Work、receipt、event 和 dispatch 在同一事务重绑，避免“控制面显示重试成功但执行面无法按新 Work 找到 occurrence”。正常链路、“pending dispatch 已提交、首次投递前 server 退出”和 completion acknowledgement 丢失切点已用普通运营员、真实 server/daemon、Recipe KV、Artifact File、非 root MySQL 和 Greenhouse 公共 API 跑通；completion 重投没有增加 Attempt，并正确以 `quality_rejected` 停在 `waiting_human` 而不推进 Checkpoint。无进展 Attempt 已复用 reconcile timer 做有界、并发安全的 expire/retry 恢复。`run.diagnostic`、独立 `run.production`、主动 incarnation 失效、Executor 处理中退出和业务结果 acknowledgement 丢失 e2e 尚未接通；日报关闭后的 recovered 补偿也仍待实现。其余批量控制词、Work correct 和 Source 验证结果发布仍待实现。证据记录于 `docs/experiments/atoll-recruiting-p3-acceptance.zh-CN.md`。

进展补充（2026-09-09，替代上一段关于 diagnostic/production 尚未接通的陈述）：`run.diagnostic` 与独立 `run.production` 均已接通 ListingRun、统一 Work/Attempt/Permit/dispatch 和 HTTP Executor。diagnostic 使用 evidence-only 结果事务，不进入 DailyRun，也不写 Job、Observation、Detail Work 或 Checkpoint；production 只接受具备已验证增量契约和已有基线 Checkpoint 的 Source，复用有界 page ingestion，并在 completion 以冻结版本 CAS 当前 Checkpoint。MySQL 合同已覆盖 production 成功发布和并发日常运行先推进水位后的 fence；真实 server/daemon、Greenhouse 公共 API、Recipe KV、Artifact File 与非 root MySQL 旅程中，两种独立运行均如实进入 `waiting_human/quality_rejected`，Checkpoint 保持不变。Artifact 采用每 Attempt 独立同层目录，以适配不支持重复/递归建目录的真实设备驱动，同时不修改 Atoll Resource 语义。

进展补充（2026-09-09，公司导入预览、应用与取消）：`recruiting.company.import` 已建立 Resource-backed 纵向切片。命令原子创建父 Work、`CompanyImport`、receipt/event/dispatch；同一 `recruiting-executor` class 以 `company.import` capability 读取 File Resource、校验原始 SHA-256 和 `company-import.v1` CSV，并以最多 500 项的 envelope 提交。控制面用 batch version、连续 chunk sequence、Attempt incarnation 和 Work acceptance 联合 fence，每个分片独立提交，最终不信任 Executor 自报摘要而从已存项目重算 preview hash。中断后按 item count/sequence 续传；预览成功只让父 Work 进入 `waiting_human(preview_ready)`，逐项审阅可用稳定游标分页。`recruiting.company.import.confirm` 以精确 preview hash/version 启动有界应用协调 Work；每项独立事务创建 Company、子 Work、outcome/event，数据库重复转人工而不回滚其他项；超过一页时同一 Work 持久重投，只续跑 outcome 为空的项，最后 item 已提交而页回执丢失时用空 finalizer offer 恢复汇总。`import.cancel` 先推进 batch fence 并取消旧协调 Work，使在途结果失效，再逐页、逐项取消未开始项。普通用户 Portal、真实 server/daemon、daemon File Resource、非 root MySQL 的 E2E 已覆盖两页 apply、命令重放、部分成功以及预览后取消；隔离 MySQL 另覆盖取消正在执行的批次及陈旧结果拒绝。等待人工项的修复与修复后重新汇总尚未实现。

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

Listing Recipe 必须显式声明 identity field 与 detail URL field，不能依赖 `detail_url` 等约定字段名猜测。单个外部页面最多产生 500 条唯一 Observation，与控制面单事务页面上限一致；跨页重复岗位只随首次出现的页面提交，内容冲突使分页质量证明失败。Driver 成功结果保留逐页 URL、下一游标、页面 Artifact、去重后新增 items 和 terminal 标记，再由 Executor 生成逐页提交及独立 ListingDelta completion Artifact。

### 9.2 Driver

- HTTP：超时、响应上限、redirect policy、User-Agent、robots/条款记录、origin 限流、429/403 熔断；
- Browser：隔离 context、页面/内存/时间上限、下载和外部写操作禁用；
- Profile：只传不可解析引用，在授权设备和安全域解析；Cookie、密码、OTP 不进 Channel/AI/普通 Artifact；
- Extension：只负责捕获和人工修复，输出候选 Recipe/Artifact，不直接修改领域数据；
- Artifact：内容哈希、脱敏、大小限制、访问权限、保留期。

Recipe 与 Artifact 只通过 Atoll 已有的公开 `Actor Resource` 接口接入，不新增或修改 core Resource 语义：Recipe 正文是受 Channel 权限保护的小型 KV Resource，offer 只携带 `recipe://`/`artifact://` 不透明引用和预期 SHA-256；Executor 读取后严格解码，并复核 ABI、kind、transport、capability 与内容哈希。原始响应、页面和截图是 File Resource，Executor 在字节上限内流式写入并提交，领域消息和 MySQL 只保存 ResourceID、内容哈希、访问范围与保留策略。Resource 拒绝、缺失、超量或哈希不符均 fail closed，不能降级为把正文塞进 Message、State 或数据库。

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

进展补充（2026-09-09，baseline 中断隔离）：migration 20 为 staging 行增加 `attempt_id`，并让 finalized BaselineGeneration 冻结唯一成功的 `listing_attempt_id`。页间退出后新 Attempt 从第 1 页重扫；completion 只统计当前 Attempt，物化也只读取被冻结 Attempt 的行，旧 Attempt 的独有岗位键不能混入基线或 Checkpoint。隔离 MySQL 合同已用“旧 Attempt 留 1 行、重试 Attempt 产生 2 个不同岗位”的不利场景证明最终只物化后者；10,000 条 legacy/fixture staging 路径仍保持 20 个短事务。真实 Executor 进程退出切点仍待补齐。

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

## 13. P8：25 场景验收矩阵

每个场景保存独立测试记录：前置数据、用户身份、命令、预期状态转换、注入故障、用户可见结果、数据库断言和 ledger/Artifact 因果链。

| ID | 场景 | 必须自动化的核心断言 |
|---|---|---|
| S01 | 单个公司新增 | command replay、业务去重、0/1/N Source |
| S02 | 批量导入公司 | 文件 hash、重复行、部分失败、取消续跑 |
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

S01—S24 至少有 model/Repository/actor 测试中的一种自动化覆盖，并有一个端到端用户旅程覆盖其关键交互；不可安全施加给第三方的并发和故障全部在本地 fixture 或测试数据库中完成。

## 14. P9：容量、可靠性和生产准入

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
