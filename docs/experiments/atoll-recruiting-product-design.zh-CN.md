# Atoll Recruiting：招聘数据持续采集产品设计

状态：产品与架构设计草案

版本：v0.2

日期：2026-09-07

目标规模：10,000 家公司，支持向百万级任务/日演进

## 1. 文档目的

本文定义一个遵循 Atoll 设计思想和架构原理的招聘数据持续采集产品。Atoll 是系统结构、运行语义和安全边界的唯一权威；Snowland 与 Staircase 只提供业务场景、真实数据样本、站点经验和失败案例，不构成新系统的架构依赖。

本文用于统一以下问题：

- 产品解决什么问题，第一阶段不解决什么问题；
- Atoll 与招聘领域模块分别拥有什么状态；
- 用户、Agent、调度器和 Browser Worker 如何协作；
- 一万家公司和百万级任务如何调度；
- 失败、重试、人工审批和数据一致性如何保证；
- 第一阶段如何交付和验收。

## 2. 一句话定位

Atoll Recruiting 是一个通过自然语言协作、以确定性 Recipe 执行为生产核心、能够持续发现和修复采集异常的招聘数据平台。

```text
用户通过 Web / 飞书提出目标或处理异常
              ↓
Atoll Actor / Channel / Message 组织全部业务行为
              ↓
Recruiting Actor 通过 Timer / Resource 推进领域状态
              ↓
Browser Worker Actor 通过 Driver 执行真实网页采集
```

## 3. 背景与前提

### 3.1 已获得的业务证据

Snowland 和 Staircase 是同一业务场景的探索项目，为新产品提供以下经验性输入：

- 真实业务对象包括公司、招聘入口、职位列表、职位详情和招聘类型；
- 真实网站存在 SPA、API、分页、登录态、ATS、多语言和页面变化等差异；
- Recipe、浏览器插件和持久 Profile 是值得继续验证的执行手段；
- 正常采集、异常修复和人工处理是三个不同成本的业务路径；
- Web IM 和飞书可以作为用户指令、通知与仲裁入口。

这些结论是需求假设和测试语料，不自动成为目标领域模型。Company、URL、Recipe、Task 等概念仍须根据 Atoll 原语、真实网站测试和新系统不变量重新定义。

### 3.2 实施前提

- 两个试验项目都不是必须原样迁移的生产系统；
- 新系统可以重新定义数据库、API、ID 和可靠性边界；
- 可以复用经过验证的代码，但不得继承未经确认的状态语义和一致性缺陷；
- 第一阶段运行在一个 Atoll trust domain 内；
- Atoll 当前是 pre-release，正式生产前必须完成数据安全和恢复验收。

### 3.3 设计权威顺序

发生冲突时按以下顺序裁决：

1. Atoll 的身份、消息、生命周期、权限、恢复和分层不变量；
2. 本文冻结的招聘领域不变量和验收标准；
3. 真实招聘网站产生的可复现实证；
4. Snowland/Staircase 的历史设计和代码。

Snowland/Staircase 的实现不得反向要求 Atoll core 引入招聘领域概念。历史代码只有在成为受 Atoll Actor 约束的叶子模型或 Driver 后才可复用。

## 4. 产品目标

### 4.1 核心目标

1. 长期维护至少 10,000 家目标公司及其招聘入口。
2. 按刷新策略持续采集职位列表和职位详情。
3. 正常生产不依赖 LLM 逐页判断。
4. 页面变化或质量异常时，自动保存证据、诊断并验证修复方案。
5. 自动处理无法收敛时，通过 Atoll 请求用户仲裁或审批。
6. 所有重要操作都能回答：谁发起、谁执行、用了哪个 Recipe、产生了什么结果。
7. Worker 数量可以水平扩展，单个 Worker 或服务重启不得破坏业务事实。

### 4.2 非目标

第一阶段不包括：

- 绕开 Atoll 另建第二套身份、权限、控制入口和编排中心；
- 把页面正文、截图、网络包和每次内部轮询直接写入 Channel ledger；
- 为每家公司、每个 URL 或每个任务创建 Channel；
- 让 LLM 直接访问或修改领域数据库；
- 承诺对所有招聘网站绕过登录、验证码或反自动化机制；
- 建设完整的数据分析仓库、推荐算法或商业化客户系统；
- 在未经容量测试前承诺跨地域强一致调度。

## 5. 用户与核心场景

### 5.1 用户角色

| 角色 | 主要职责 |
|---|---|
| 运营人员 | 添加公司、查看状态、触发运行、处理普通异常 |
| Recipe 维护者 | 检查失败证据、验证和批准 Recipe 新版本 |
| 系统管理员 | 管理 Worker、容量、凭证、权限和系统策略 |
| 数据使用者 | 查询和导出结构化职位数据 |
| Recruiting Agent | 理解意图、调用白名单能力、汇总结果和提出仲裁问题 |

### 5.2 用户旅程：新增公司

```text
用户：持续跟踪某家公司
  → Agent 解析公司和可选 URL
  → Recruiting Tool 创建公司目标
  → URL Discovery 或人工 URL 校验
  → 生成并验证 Recipe
  → 用户批准高风险的新 Recipe
  → 公司进入正常调度
  → Channel 收到首次采集摘要
```

### 5.3 用户旅程：每日运行

```text
Atoll durable timer 唤醒 Planner
  → Planner 分批选择到期 URL
  → 创建或复用幂等任务
  → Worker 领取任务并执行
  → 结果事务提交
  → 更新下一次运行时间
  → Atoll 收到批次摘要
```

### 5.4 用户旅程：失败修复

```text
任务失败或质量检查不通过
  → 保存结构化 Failure Evidence
  → 确定性规则先分类
  → 可重试错误进入退避队列
  → 页面结构错误进入 Recipe Repair
  → 新 Recipe 小样本验证
  → 低风险自动发布 / 高风险请求审批
  → 重跑原任务
  → 多轮失败后请求人工仲裁
```

## 6. 核心设计原则

### 6.1 Atoll 是系统底座，领域状态由 Actor 拥有

所有外部业务命令、Actor 间控制交互和可观察领域事件都通过 Atoll Message 发生，并受 Channel membership 和 capability 约束。消息先进入持久 ledger，再交付给 Actor。

Recruiting Actor 是公司、URL、Recipe、任务、Attempt 和职位状态的唯一行为权威。大量结构化数据可以保存在 Actor 控制的领域数据库 Resource 中；这是一种数据面委托，不是独立于 Atoll 的第二套控制面。

### 6.2 确定性优先，AI 处理例外

- 已验证 Recipe 的执行不调用 LLM；
- 能通过 HTTP/API 完成的工作不启动浏览器；
- 能通过规则判断的失败不调用 AI；
- AI 产生的 Recipe 必须经过真实执行验证；
- AI 不直接写数据库，只能调用白名单领域命令。

### 6.3 业务状态与执行状态分离

- Company 状态表达是否参与业务运行；
- Company URL 状态表达一个招聘入口的事实进度；
- Task 状态表达一次计划工作的生命周期；
- Attempt 状态表达一个 Worker 的一次具体执行；
- Recipe 状态表达一个采集资产是否可用于生产。

### 6.4 至少一次执行，结果幂等生效

分布式执行允许任务重复领取和结果重复提交，但同一个业务结果不得重复生效。系统不以网络层的“恰好一次”作为正确性前提。

### 6.5 Pull + Lease

Worker 主动领取符合自身能力的任务。调度器不依赖向特定 Worker 推送，领取结果由有过期时间的租约保护。

### 6.6 限流优先于扩容

系统必须保护目标网站、登录 Profile 和自身数据库。增加 Worker 不能突破站点预算和熔断策略。

### 6.7 聚合通知

Atoll ledger 记录具有控制、因果或协作价值的消息。页面正文、截图、网络响应和大批量职位作为 Resource 保存，消息只携带稳定引用、摘要和内容哈希。Worker 内部的页面事件与轮询不提升为领域消息。

### 6.8 用 Atoll 原语表达系统

| Atoll 原语 | 招聘产品中的含义 |
|---|---|
| Actor | 人、Recruiting Agent、领域服务、Planner、Worker、Repair Agent |
| Channel | 运维、执行分片、人工审核等权限与协作边界 |
| Message | 命令、任务领取、结果提交、状态事件、审批和回复 |
| Resource | 领域数据库、Recipe、截图、原始响应、导出文件 |
| Timer | 到期扫描、租约回收、重试、熔断恢复和摘要生成 |

Actor 私有内存不是事实来源；REST 路由不是内部业务总线；prompt 不是权限系统；进程身份不是稳定 Actor 身份。

## 7. 总体架构

```text
┌──────────────────────────────────────────────────────────┐
│ Atoll Node                                               │
│                                                          │
│ recruiting-ops Channel                                   │
│   Human ⇄ Recruiting Agent ⇄ Recruiting Service Actor    │
│                                  │ peer messages          │
│                 ┌────────────────┴───────────────┐        │
│                 v                                v        │
│ recruiting-exec.N Channel             recruiting-review  │
│   Planner / Dispatcher Actors          Human / Repair     │
│   Browser Worker Actors                Approval messages  │
│                                                          │
│ Ledger / Membership / Access / Timer / Resource / Device  │
└────────────────────────────┬─────────────────────────────┘
                             │ resource data plane / drivers
                             v
┌──────────────────────────────────────────────────────────┐
│ Actor-owned Resources and External Effects               │
│ Domain DB / Recipe / Evidence / Object Store              │
│ HTTP / Chromium / Extension / authorised AI provider     │
└──────────────────────────────────────────────────────────┘
```

不存在独立于 Atoll 的 `Recruiting Control API`。Web、飞书、Agent、Planner 和 Worker 发起的控制动作都进入 Channel，由目标 Actor 的公开词处理。大对象上传下载走 Resource data plane，完成后以携带引用和哈希的 Message 提交结果。

### 7.1 所有权边界

| 能力 | 权威所有者 |
|---|---|
| 用户、Agent、工具身份 | Atoll |
| Channel、成员、访问权限 | Atoll |
| 对话、审批过程、运行摘要 | Atoll ledger |
| 可靠唤醒 | Atoll timer |
| 公司、URL 和刷新策略 | Recruiting Service Actor，状态存于其 Resource |
| Recipe 和版本 | Recruiting Service Actor，内容存于 Resource |
| Task、Attempt 和租约 | Dispatcher Actor/领域模型，状态存于 Resource |
| Worker 业务能力与任务匹配 | Dispatcher Actor |
| Worker 身份、所在设备和 incarnation | Atoll |
| 职位 URL、详情和质量事实 | Recruiting Service Actor，数据存于 Resource |
| 截图和大型原始证据 | Resource data plane，消息保存引用与哈希 |

### 7.2 模块与依赖方向

目标实现遵循 Atoll 现有领域实验的分层方式：

```text
纯 Recruiting model
        ↑
Recruiting Actor adapter
        ↑
Atoll registry / channel composition

Browser/HTTP/AI Driver → 只实现外部效果
Web Console           → 只调用公开领域词
```

- 纯领域模型不依赖 Atoll runtime、网络、墙钟或具体数据库；
- Actor adapter 把 Message 翻译成领域命令，把领域事件写回 Channel；
- Resource adapter 持久化大规模领域状态；
- Driver 不拥有业务决策，不直接接受绕开 Actor 的用户命令；
- Atoll `protocol/`、`runtime/`、`platform/` 不 import Recruiting 领域包；
- Web 界面不能通过私有数据库接口绕开公开消息协议。

## 8. Atoll 产品映射

### 8.1 Channel 规划

第一阶段默认建立：

- `recruiting-ops`：日常指令、状态和运行摘要；
- `recruiting-review`：Recipe 审批、登录问题和疑难异常；
- `recruiting-dev`：可选，调试和试验环境。

只有权限边界、参与人员或上下文明显不同时才创建新 Channel。

### 8.2 Actor 规划

| Actor | 类型 | 职责 |
|---|---|---|
| Recruiting Agent | agent | 理解意图、选择工具、组织回复 |
| Recruiting Tool | tool | 暴露受控领域命令和查询 |
| Worker Fleet | tool | 查询容量、Worker 健康和扩缩容状态 |
| Notification Adapter | tool | 对接飞书等外部消息系统 |
| Human Operator | human | 发起操作、审批和仲裁 |

### 8.3 第一批公开词汇

```text
recruiting.company.add
recruiting.company.pause
recruiting.company.resume
recruiting.company.status
recruiting.company.list

recruiting.run.start
recruiting.run.status
recruiting.batch.status

recruiting.failure.list
recruiting.failure.inspect
recruiting.failure.retry

recruiting.recipe.inspect
recruiting.recipe.validate
recruiting.recipe.approve
recruiting.recipe.reject

recruiting.jobs.search
recruiting.system.status
recruiting.capacity.status
```

这些词汇属于领域协议，不加入 Atoll 通用 `protocol/message` 的系统闭集。

### 8.4 当前能力缺口

Atoll 的通用 Jobs、Approvals 和 Quotas 尚未交付时：

- Task 语义由 Recruiting 领域模型定义，并由 Atoll 中的 Dispatcher Actor 执行；
- Recipe Approval 由领域命令和 Atoll 消息共同实现；
- Quota 由 Dispatcher 的预算系统实现；
- 后续通过适配器迁移到通用组织层，不改变领域事实。

Atoll Agent 模型循环支持调用 Tool Actor 前，第一阶段允许 Web UI 或确定性 Agent 直接提交结构化领域命令；自然语言自动调用工具作为独立里程碑。

## 9. 领域模型

### 9.1 Company

控制状态：

```text
active | paused | needs_review | archived
```

派生业务阶段：

```text
new | discovering | ready | running | healthy | attention | blocked
```

控制状态决定是否允许调度；业务阶段由 URL 状态聚合得出，二者不得混为同一个字段。

### 9.2 Company URL

```text
new
→ discovering
→ recipe_needed
→ validating
→ ready
→ queued
→ running
→ succeeded / cooldown

异常分支：retrying / failed / needs_review
控制分支：paused / archived
```

Company URL 是调度目标，不是单次任务。

### 9.3 Recipe

```text
draft → validating → active → degraded → deprecated
```

每次 Task 固定引用 `recipe_id + recipe_version`，运行过程中不得自动漂移到新版本。

### 9.4 Task 与 Attempt

Task：

```text
queued → leased → running → succeeded
                         └→ failed → retrying → queued
queued / leased → canceled / expired
```

Attempt：

```text
created → running → succeeded / failed / expired / rejected
```

一个 Task 可以有多个 Attempt，但最多只有一个当前有效租约。

### 9.5 核心数据表

```text
companies
company_urls
schedule_targets

recipes
recipe_versions
recipe_validations

tasks
task_attempts
workers
worker_slots
origin_budgets

recipe_runs
failure_evidence
job_urls
job_details

domain_commands
domain_outbox
```

`domain_commands.command_id` 用于用户命令幂等，`domain_outbox.event_id` 用于向 Atoll 可靠补发。

## 10. 大规模调度设计

### 10.1 容量假设

首个容量模型使用：

```text
10,000 家公司
平均每家公司 2 个招聘入口
约 20,000 个 listing 调度目标/日
详情任务根据增量变化产生，不按全部历史职位全量展开
```

系统必须把 listing、detail、HTTP、browser、AI 和 human review 看成成本完全不同的工作类别。

### 10.2 Planner

Planner 由少量 Atoll durable timer 周期性唤醒，不为每家公司创建一个 Actor 或 timer。

```text
timer fire
  → 查询 next_run_at 已到期的目标
  → 每页读取固定数量
  → 根据幂等键创建近期任务
  → 更新下一个扫描游标
  → 在时间预算耗尽前继续或安排下一次唤醒
```

Planner 只规划近期窗口，不在每天零点创建全天全部任务。

任务幂等键建议为：

```text
task_type + target_id + schedule_window + recipe_version
```

### 10.3 队列与分片

Worker 通过执行 Channel 向 Dispatcher Actor 发送 `recruiting.task.claim`，不得直接查询任务表。Dispatcher 在处理该消息时，使用 Actor 控制的关系数据库 Resource 保存任务投影，并可在内部通过 `FOR UPDATE SKIP LOCKED` 完成并发选择。队列按以下维度逻辑隔离：

```text
http-production
browser-production
validation
retry
ai-repair
human-review
```

分片键优先使用规范化 `origin_host`，使站点级限流和熔断容易执行；热点站点允许在共享预算约束下拆成多个物理分片。

只有当 Resource adapter 的数据库领取成为经测量确认的瓶颈时，才在 Actor 背后引入 Redis Streams、NATS 或 Kafka 作为分发加速层。任何加速层不得成为新的用户入口、权限边界或业务权威。

### 10.4 优先级与公平性

有效优先级由以下因素组成：

```text
effective_priority =
    business_priority
  + deadline_overdue_score
  + queue_age_score
  + manual_urgency
  - retry_penalty
```

调度采用任务类型配额与公司轮转，避免单个大型公司占满全部 Worker。初始软配额建议：

| 工作类别 | 初始容量份额 |
|---|---:|
| 正常生产 | 50% |
| 到期复检 | 20% |
| 首次接入与验证 | 15% |
| 自动重试与修复验证 | 10% |
| 人工紧急任务 | 5% |

空闲份额可以被其他队列借用，队列年龄必须提供防饥饿加权。

### 10.5 租约与领取

一次领取必须在短事务中：

1. Atoll 先把 Worker 的 claim request 记入执行 Channel ledger；
2. Dispatcher 验证发送 Actor 的稳定身份、当前 incarnation 和能力；
3. 选择一个当前可运行且符合 Worker 能力的 Task；
4. 检查全局、站点、Profile 和任务类型预算；
5. 创建唯一 `attempt_id` 和不可猜测的 `lease_token`；
6. 写入 `lease_expires_at` 并将 Task 标记为 leased；
7. 提交 Resource 事务；
8. 通过关联原 request 的 result message 返回任务和 Resource ticket。

Worker heartbeat/result 都是发给 Dispatcher/Committer Actor 的消息，只能延长或完成自己当前有效的 Attempt。旧 incarnation 或旧 Attempt 的延迟消息不得改变新 Attempt。

### 10.6 多层预算

```text
全局 Worker Slot
  └─ task_type Slot
      └─ origin_host 并发与 QPS
          └─ login profile 并发
              └─ company 公平份额
```

`origin_budgets` 至少维护：

```text
max_concurrency
requests_per_second
burst
active_leases
cooldown_until
recent_error_rate
circuit_state
```

遇到 403、429、验证码或连续超时时，应降低站点预算或打开熔断器，而不是通过扩容产生重试风暴。

### 10.7 分级执行

为控制浏览器成本，执行顺序为：

1. 使用列表页已有的完整职位内容；
2. 使用可复现的公开 HTTP/API；
3. 捕获页面实际调用的后端 API；
4. 只对新增或摘要变化的职位打开详情；
5. 只有依赖 JavaScript、登录态或真实 DOM 时才使用 Browser Worker；
6. 只有未知、异常和抽样质检才进入 AI Worker。

### 10.8 Worker 扩缩容

Worker 注册以下能力：

```text
worker_id
worker_type
region
browser
extension_version
profile_ids
slot_count
supported_task_types
```

扩缩容信号按优先级为：

1. 最老可运行任务的等待时间；
2. deadline 违约率；
3. 可用任务对应的 Slot 利用率；
4. Worker 错误率和资源压力；
5. 队列长度。

仅有大量处于 cooldown 或受站点限流的任务时，不得盲目扩容。

容量估算公式：

```text
所需并发 Slot ≈
任务数 × 平均执行秒数 ÷ 时间窗口秒数 ÷ 目标利用率
```

### 10.9 重试和熔断

默认使用带随机抖动的指数退避：

```text
1 分钟 → 5 分钟 → 30 分钟 → 2 小时
```

| 失败类型 | 默认策略 |
|---|---|
| 网络超时、偶发 5xx | 自动退避重试 |
| 429 | 站点降速并延迟 |
| 403、验证码、登录失效 | Profile/站点熔断并请求处理 |
| Selector 或页面结构变化 | Recipe Repair |
| 空结果、字段异常 | Quality Review |
| 确定性数据错误 | 不盲目重试，进入修复 |
| 多轮不收敛 | 人工仲裁 |

熔断粒度优先为 `origin_host + recipe_id` 或 `profile_id`，不得因单个站点故障阻塞整个生产队列。

## 11. 一致性与故障恢复

### 11.1 结果提交事务

一次结果提交必须在同一个领域数据库事务内完成：

```text
验证 Task、Attempt、Worker、lease_token 和租约期限
  → 幂等创建 recipe_run
  → upsert job_urls / job_details 或 failure_evidence
  → 推进 URL 和 Company 派生状态
  → 创建后继 Task
  → 完成 Task 和 Attempt
  → 写 domain_outbox
  → COMMIT
```

禁止先标记 Task 成功，再在事务外写职位数据。

### 11.2 幂等边界

- 用户命令：`command_id` 唯一；
- Planner：任务幂等键唯一；
- Attempt：`attempt_id` 唯一；
- Result：`result_id` 唯一；
- 职位：稳定来源 ID 或规范化 URL 业务键唯一；
- Outbox：`event_id` 唯一；
- Atoll 消息：使用稳定 correlation/cause 关联命令与领域事件。

### 11.3 恢复任务

独立 Reconciler 周期性处理：

- 租约过期但 Task 未结束；
- Outbox 长时间未发布；
- Task 已成功但派生状态不一致；
- Worker 离线但仍持有租约；
- Recipe 已废弃但存在尚未领取的旧版本任务；
- 批次统计与任务事实不一致。

Reconciler 只能根据持久事实修复，不依赖进程内存。

## 12. Atoll 消息策略

### 12.1 写入 Channel 的事件

```text
recruiting.batch.started
recruiting.batch.completed
recruiting.company.blocked
recruiting.origin.circuit_opened
recruiting.recipe.review_requested
recruiting.recipe.published
recruiting.capacity.degraded
recruiting.system.recovered
```

### 12.2 执行消息与大数据分离

Task claim、租约续期和 result submission 具有控制与因果价值，使用紧凑消息进入分片后的 execution Channel。它们不进入面向人的 Ops Channel，也不能在消息 payload 中嵌入页面正文或大批职位。

以下内容只进入 Actor 控制的 Resource 或指标系统：

- 页面打开、DOM 变化和网络响应明细；
- 页面正文、截图和原始 API 样本；
- 单条职位的完整正文；
- Worker 进程内部轮询；
- 不影响 Task/Attempt 状态的细粒度进度。

用户查询明细时，由 Recruiting Tool 从领域数据库读取并返回摘要，而不是提前把全部明细复制进 Channel。

## 13. 安全与权限

### 13.1 权限原则

- 权限由 Atoll membership/capability 执行，不依赖 prompt；
- Agent 只能调用注册的领域命令；
- 普通运营人员不能直接发布 Recipe 或读取明文凭证；
- Worker 只能领取符合其能力和授权范围的任务；
- 结果提交凭证与具体 Attempt 绑定；
- 不允许共享全局 `demo-token` 作为正式认证方式。

### 13.2 凭证与敏感数据

- 网站登录凭证和 Cookie 不进入 Channel 消息；
- Profile 绑定指定 Worker 或安全域；
- Failure Evidence 在发送给 LLM 前执行字段过滤和体积限制；
- 截图、HTML、API 样本保存到受控对象存储；
- Atoll 消息仅保存必要摘要和受控资源引用。

## 14. 可观测性

### 14.1 核心指标

业务指标：

- 活跃公司、可运行 URL、健康/异常/阻塞公司数；
- 新增职位、更新职位、过期职位；
- Recipe 覆盖率、成功率和平均寿命；
- 自动修复成功率、人工介入率。

调度指标：

- 各队列深度与最老任务年龄；
- 计划延迟和 deadline 违约率；
- claim 吞吐、Attempt 过期率、重复结果率；
- 各站点并发、QPS、429/403 和熔断状态；
- Worker Slot 利用率、任务耗时和错误率。

可靠性指标：

- 未发布 Outbox 数和最老年龄；
- 状态对账差异；
- stale Attempt 被拒绝数量；
- 数据事务回滚和死锁重试；
- Atoll 通知投递延迟。

### 14.2 追踪标识

以下 ID 必须贯穿日志、指标和消息：

```text
command_id
correlation_id
batch_id
task_id
attempt_id
recipe_run_id
company_id
url_id
```

## 15. 产品界面

### 15.1 Ops Channel

提供：

- 自然语言命令；
- 今日运行摘要；
- 公司状态查询；
- 容量和异常摘要；
- 进入 Dashboard 或 Review 的链接。

### 15.2 Dashboard

第一屏回答：

```text
今天计划了多少？完成多少？
最老任务等了多久？
哪些站点正在熔断？
哪些公司需要关注？
Worker 容量是否足够？
今天新增和更新了多少职位？
```

### 15.3 Review Queue

每个审批项展示：

- 受影响公司和 URL；
- 当前 Recipe 与候选 Recipe 的差异摘要；
- 原失败步骤和结构化证据；
- 验证样本及质量结果；
- 风险范围和回滚版本；
- 批准、拒绝、要求重新修复操作。

## 16. 交付阶段

### M0：领域契约

- 冻结第一版实体、状态和命令词汇；
- 建立 ID、幂等、事务和 Outbox 约束；
- 建立 Actor、Message、Resource 与 Driver 的所有权边界。

### M1：端到端纵向切片

完成：

```text
Atoll 用户提交结构化添加公司命令
→ 创建公司和 URL
→ 创建 listing Task
→ 一个 Browser Worker 领取并执行
→ 事务写入职位
→ Atoll Channel 收到摘要
```

### M2：可靠调度

- Task/Attempt/Lease；
- heartbeat、过期和重领；
- 幂等结果提交；
- Outbox 和 Reconciler；
- 站点限流、重试和熔断。

### M3：Agent 与修复闭环

- Agent 模型循环调用 Tool Actor；
- URL Discovery；
- Recipe 生成、验证和发布；
- Failure Evidence；
- 自动修复和人工审批。

### M4：10K 容量验证

- 10,000 家公司调度模拟；
- HTTP/Browser/AI Worker 分池；
- Scheduler 多实例；
- 热点站点、公平性和重试风暴测试；
- Worker 自动扩缩容。

### M5：生产准备

- Atoll 与领域数据库备份恢复；
- 权限和凭证审计；
- 故障注入和恢复演练；
- 数据保留和删除策略；
- 正式 SLA 和容量基线。

## 17. 第一阶段验收标准

### 17.1 功能验收

- 用户可以从 Atoll 创建、暂停、恢复和查询公司；
- 验证过的 Recipe 可以被 Worker 执行并写入结构化职位；
- 失败生成结构化证据并进入正确处理路径；
- 需要人工操作时，指定成员在 Review Channel 收到请求；
- 用户可以从命令追踪到 Task、Attempt、Recipe Run 和数据结果。

### 17.2 可靠性验收

- 重复用户命令不创建重复公司或批次；
- Worker 在执行中退出后，租约过期并由另一 Worker 恢复；
- 旧 Attempt 的延迟 heartbeat/result 无法覆盖新 Attempt；
- 重复 result 不重复写入职位或创建后继任务；
- 事务提交后模拟进程退出，Outbox 能补发 Atoll 摘要；
- Atoll 暂时不可用时，采集生产链路继续运行，恢复后补发聚合事件；
- 领域服务重启不依赖内存恢复关键流程。

### 17.3 容量验收目标

以下是首轮工程目标，不是未经测试的外部 SLA：

- 保存并扫描 10,000 家公司、20,000 个日常 URL 目标；
- 支持至少 100,000 个待处理 Task 的积压；
- 持续完成至少 20 次 Task 状态提交/秒，峰值达到 50 次/秒；
- 支持至少 200 个并发执行 Slot 的领取和 heartbeat；
- 相同公司和站点受公平性、并发和 QPS 预算约束；
- 压测期间无任务事实丢失，无 stale Attempt 覆盖，无重复业务结果；
- Atoll ledger 流量按批次和异常聚合，不随单条职位数量线性增长。

具体浏览器吞吐不以固定任务数验收，而以真实 Recipe 的平均执行时长、站点预算和 deadline 达成率计算。

### 17.4 真实招聘网站验收

招聘发现、Recipe、HTTP/API 抽取和 Browser Worker 的产品验收以真实公开招聘网站为准，不以本地模拟页面代替真实业务效果。并发冲突、租约过期、数据库回滚、进程崩溃等不可安全施加给第三方网站的故障，仍在本地数据面注入。

初始真实站点池从 Snowland 和 Staircase 已经试验过的站型中选择，并在每次测试前重新确认当前官方入口：

| 站型 | 候选样本 | 主要验证点 |
|---|---|---|
| 国内大型 SPA 招聘站 | 腾讯招聘、百度招聘 | 动态列表、筛选参数、分页、详情/API 映射 |
| 独立公司招聘站 | 爱奇艺招聘、网易相关招聘站 | 多招聘类型、空职位、历史入口失效 |
| 通用 ATS | Moka、飞书招聘、北森等真实公司入口 | 同平台 Recipe 复用、租户差异、分页 |
| 国际招聘站/ATS | ASML、Greenhouse 上的公开职位页 | 多语言、地区、时区和字段归一化 |
| Landing/无职位页面 | 当前公开但没有职位的官方入口 | 正确识别无职位，不误判为 Recipe 失败 |

真实站点池是版本化测试数据，不是永久 URL 清单。每条样本至少记录：

```text
sample_id
company_name
official_entry_url
origin_host
site_type
expected_recruitment_types
expected_result_class
allowed_access_method
last_human_verified_at
enabled
```

由于职位数量和页面内容每天变化，自动验收不依赖固定职位总数，而验证稳定不变量：

- URL 属于经过人工确认的官方招聘入口或官方使用的 ATS；
- 预期有职位的样本能够产生至少一个合法职位，预期空站点被识别为 `no_open_jobs`；
- 每条职位具有稳定来源 ID 或可规范化的唯一详情 URL；
- 标题、地点、招聘类型、职责和要求达到对应站型的字段完整度门槛；
- 翻页后职位集合单调扩展且没有异常重复；
- 列表项能够映射到正确详情，不能串公司或串岗位；
- 相同 Recipe 重跑不会制造重复业务结果；
- 页面变化时产生 Failure Evidence，而不是静默写入低质量数据。

真实站点测试分三级运行：

| 级别 | 范围 | 用途 |
|---|---|---|
| Live Smoke | 1 至 3 个低成本站点、每站少量页面 | 开发者手动运行和合并前冒烟 |
| Nightly Canary | 覆盖主要站型的 5 至 10 个样本 | 发现网站变化和 Recipe 回归 |
| Weekly Coverage | 20 至 50 个分层样本 | 评估跨平台覆盖率和数据质量 |

每次真实站点运行保存带时间戳的：

- 最终 URL、HTTP 状态和页面标题；
- Recipe ID、版本和运行 trace；
- 列表/详情字段统计和去重统计；
- 必要的页面摘要、API 样本和截图；
- 失败分类、站点预算和重试决定；
- 人工抽样结论。

CI 默认测试不应因第三方网站短时不可用而阻断所有代码提交。Live Smoke 作为发布或合并前的独立网络验收门，Nightly/Weekly 失败先进入分类：确认是代码回归后阻断发布，确认是网站变化后创建 Recipe 修复任务。

真实站点测试必须遵守以下边界：

- 只访问公开招聘信息或使用明确授权的测试账号；
- 不绕过验证码、登录控制、付费墙或访问控制；
- 默认每个 origin 单并发并设置保守请求间隔；
- 尊重站点公开规则、服务条款和明确的限流响应；
- 禁止为了测试熔断、重试或并发而向第三方网站制造异常流量；
- 发现 403、429 或验证码后立即降速或停止该站点测试；
- 不执行职位投递、简历上传或其他会改变第三方业务状态的动作。

计划提供独立命令，避免与离线测试混淆：

```text
make recruiting-live-smoke
make recruiting-live-nightly
make recruiting-live-weekly
```

上述命令在对应实现交付时加入 Makefile；运行结果必须明确标注测试时间、网络环境、样本版本和未执行原因。

## 18. 关键风险与对策

| 风险 | 对策 |
|---|---|
| Atoll 尚未 data-safe | 测试先行；生产前完成备份、迁移、恢复和故障注入验收 |
| Agent 尚不能完整调用 Tool Actor | M1 使用结构化命令；M3 前补齐模型工具端口 |
| 浏览器任务成本失控 | HTTP/API 优先、增量详情、分级 Worker 和预算控制 |
| 目标站点反自动化 | 站点限流、Profile 隔离、熔断和人工仲裁 |
| 重试风暴 | 分类重试、指数退避、随机抖动和站点级熔断 |
| 双重状态源 | Actor 是行为权威；ledger 保存因果输入输出，Actor Resource 保存可恢复领域投影，并通过幂等键对账 |
| 高频事件压垮 Atoll ledger | 执行 Channel 分片；消息只含控制 envelope、摘要和 Resource 引用，大对象不进 ledger |
| AI 发布错误 Recipe | 版本冻结、小样本验证、风险分级审批和快速回滚 |

## 19. 已确定的架构决策

1. Atoll 是唯一架构底座和控制平面，所有控制动作通过 Actor/Channel/Message 发生。
2. Recruiting Service Actor 是领域行为权威，大规模状态存于其控制的 Resource。
3. Task claim、lease、result 是执行 Channel 中的领域消息；大对象通过 Resource data plane 传递。
4. 不建立独立于 Atoll 的 Recruiting REST 控制面、身份系统或权限系统。
5. 正常批量生产不调用 LLM。
6. Worker 使用消息驱动的 Pull + Lease，不依赖定向推送。
7. 分布式语义采用至少一次执行和幂等结果。
8. 公司、URL 和任务不映射为独立 Channel；Channel 只表达协作、权限和执行分片边界。
9. Atoll timer 唤醒 Planner Actor，不为每个目标创建独立调度 Actor。
10. 站点预算和熔断优先于 Worker 扩容。
11. Snowland 和 Staircase 只作为业务证据、真实样本和叶子实现来源，不拥有目标架构决策权。

## 20. 后续待决策项

- 第一版 Actor Resource adapter 选择 SQLite、MySQL 还是 PostgreSQL；
- Browser/Extension Driver 采用 Atoll 原生 Driver 协议还是 MCP 适配；
- Browser Worker 的 Profile 加密、分配和迁移策略；
- Recipe 自动发布的风险等级和审批阈值；
- 招聘数据的保留、删除和合规规则；
- 生产部署采用单地域还是多地域 Worker；
- 对外查询是直接领域 API，还是增加独立搜索与分析存储。

这些待决策项不影响 M1 纵向切片，可以在真实运行数据出现后逐项冻结。
