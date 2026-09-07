# Atoll Recruiting：招聘数据持续采集产品设计

状态：产品与架构设计草案

版本：v0.4

日期：2026-09-07
目标规模：维护约 10,000 家公司的招聘数据；吞吐目标由真实网站基准测试确定

## 1. 文档目的

本文定义一个遵循 Atoll 设计思想的招聘数据持续采集产品。Atoll 是身份、协作、运行语义和安全边界的唯一权威；Snowland 与 Staircase 只提供业务场景、样本、站点经验和失败案例，不构成目标架构依赖。

本文明确区分三类结论：

- **已确定原则**：由 Atoll 原理或业务要求直接推出；
- **第一版选择**：为了形成最小闭环而作出的可逆选择；
- **待验证假设**：必须通过真实网站、故障注入或容量测试决定。

## 2. 产品定位

Atoll Recruiting 是一个由 Atoll 组织人和自动化协作、以可重复执行的采集计划为正常生产路径、持续发现并修复招聘数据异常的平台。

```text
用户 / Timer / 领域事件
          ↓
Atoll Channel + Message
          ↓
Recruiting Actor（领域行为权威）
          ↓
Recruiting Executor Actor(s)
          ↓
HTTP / Browser / Extension Driver
          ↓
Artifact 与结构化招聘数据
```

## 3. 业务依据与设计权威

### 3.1 从测试项目获得的业务知识

Snowland 和 Staircase 证明或提示：

- 对象包括公司、招聘入口、职位列表、职位详情和招聘类型；
- 网站存在 SPA、API、分页、登录态、ATS、多语言和页面变化；
- Recipe、浏览器插件和持久 Profile 值得继续验证；
- 正常采集、异常修复和人工处理具有不同成本；
- Web IM 和飞书可以作为指令、通知与仲裁入口；
- 新公司通常只需一至两次全量，稳定后每天检查全部入口并增量更新。

这些是需求和测试输入，不自动成为目标领域模型。历史项目中的 Worker 分类、任务表、状态枚举、队列、租约和服务拆分均不直接继承。

### 3.2 裁决顺序

1. Atoll 的身份、消息、生命周期、权限、恢复和分层不变量；
2. 已验证的招聘业务不变量；
3. 真实招聘网站产生的可复现实证；
4. Snowland/Staircase 的历史代码；
5. 通用分布式系统惯用实现。

历史代码只有成为受 Atoll Actor 约束的领域模型、Resource adapter 或 Driver 后才可复用。

## 4. 产品目标与非目标

### 4.1 核心目标

1. 长期维护至少 10,000 家目标公司及招聘入口。
2. 新公司完成一至两次基线全量后，每天扫描全部有效入口，只展开新增或变化的职位详情。
3. 正常生产不依赖 LLM 逐页判断。
4. 页面变化或质量异常时保存证据，先确定性诊断，再由 Agent 或人修复。
5. 用户可以创建、查看、暂停、恢复、修正、重试、取消和完结有权限的工作。
6. 重要操作能够回答：谁发起、为何发生、谁执行、采用什么计划、产生什么结果。
7. 执行能力可水平扩展，单个执行进程或服务重启不破坏业务事实。
8. 对目标网站实施保守限流、熔断和访问边界。

### 4.2 非目标

- 绕开 Atoll 另建身份、权限、控制入口或编排权威；
- 为每家公司、URL 或工作创建 Channel 或 Actor；
- 把页面正文、截图、网络包和内部轮询写入 ledger；
- 让 LLM 直接修改领域数据库；
- 绕过登录、验证码、付费墙或访问控制；
- 在无实测依据时冻结 Worker 类型、队列技术、Channel 分片或服务数量；
- 在容量测试前承诺具体吞吐 SLA 或跨地域强一致调度。

## 5. 参与者与业务流程

### 5.1 角色是权限集合

| 参与者 | 第一版行为 |
|---|---|
| Human | 发起、观察、修正、审批和仲裁 |
| Recruiting Agent | 理解意图、调用公开能力、组织回复 |
| Recruiting Actor | 验证命令并推进招聘领域事实 |
| Recruiting Executor | 根据 capability 执行计划并提交证据与结果 |

“运营人员”“Recipe 维护者”“管理员”“数据使用者”是 capability 组合，不冻结为不同 Actor class。“Planner”“Dispatcher”“Committer”“Worker Fleet”“通知适配器”可以是 handler、投影或 Driver，不因名称不同就成为 Actor。

只有确实需要独立稳定身份、权限边界、部署位置、故障隔离或生命周期时才拆 Actor。

### 5.2 新增公司

```text
用户要求持续跟踪公司
  → Agent 或 UI 提交公开领域命令
  → Recruiting Actor 创建 Target 和 bootstrap Work
  → 自动发现或人工确认入口
  → 生成候选 Plan 并用真实页面验证
  → 必要时请求人批准高风险 Plan
  → 执行一至两次基线全量
  → Target 进入每日增量运行
```

### 5.3 每日增量

```text
Atoll durable timer 产生到期信号
  → Recruiting Actor 找到到期 Target
  → 幂等创建 incremental Work
  → Executor 扫描招聘入口
  → 对比上次水位、内容哈希或来源游标
  → 只处理新增或变化项的必要详情
  → 原子推进结果和下次到期事实
  → Channel 收到聚合摘要
```

Timer 粒度不冻结：可以每个 Target 一个 durable timer，也可以少量 timer 唤醒后扫描索引，以 Atoll 容量、恢复成本和延迟实测决定。

### 5.4 修复和人工接管

```text
执行或质量检查失败
  → 保存 Failure Artifact
  → 确定性规则分类
  ├─ 可重试：有界退避
  ├─ 已知修复：验证受控 Plan 版本
  ├─ 未知异常：Repair Agent 分析并验证候选方案
  └─ 无法收敛：Work 进入 waiting_human
                         ↓
             人领取、修正、批准、跳过或终止
                         ↓
                  恢复原 Work 或完结
```

人工处理是 Work 的状态和协作过程。第一版不要求独立 Human Review 实体或表；Review 只是 `waiting_human` Work 的视图。

### 5.5 正交表达工作来源

```text
trigger:    manual | timer | event
purpose:    bootstrap | incremental | reconcile | repair
initiator:  initiator_actor_id
cause:      cause_message_id / cause_work_id
```

不再用一个 `source` 同时表达触发方式、业务目的和发起者。人工修复可以是 `trigger=manual`、`purpose=repair`。所有工作遵守同一状态、限流、质量和审计规则。

## 6. 核心设计原则

### 6.1 Atoll 是控制权威

业务命令、Actor 间控制交互和有协作价值的领域事件通过 Atoll Message 发生，受 Channel membership 和 capability 约束。Recruiting Actor 是领域行为权威；大规模事实可委托给其控制的 Resource，但 Resource 不是第二控制面。

HTTP、Web、飞书或 CLI 可以作为 Gateway/Driver。禁止的是绕过 Atoll 身份、权限、因果和领域校验，不是禁止 HTTP。

### 6.2 Actor 是身份与生命周期边界

Actor 不是模块、进程池或微服务的同义词。第一版将规划、调度判断、结果提交和恢复作为 Recruiting Actor 内部 handler。只有出现明确身份、授权、位置、故障或生命周期边界时才拆分。

### 6.3 Channel 是协作与权限边界

Channel 不作为数据库分片或队列分区。第一版默认一个招聘业务 Channel。只有参与者、可见性、权限或上下文确实不同才增加 Channel；执行吞吐先由 Resource data plane 和执行协议处理。

### 6.4 确定性优先

- 已验证 Plan 的正常执行不调用 LLM；
- 能用 HTTP/API 时不启动浏览器；
- 能用规则判断时不调用 AI；
- AI 生成的 Plan 必须真实执行和质量验证；
- AI 只能调用公开能力，不能直接写数据。

### 6.5 Work 与 Attempt 分离

Work 是用户能理解和运维的业务工作；Attempt 是对 Work 的一次机器执行。执行可能重复，业务结果幂等生效。旧 Attempt 的延迟结果不能覆盖后来已经接受的结果。

### 6.6 先定义不变量，再选分发协议

第一版不冻结 Pull、Push 或 Lease。任何方案必须满足：

1. 只有授权且能力匹配的 Executor 可以执行；
2. 一个 Work 可有多个 Attempt，但结果按当前接受条件生效；
3. 执行进程消失后 Work 能恢复；
4. 重复命令、交付和结果不重复产生业务数据；
5. 取消、暂停和人工修正不被旧结果覆盖；
6. 能实施站点、凭证和全局资源预算。

是否需要 lease、heartbeat 和 token，根据任务时长、Atoll incarnation、跨进程恢复和故障测试决定。即使不用 lease，也必须使用 `attempt_id` 和版本条件隔离陈旧结果。

### 6.7 外部安全预算优先

增加 Executor 不能突破目标站点、登录 Profile、领域存储和第三方服务预算。遇到 403、429、验证码或连续超时时，优先降速或熔断。

### 6.8 控制消息与大对象分离

命令、接受/拒绝、状态转换、人工决定和聚合摘要进入 ledger。HTML、截图、网络响应、职位正文和批量结果作为 Artifact/Resource 保存，Message 只携带引用、摘要和哈希。

### 6.9 人和自动化遵守同一种规则

Human、Agent 和 Tool 修改领域事实时都使用公开领域词，携带稳定身份、因果、命令 ID 和预期版本，接受相同状态校验，并在 ledger 留下记录。

## 7. 最小总体架构

```text
┌──────────────────────────────────────────────────────────┐
│ Atoll Node                                               │
│ recruiting Channel                                      │
│   Human ⇄ Recruiting Agent ⇄ Recruiting Actor            │
│                                  ⇅                       │
│                         Recruiting Executor Actor(s)      │
│ Ledger / Membership / Access / Timer / Resource / Device  │
└────────────────────────────┬─────────────────────────────┘
                             │ Resource data plane / Drivers
                             ▼
┌──────────────────────────────────────────────────────────┐
│ Domain facts / Plan / Artifact / HTTP / Browser / AI      │
└──────────────────────────────────────────────────────────┘
```

第一版不预设独立 Planner、Dispatcher、Committer、Fleet、Notification 或 Reconciler Actor。

### 7.1 所有权

| 能力或事实 | 权威所有者 |
|---|---|
| Human、Agent、Executor 身份和 incarnation | Atoll |
| Channel、成员和访问权限 | Atoll |
| 控制消息、协作过程和摘要 | Atoll ledger |
| 可靠唤醒 | Atoll timer |
| Target、Plan、Work、Attempt 接受规则和招聘数据 | Recruiting Actor |
| 大规模事实和 Artifact 内容 | Recruiting Actor 控制的 Resource |
| HTTP、Browser、Extension、AI 外部效果 | 授权 Driver；调用 Actor 承担业务责任 |

### 7.2 依赖方向

```text
纯 Recruiting domain model
        ↑
Recruiting Actor adapter
        ↑
Atoll registry / channel composition

HTTP / Browser / AI Driver → 只实现外部效果
Web / 飞书 Gateway         → 只提交公开消息
```

领域模型不依赖 runtime、网络、墙钟或具体数据库；Driver 不拥有业务决策；Atoll core 不导入 Recruiting；UI 不直接修改数据库。

## 8. Atoll 产品映射

### 8.1 Channel

默认只创建 `recruiting` Channel。Dashboard 的 Ops、Review、Capacity 是同一事实集合上的不同视图。只有下列证据出现后才拆 Channel：

- 审核证据需要更小的可见成员集合；
- 客户或团队必须强隔离；
- 长期上下文互相污染；
- ledger 实测成为瓶颈，且拆分不破坏因果追踪。

### 8.2 Actor 与能力

| Actor | 类型 | 职责 |
|---|---|---|
| Human | human | 发起、观察、修正、审批和仲裁 |
| Recruiting Agent | agent | 理解意图、调用公开能力、组织回复 |
| Recruiting Actor | service/tool | 领域行为、调度判断、结果接受和恢复 |
| Recruiting Executor | tool | 按 capability 调用 Driver 并提交结果 |

第一版只冻结一个 `recruiting-executor` Actor class，可声明 `http.fetch`、`browser.recipe`、`browser.profile`、`extension.required`、`plan.validate` 等 capability。同一执行者可具备多项能力；步骤种类不等于 Executor 类型。

### 8.3 首批公开领域词

```text
recruiting.target.add / pause / resume / get / list
recruiting.work.create / get / list / pause / resume
recruiting.work.correct / retry / cancel / resolve
recruiting.plan.inspect / validate / approve / reject
recruiting.execution.offer / accept / started / result / failed
recruiting.jobs.search
recruiting.system.status
recruiting.capacity.status
```

`offer/accept` 可映射为推送、拉取或其他机制；公开词不暴露数据库领取语义。

## 9. 最小领域模型

第一版只冻结五个核心概念。

### 9.1 Target

持续维护的业务目标，可包含公司和一个或多个招聘入口。

```text
active | paused | archived
```

健康、发现中、等待修复和无职位等优先由最近 Work、Artifact 和质量事实形成投影，不重复存储成互相漂移的控制状态。

### 9.2 Plan

描述如何执行发现、列表、详情或验证。Recipe 是 Plan 的一种候选表示，不假设它覆盖所有 HTTP、Browser、登录态和 Extension 场景。

```text
draft → validating → active → deprecated
```

Attempt 固定引用 `plan_id + version`，不能静默漂移版本。

### 9.3 Work

用户可理解、查询和运维的一项业务工作。可以存在父子 Work，但第一版不冻结 Work Order、Bundle、Task 三套实体。

```text
open → running → completed
          ├─ waiting_retry → running
          ├─ waiting_human → running / completed
          └─ failed
任意非终态 → paused / canceled
paused → open
```

最小字段：

```text
work_id, parent_work_id?, target_id
purpose, trigger, initiator_actor_id
cause_message_id?, cause_work_id?
status, waiting_reason?, priority, expected_version
plan_id?, plan_version?, not_before?, deadline_at?
summary, created_at, updated_at
```

多步骤可以使用子 Work 或步骤进度；是否需要独立 Task 由纵向切片决定。

### 9.4 Attempt

Executor 对 Work 的一次具体执行。

```text
offered → accepted → running → succeeded / failed / expired / rejected
```

保存 `attempt_id`、`work_id`、Executor identity/incarnation、capability 快照、Plan 版本、状态、接受版本、时间和结果/失败 Artifact 引用。`acceptance_version` 是逻辑 fencing 条件，但不要求一定使用 lease token。

### 9.5 Artifact

Resource 中大对象或批量结果的稳定引用，包括页面、截图、响应、原始职位、失败证据、候选 Plan、验证报告和 trace。至少保存 ID、内容哈希、类型、创建者、关联 Work/Attempt、访问和保留策略。

### 9.6 不冻结物理表

系统必须可恢复地保存五类核心事实、职位唯一键和内容版本、命令幂等记录以及站点预算状态。这不等于提前确定 `human_reviews`、`work_bundles`、`worker_slots`、`origin_budgets`、`domain_outbox` 等表。表、索引、事务和存储产品根据 Resource API 与访问实测决定。

## 10. 大规模调度

### 10.1 负载假设

```text
约 10,000 家公司
平均约 2 个招聘入口
约 20,000 个入口每日检查
接入阶段一至两次全量
日常详情量取决于当天增量，不等于历史职位总量
```

这些数字用于容量实验，不是已知吞吐事实。

### 10.2 到期工作

保存 Target 的刷新策略、下次到期时间、最近成功水位和幂等周期键。每日运行不依赖进程内 cron 和记忆。

| 候选方案 | 优点 | 风险 |
|---|---|---|
| 每 Target durable timer | 语义直接，无集中扫描 | 要验证大量 timer 的存储、恢复和唤醒成本 |
| 少量 timer + 到期索引 | timer 少，易做窗口规划 | 扫描器可能成为热点 |

M1 选择最简单可工作的方案，M4 用 20,000 个 Target 的测试决定生产方案。

### 10.3 执行分发

```text
到期 Work
  → Recruiting Actor 校验状态、预算和 capability
  → 创建 Attempt 并关联 Executor
  → Executor 获取 Plan 与输入 Resource
  → 执行并上传 Artifact
  → Recruiting Actor 按 attempt_id + acceptance_version 接受或拒绝
```

Push、Pull、带过期执行权、无 heartbeat 的短任务均可实验。依据是恢复正确性、延迟、ledger 流量、复杂度和吞吐，不是历史项目实现。

### 10.4 Batch 只是优化

多个 Work 可形成 Execution Batch 以减少往返，但 Batch 不改变单个 Work 的状态、取消、优先级和幂等边界；不要求用户可见、永久表、固定大小或独立 Channel；只有基准测试证明收益后才实现。

### 10.5 公平与预算

调度考虑优先级、deadline、等待时间、站点预算和重试退避，防止单一公司或站点占满能力。不冻结 50/20/15/10/5 等容量比例；默认值必须可配置并由实测校准。

```text
全局执行资源
  └─ capability 稀缺资源
      └─ origin_host 并发与速率
          └─ login profile 并发
              └─ company 公平份额
```

站点至少表达并发上限、速率、冷却期、近期错误率和熔断状态；令牌算法、表和缓存产品属于实现选择。

### 10.6 分级执行与扩容

默认按成本从低到高：列表已有内容、公开 HTTP/API、页面实际 API、变化职位详情、Browser、AI。它们是 capability 和决策顺序，不是不同 Worker 类型。

扩容观察最老可运行 Work、deadline、能力利用率、执行耗时/错误和可运行积压。处于 cooldown、等待人工或权限不匹配的 Work 不能靠扩容解决。

```text
所需并发 ≈ 可运行工作量 × 平均执行秒数 ÷ 时间窗口秒数 ÷ 目标利用率
```

是否拆 HTTP/Browser template、placement、进程池或 Actor class，只能由安全域、资源隔离和压测决定。

### 10.7 重试与熔断

退避参数按站点和错误配置，不冻结统一时间序列。

| 失败 | 默认决策 |
|---|---|
| 超时、偶发 5xx | 有界退避 |
| 429 | 降速并延迟 |
| 403、验证码、登录失效 | 停止对应站点/Profile 并请求处理 |
| 页面结构变化 | Plan 修复和真实样本验证 |
| 空结果、字段异常 | 质量检查，不能直接当成功 |
| 确定性数据错误 | 进入修复，不盲目重试 |
| 多轮不收敛 | `waiting_human` |

## 11. 一致性与恢复

### 11.1 必须保持

- 命令 ID 在作用域内唯一；
- 同一周期对同一 Target 创建 Work 幂等；
- Attempt 只在接受条件仍成立时推进 Work；
- 职位使用来源 ID 或规范化 URL 作为业务键；
- 保存结果与推进 Work 不留下不可恢复的半完成状态；
- ledger 与 Resource 的非原子间隙有明确恢复协议；
- 恢复只依赖持久事实。

### 11.2 结果接受

```text
验证 identity / incarnation / attempt_id / acceptance_version
  → 验证 Artifact 哈希与权限
  → 幂等写入职位和质量事实
  → 生成必要后继 Work
  → 推进 Attempt 与 Work
  → 产生可恢复的领域事件意图
```

若 ledger append 与领域事务不能原子提交，必须实现可重放交付或等效机制；Transactional Outbox 是候选，不是唯一答案。

### 11.3 对账

Recruiting Actor 的恢复 handler 检查长期无进展 Work/Attempt、已上传但未接受的 Artifact、状态投影差异、失效 incarnation、废弃 Plan 引用和长期未进入 ledger 的事件意图。只有出现独立权限、生命周期或故障边界后才拆 Reconciler Actor。

### 11.4 Atoll 暂时不可用

- 不创建新 Work，不作新分发或人工控制决定；
- 已开始的外部请求可以安全结束；
- Executor 可将结果和 Artifact 暂存在受控持久位置；
- 恢复后按 Attempt、版本和幂等键提交、接受或拒绝；
- 不允许旁路控制面继续推进领域状态。

## 12. 消息、权限和界面

### 12.1 消息与数据

`work.created/waiting_human/resumed/completed`、`target.blocked`、`origin.circuit_opened`、`plan.review_requested/published`、`capacity.degraded` 等协作事件进入 Channel。

执行消息是否逐条进入 Channel，要根据控制价值和 ledger 容量实测决定，但不能绕过 Atoll 身份、授权和可恢复因果。允许 Message 引用 Resource 中的紧凑批量 envelope。

页面正文、截图、响应、完整职位、细粒度进度和高频指标只进入 Resource 或指标系统。

### 12.2 权限

- 权限由 Atoll membership/capability 执行，不依赖 prompt；
- Agent 只能调用注册能力；
- Executor 只能执行符合其 capability、设备和安全域约束的 Work；
- 凭证和 Cookie 不进入 Channel；
- Profile 绑定授权设备或安全域；
- 发送给 AI 的 Artifact 必须过滤、脱敏和限量；
- 人工命令使用 `expected_version`，旧操作不能覆盖新状态；
- 批量影响 Target 或共享 Plan 的动作可配置二次批准。

### 12.3 Work Center

用户可按 trigger、purpose、状态、等待原因、Target、发起者和时间筛选 Work，并查看输入、Plan、Attempt、Artifact 和因果链；执行创建、领取人工项、暂停、恢复、取消、修正、重试、跳过、批准、拒绝或终止。

Review Queue 是 `waiting_human` Work 的视图；Capacity 是 Executor 和预算的投影，不要求独立 Actor。

## 13. 可观测性

业务指标包括 Target 健康、职位变化、Plan 覆盖与修复率；调度指标包括可运行量、等待原因、最老年龄、deadline、Attempt 结果、站点预算和能力利用率；可靠性指标包括跨 ledger/Resource 未完成意图、状态差异、陈旧结果拒绝和恢复时间。

必须贯穿 `command_id`、`correlation_id`、`target_id`、`work_id`、`attempt_id`、Plan 版本和 `artifact_id`。只有真正引入 Batch 等实体后才增加对应 ID。

## 14. 实施阶段

### M0：最小契约

- 冻结五个核心概念和公开命令；
- 冻结幂等、版本校验和陈旧结果拒绝不变量；
- 建立 Actor、Message、Resource、Driver 边界；
- 不冻结 Batch、Lease、Outbox、物理表或 Channel 分片。

### M1：纵向切片

```text
用户添加公司
→ 创建 Target 和 bootstrap Work
→ 一个 Executor 获取并执行 Plan
→ 上传真实 Artifact 和结构化职位
→ Recruiting Actor 幂等接受
→ recruiting Channel 收到摘要
```

用户可查看、暂停、修正、重试，并触发第二次校准全量；随后进入每日入口扫描和增量详情。

### M2：可靠执行实验

- 比较 push、pull 以及有/无 lease；
- 注入退出、重复、延迟和 incarnation 更换；
- 验证 Attempt fencing、幂等和对账；
- 验证限流、退避和熔断；
- 根据原子间隙决定是否使用 Outbox。

### M3：修复闭环

完成 URL Discovery、Plan 生成/验证/发布/回滚、Failure Artifact、自动修复，以及人从 `waiting_human` 恢复或完结 Work。

### M4：10K 容量决策

- 对比 20,000 个 Target 的 timer 方案；
- 用真实增量比例建模；
- 压测单 Channel 和 Resource data plane；
- 测试 capability 匹配、公平性、热点和重试风暴；
- 只有证据表明需要时引入 Batch、存储分片、Channel 拆分、独立调度 Actor 或不同 Executor placement/template；
- 形成可复现容量基线和 SLA 建议。

### M5：生产准备

完成备份恢复、权限凭证审计、故障演练、数据保留删除与合规策略，并根据容量基线确定部署规模。

## 15. 验收

### 15.1 功能与可靠性

- 用户可管理 Target 和 Work；
- manual、timer、event 使用同一状态和审计路径；
- 一至两次全量后每日扫描全部 active 入口且只展开增量详情；
- Plan 能执行并产生结构化职位；
- 失败生成 Artifact，人工能在原上下文处理；
- 可从命令追踪 Work、Attempt、Plan、Artifact 和数据；
- 旧 `expected_version` 明确拒绝；
- 重复命令、交付和结果不产生重复业务结果；
- Executor 退出后 Work 可恢复；
- 旧 Attempt 不能覆盖新事实；
- 在 ledger/Resource 每个故障点注入退出后可对账恢复；
- Atoll 不可用时不产生新调度，已开始效果安全结束并暂存，恢复后幂等处理；
- Actor 重启不依赖私有内存。

### 15.2 容量基准场景

以下不是预先承诺的 SLA：

- 保存约 10,000 家公司和 20,000 个日常入口；
- 构造增量、全量、修复、人工等待和 cooldown 混合负载；
- 测量 timer 创建、唤醒和恢复；
- 测量单 Channel、Resource data plane 和 ledger 增长；
- 逐步增加 Work 和并发，记录瓶颈，不预设每秒提交数或 Slot 数；
- 验证公平、站点预算、无事实丢失、无陈旧覆盖和无重复结果；
- 根据真实 Plan 耗时分布计算每日窗口所需容量。

报告必须记录硬件、数据库、Atoll 版本、数据分布、Plan 类型、预算、并发、P50/P95/P99、错误率和恢复时间，才能作为拆架构的依据。

### 15.3 真实网站验收

发现、Plan、HTTP/API 和 Browser 以真实公开招聘网站验收。本地只注入不能安全施加给第三方的并发、重复、崩溃和数据库故障。

候选站型：

| 站型 | 样本 | 验证点 |
|---|---|---|
| 国内大型 SPA | 腾讯招聘、百度招聘 | 动态列表、筛选、分页、API 映射 |
| 独立招聘站 | 爱奇艺、网易相关招聘站 | 招聘类型、空职位、入口失效 |
| 通用 ATS | Moka、飞书招聘、北森公开入口 | Plan 复用、租户差异、分页 |
| 国际站/ATS | ASML、Greenhouse 公开职位页 | 多语言、地区、时区、归一化 |
| 无职位页 | 当前公开的官方入口 | 正确识别 `no_open_jobs` |

样本需版本化保存官方 URL、origin、站型、预期结果、允许访问方式、人工确认时间。验收职位唯一性、字段质量、分页、列表详情映射、幂等和页面变化证据，不依赖固定职位总数。

测试分为 Live Smoke（1–3 站）、Nightly Canary（5–10 站）和 Weekly Coverage（20–50 站）。每次保存 URL、HTTP 状态、Plan 版本、trace、字段/去重统计、必要截图、失败分类、预算决定和抽样结论。

边界：

- 只访问公开信息或明确授权账号；
- 不绕过验证码、登录、付费墙或访问控制；
- 默认 origin 单并发并保守间隔；
- 遵守规则、条款和限流响应；
- 不用第三方网站测试异常流量；
- 遇到 403、429 或验证码立即降速或停止；
- 不执行投递简历等外部写操作。

计划提供 `make recruiting-live-smoke`、`make recruiting-live-nightly`、`make recruiting-live-weekly`。第三方短时不可用需先区分代码回归与网站变化。

## 16. 已确定的架构决策

1. Atoll 是唯一身份、权限、消息协作和业务控制底座。
2. Recruiting Actor 是领域行为权威，事实保存在其控制的 Resource。
3. 第一版默认一个招聘 Channel，不把 Channel 当队列分片。
4. 第一版不拆 Planner、Dispatcher、Committer、Fleet 或 Reconciler Actor。
5. 第一版只冻结一个 `recruiting-executor` Actor class，capability 与步骤类型分离。
6. 只冻结 Target、Plan、Work、Attempt、Artifact 五个核心概念。
7. trigger、purpose、initiator、cause 正交表达工作，不使用混合 source。
8. 人工审核是 `waiting_human` Work 的状态和视图。
9. 一至两次全量后，每日扫描全部 active 入口并只处理增量详情。
10. 正常生产确定性优先，不逐页调用 LLM。
11. 执行可重复，结果按幂等键和 Attempt 接受条件生效。
12. 站点预算和熔断优先于扩容。
13. 大对象使用 Resource，Message 保存控制、因果和稳定引用。
14. Gateway/Driver 不能绕过 Atoll 权限、消息和领域校验。
15. Snowland/Staircase 只提供业务证据、样本和可选叶子实现。

## 17. 待实验后决策

- 每 Target timer 还是少量 timer 扫描；
- push、pull 还是混合分发；
- 哪类 Work 需要 lease/heartbeat；
- 是否需要 Execution Batch；
- 单 Channel 容量和拆分条件；
- Resource adapter 与物理数据模型；
- Outbox 或其他跨存储恢复协议；
- Executor template、placement 或进程池；
- Browser/Extension Driver 协议；
- Profile 加密、授权、分配和迁移；
- Plan 自动发布风险阈值；
- 数据保留、删除和合规；
- 单地域或多地域；
- 查询使用领域能力还是搜索分析投影。

待决策项只有记录测试条件、观测结果、替代方案和取舍后，才能转为已确定决策。
