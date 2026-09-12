# Atoll Recruiting：招聘数据持续采集产品设计

状态：产品与架构设计草案

版本：v0.8

日期：2026-09-07

目标规模：维护约 10,000 家公司的招聘数据；吞吐目标由真实网站基准测试确定

## 1. 文档目的

本文定义一个遵循 Atoll 设计思想的招聘数据持续采集产品。Atoll 是身份、协作、运行语义和安全边界的唯一权威；Snowland 与 Staircase 只提供业务场景、样本、站点经验和失败案例，不构成目标架构依赖。

本文明确区分三类结论：

- **已确定原则**：由 Atoll 原理或业务要求直接推出；
- **第一版选择**：为了形成最小闭环而作出的可逆选择；
- **待验证假设**：必须通过真实网站、故障注入或容量测试决定。

配套工程执行、测试分层、25 场景验收和生产准入见 `docs/experiments/atoll-recruiting-development-validation-plan.zh-CN.md`。

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
- Recipe 已证明适合作为固化和复用网站流程的核心资产；浏览器插件和持久 Profile 是发现、修复及部分网站执行所需的可选能力，不是全部日常任务的固定依赖；
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
- 判断、同步或删除已经从招聘来源消失的岗位；
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
  → 生成候选 Recipe 并用真实页面验证
  → 必要时请求人批准高风险 Recipe
  → 执行一至两次基线全量
  → Company 及其 active Source 进入每日增量运行
```

### 5.3 每日增量

```text
Atoll durable timer 产生到期信号
  → Recruiting Actor 找到当日到期的 active Source
  → 幂等创建 incremental Work
  → 从 Resource 读取该 Source 上次成功的 Incremental Checkpoint
  → Executor 从列表第一页开始执行 Listing Recipe
  → 按最新活动时间倒序处理到旧边界并完成重叠窗口
  → 原子保存新增/更新观测、创建 Detail Work 并提交新 Checkpoint
  → Executor 对派生的 Detail Work 执行 Detail Recipe
  → Channel 收到聚合摘要
```

Timer 粒度不冻结：可以每个 Recruitment Source 一个 durable timer，也可以少量 timer 唤醒后扫描索引，以 Atoll 容量、恢复成本和延迟实测决定。

### 5.4 修复和人工接管

```text
执行或质量检查失败
  → 保存 Failure Artifact
  → 确定性规则分类
  ├─ 可重试：有界退避
  ├─ 已知修复：验证受控 Recipe 版本
  ├─ 未知异常：Repair Agent 分析并验证候选方案
  └─ 无法收敛：Work 进入 waiting_human
                         ↓
             人领取、修正、批准、跳过或终止
                         ↓
                  恢复原 Work 或完结
```

人工处理是 Work 的状态和协作过程。第一版不要求独立 Human Review 实体或表；Review 只是 `waiting_human` Work 的视图。

详情 Recipe 修复的安全切点不是“直接重跑旧 Work”，而是先把已验证的新版本以 Source Assignment 原子切换，再从已终结的失败 Work 创建因果 Retry Work。普通发布只接受结果契约、scope 和 Executor capability 均兼容的 Recipe；Source version、Assignment version、命令回执和审计事件在同一事务受围栏。改变契约、scope 或 capability 的版本必须进入独立迁移流程，不能借普通修复命令悄悄改变既有 Work 的执行含义。Listing Recipe 采用逐 Source 灰度原语：兼容切换在同一事务重绑定 Checkpoint Recipe 身份但不移动 frontier，随后 Source 强制进入 `repairing`，必须重新校准才能恢复每日调度；批量升级只能逐项调用该原语并依据每项验证结果继续或回滚。解析失败不能只保存二次生成的 failure 摘要：导致失败的原始 response/page 与主 failure Artifact 必须全部绑定同一 Work/Attempt，并随失败状态原子登记；否则人和 Agent 无法离线复盘修复依据。

新 Source 的首次 Detail Recipe 绑定不是 rollout：rollout 只替换已有 Assignment，不能用测试种子或数据库直写补齐首次绑定。公开 `recruiting.recipe.assign` 只允许在 Source 已完成 Listing 校验、当前 Listing Assignment 精确匹配且尚无 Detail Assignment 时，绑定一个同 scope、active、v1 的 Detail Recipe；Source version、首条 Assignment/历史、命令回执与 `source.detail_recipe_assigned` 事件在一个事务内提交。声明式 HTTP Recipe 下一步进入 baseline；Browser Recipe 若还没有 Profile，则明确先绑定 Profile。第二次“首次绑定”必须拒绝，后续变更统一走验证后的 rollout/rollback。

### 5.5 正交表达工作来源

```text
trigger:    manual | timer | event
purpose:    company_discovery | source_discovery | baseline
            listing_sync | detail_sync
            reconcile | repair | data_maintenance
initiator:  initiator_actor_id
cause:      cause_message_id / cause_work_id
```

不再用一个 `source` 同时表达触发方式、业务目的和发起者。人工修复可以是 `trigger=manual`、`purpose=repair`。所有工作遵守同一状态、限流、质量和审计规则。

### 5.6 确定的业务对象关系

以下关系来自业务本身，第一版必须明确表达：

```text
Company
  └─ Recruitment Source（一个可枚举职位的招聘列表来源）
       └─ Job Posting（一个来源岗位及其详情 URL）
```

- **Company**：需要持续维护的公司主体。
- **Recruitment Source**：公司的岗位列表入口。它可能是一个网页 URL，也可能是带固定参数的 API、ATS 租户入口或多个招聘类别的逻辑入口。
- **Job Posting**：来源中的岗位。保存来源岗位 ID、规范化详情 URL、当前结构化数据、内容版本和最近来源活动时间。
- 一个 Company 可以没有已确认 Source，也可以有多个 Source。
- 公司（以稳定 company_id 标识，company_name 可版本化修改）与岗位列表 URL 是一对多关系；列表 URL 必须归属于一个 Company。
- 同一个岗位可能在多个 Source 出现；第一版先保证来源内唯一，再通过可审计规则做跨来源合并。
- 列表 Recipe 和详情 Recipe 可以独立版本化、失效和修复，不能用一个笼统“网站 Recipe 状态”覆盖两者。

Target 是“可调度对象”的统一称呼，不替代这些业务实体。Company 和 Recruitment Source 都可以成为 Target；Job Posting 只有需要获取详情、核验或修复时才产生 Work。

### 5.7 完整业务场景目录

| 场景 | 触发 | 主要输入 | 产生的 Work | 完成结果 |
|---|---|---|---|---|
| 单个新增公司 | manual/event | 公司名称、官网等 | company/source discovery | 公司和候选 Source |
| 批量导入公司 | manual/event | 文件或 Resource | 多个 company discovery | 逐公司成功/失败报告 |
| 发现岗位列表来源 | 新增公司、URL 失效或人工复核 | Company、官网、历史证据 | source discovery | 新增、确认或拒绝 Source |
| 校验岗位列表来源 | 候选 Source 被接受或修复后复核 | Candidate Endpoint、active Listing Recipe | source validation | 仅保存页面与 trace 证据，等待发布判断 |
| 人工维护 Source | manual | 列表 URL、类别、参数 | data maintenance/validation | 新版本 Source |
| 首次全量初始化 | Source 首次可用 | Source、列表/详情 Recipe | baseline → listing/detail | 当前全部岗位基线和首个 Checkpoint |
| 第二次全量校准 | 首次基线完成或人工触发 | 已有基线 | reconcile | 验证分页、稳定身份、活动排序和边界 |
| 每日列表增量 | timer | 所有 active Source、旧 Checkpoint | listing sync | 边界前的新增与更新岗位 |
| 新岗位详情 | listing delta | 新 Job Posting | detail sync | 新岗位完整详情 |
| 变化岗位详情 | 岗位更新后重新进入列表顶部 | 已有 Job Posting | detail sync | 新内容版本 |
| 列表来源修复 | 列表失败或异常为空 | Source、Failure Artifact | repair | 新列表 Recipe/URL 或人工结论 |
| 岗位详情修复 | 详情失败或字段异常 | Job URL、Failure Artifact | repair | 新详情 Recipe/URL 或人工结论 |
| 登录/Profile 修复 | 认证失效、验证码 | 安全域和失败证据 | repair/waiting human | 恢复、暂停或终止 |
| 公司更新 | manual/event | 公司新信息 | data maintenance + 必要 rediscovery | 新版本公司和受影响 Work |
| 重复公司合并/拆分 | manual/质量事件 | 候选公司及关联数据 | preview + data maintenance/reconcile | 可逆逻辑映射；受控拆分 |
| 公司暂停/恢复 | manual | Company | 控制命令 | 停止/恢复新调度 |
| Source 暂停/恢复 | manual | Source | 控制命令 | 局部停止/恢复；自动熔断另记健康状态 |
| Source 重定向/归属调整 | redirect/manual | 旧新入口、所属公司 | validation/data maintenance | 保留历史并切换有效入口 |
| 公司归档/恢复 | manual | Company、影响范围 | data maintenance | 停止调度、保留历史或受控恢复 |
| 合规物理删除 | manual/合规请求 | Company、删除范围 | 独立合规 Work | M5 能力；不属于普通岗位生命周期 |
| Source 归档/恢复 | manual/失效确认 | Source | data maintenance/validation | 停止调度、保留历史并按兼容性恢复 |
| 数据纠错与重算 | manual/质量事件 | 字段、映射或新规则 | repair/reconcile | 新数据版本和影响报告 |
| 历史数据回填 | manual/规则升级 | 时间范围、字段、Target、模式 | reconcile | 新版本数据及覆盖/缺口报告，不推进日常 Checkpoint |
| Recipe 批量升级 | 新 Recipe 验证通过 | 受影响 Source 集合 | validation + staged rollout | 分阶段发布或回滚 |
| 临时手动运行 | manual | Target、运行模式、预算 | diagnostic/join occurrence/production Work | 诊断证据或受控生产结果 |
| 失败重试和人工结案 | event/manual | 原 Work 与证据 | retry/repair | 带 resolution 的成功、接受缺口或终止 |

### 5.8 每日大批量运行主流程

“每天运行所有抓取任务”的业务含义是：在各自时区和刷新窗口内，为每日截点时所有 **active Recruitment Source** 至少完成一次列表同步或记录一个明确、可解释的未完成结果。它不表示每天重抓全部历史岗位详情。

Daily Run 的固定和派生集合包括：

- 所有 active Source 的 `listing_sync`；
- 活动边界前新增和更新岗位产生的 `detail_sync`；
- 到达重试时间的失败 Work，以及修复方案发布后应重跑的 Work；
- 只计入统计但不自动执行的 `waiting_human` Work。

其中只有第一项是固定每日基线，其他项由当天新增、更新和异常派生。Source Discovery、首次全量、第二次校准和历史回填属于独立事件或人工任务，不因 Daily Run 每天重复执行。

```text
Daily Run 截点事务
  → 在同一个 Repeatable Read 快照中筛选全部 eligible Source
  → 原子写入 Daily Run、全部轻量 SourceOccurrence 和启动事件意图
  → 按窗口持续创建/激活 listing_sync Work
  → 读取每个 Source 上次成功的 Incremental Checkpoint
  → 从列表顶部按 activity_at 倒序扫描
       ├─ 边界前的新岗位   → detail_sync
       ├─ 边界前的已知岗位 → 视为更新并 detail_sync
       ├─ 到达旧边界       → 完成安全重叠窗口后停止
       └─ 顺序/边界异常     → 不推进 Checkpoint，进入 retry / repair / waiting_human
  → 原子保存观测、派生 detail_sync Work 并提交新 Incremental Checkpoint
  → 异步执行 Detail Recipe，幂等写入岗位新版本
  → 汇总 Company 健康状态
  → Daily Run 在所有应运行 Source 均有终态或明确等待原因后关闭
```

Daily Run 是面向用户和运营的统计/因果投影，不要求成为调度器中的 Work Bundle。它至少显示：

- 当日应运行、已创建、运行中、成功、失败、等待重试、等待人工和被策略跳过的 Source 数；
- 新增、更新和详情失败的岗位数；
- 未在运行窗口内完成的 Source 及原因；
- 各站点限流、熔断和容量影响；
- 从 Company → Source → Work → Attempt → Artifact 的追踪路径。

为了避免零点洪峰，截点只冻结约 20,000 条轻量 occurrence，不同时创建或启动全部抓取 Work。每条 occurrence 的 `due_at` 由 Source 稳定身份、日期和调度策略版本确定性散列到执行窗口；服务重启或 timer 重放得到同一名单和同一到期时间。窗口结束时对账 occurrence 终态，而不是再猜测当天本应有哪些 Source。

Daily Run 的口径不能用“有终态”掩盖采集缺口：

```text
listing_coverage
  = 成功到达旧边界并提交新 Checkpoint 的 active Source 数
    / 当日应运行 active Source 数

detail_completion
  = 已成功或有明确终止结论的当日详情 Work 数 / 当日产生的详情 Work 数
```

只有所有应运行 Source 都成功提交新 Checkpoint，且派生的 Detail Work 均成功时，Daily Run 才是 `completed`；存在失败、等待重试、等待人工、用户接受缺口、找不到边界、排序违约或窗口超时时标记 `completed_with_exceptions`，并保留未覆盖清单。人工终止可以令详情 Work 离开未完成队列，但不能把缺口计为采集成功。公司/Source 在截点前已暂停、归档或尚未完成四维契约验证时不进入当日分母；截点后才发生的暂停、归档或人工跳过保留 occurrence 并计为 `excluded`，不能计为采集成功。

Daily Run 截点固化 Source 及其当时的 Company/Source 配置版本和 occurrence key。截点后才 ready 的 Source 默认次日纳入；需要当天补跑时显式创建 catch-up occurrence。截点后暂停或归档不改写分母，而记录 `skipped_after_cutoff`、`canceled_by_archive` 等明确结果。日报关闭后不回写历史结论；后续修复以 `recovered` 补偿记录关联原 occurrence。

DailyRun 本身不提供修改窗口、名单、期望数或终态摘要的通用 mutation；这些都是截点/关闭事务产生的审计事实。窗口内的人工控制落在具体 SourceOccurrence：`recruiting.daily_run.occurrence.exclude` 只接受仍为 `planned`、尚无 Work 的 occurrence，并同时要求 DailyRun/Occurrence 精确 version、running 状态、当前业务时间早于冻结的 `window_end_at`。事务不删除 occurrence、不减少分母、不改变 DailyRun version，只将该项记为 `excluded`，保存 authenticated operator、原因、receipt 和事件。它与到期 Work 物化竞争同一 occurrence 行锁，最终只能是 excluded 或 queued；到期后不能把本应记录的窗口异常改写成人工排除。已 queued/running 的项使用 Work pause/cancel/retry，关闭后的缺口使用带原 occurrence 谱系的 production recovery。

### 5.9 公司、Source 与岗位数据规则

- 公司或 Source 更新采用版本化修改；正在运行的 Attempt 固定其接受时配置。Company 的名称或普通元数据更新不使采集结果失效；官网变更只派生 Source 关系复核，不能自动替换仍然有效的生产入口。

Company 官网变更使用独立的不可变 `CompanyWebsiteRevision`，并以小型 `CompanyWebsiteHead` 投影指出当前 revision。`company.update` 修改官网时，在同一事务推进 Company 的 `version/configuration_version`、追加 revision、移动 head、创建 `source_relationship_review` 人工 Work、保存 command receipt 和两条审计事件；现有 Source、Endpoint、Assignment、Checkpoint、Job 与历史观测一律不改写，也不因官网不同而自动停掉仍有效的生产入口。名称单独变化仍走普通 Company CAS，不制造官网 revision 或采集配置漂移。

官网回滚不是把 Company 或 head 的版本倒退，而是通过 `company.website.rollback` 精确引用当前 revision，恢复该 revision 的不可变 `previous_website`，同时继续追加新的 revision 和新的复核 Work；引用非当前 revision、其他 Company revision 或陈旧 Company version 均拒绝。官网变化推进 `configuration_version`，因此变化前已接受的 Discovery Attempt 结果按既有 Company 配置围栏拒绝并保留 rejected Artifact。后续显式 Source Discovery 自动绑定当前 `website_revision_id`，其 Work 以当前 revision 的复核 Work 为 `cause_work_id`；创建事务重新锁定 Company、website head、revision 与 Recipe，只有全部仍匹配时才允许形成新 discovery generation。人工可继续逐个接受/拒绝候选，并用通用 Work 结案流程完成关系复核。
- Company/Source 分别维护正交的 `control_status`、`readiness_status` 和健康/熔断事实。用户暂停、配置未验证和站点故障不能共用一个状态。
- 暂停命令必须给出 `pause_mode=drain|finish_causal_chain|cancel`。默认 `drain`：不创建新的自动 Work、冻结未开始 Work、允许已发出的外部请求结束；是否接受结果按命令产生的版本栅栏决定。恢复默认只创建一次 catch-up，不按暂停天数补造每日 Work；超过扫描预算时转校准或人工处理。

暂停不能只把 mode 写进 Company/Source JSON，也不能在用户事务里无界扫描全部历史 Work。实现必须拆开三种单调事实：实体 `version` 只负责命令 CAS；`configuration_version` 只在官网、Endpoint、Assignment、Profile 等会改变执行输入的配置变化时推进；`control_epoch` 在 pause/resume/archive 时推进并阻止新领取。Attempt 同时冻结配置版本和自身 Work acceptance fence，普通 drain/resume 不篡改不可变 Offer，也不把纯控制变化伪装成配置漂移；`cancel` 另推进 scope execution fence，使旧 Attempt 的结果只能保存为 rejected Artifact。

每次 Company/Source pause 创建一个扩展内部的 `ScopeControlOperation`，和实体状态、命令 receipt、审计事件在同一短事务提交。Work 在创建时保存不可变 `company_id/source_id` 调度归属并建立状态前导索引；现有 Recruiting reconcile 按 `(scope,status,work_id)` 游标每轮最多处理 500 项，操作状态和计数可恢复、可重放，不增加 Pause Actor/Worker。三种 mode 的执行语义是：

- `drain`：立即禁止新 Attempt；pause 前已经开始的 Attempt 可按原配置/Work fence 结算当前 Work，结算产生的后续 Work 保留但冻结，直到恢复或人工处置；
- `finish_causal_chain`：pause 事务只登记当时活动 Attempt 对应的有限根 Work，后续 Work 必须继承相同 operation/root 身份；只有这些根及其因果后代可在 paused scope 内继续领取，不能把 pause 前所有 backlog 当作“当前链”；
- `cancel`：scope execution fence 立即拒绝所有旧结果；reconcile 先按 Work 游标有界取消未终态 Work、过期活动 Attempt、释放 Permit，并为已固化的 SourceOccurrence 写明确异常结论，再进入独立的业务依赖结算阶段。已经预览但尚未物化 Work 的 BackfillItem 不能因 Work 扫描结束而遗漏，必须按每轮最多 500 项继续结算；`projection_completed` 与 `cancellation_completed` 是两个不同的持久事实，只有二者均完成且关联业务执行对象已有明确终态后 operation 才完成。Backfill 子 Work 的 Company/Source 归属必须从冻结 Item 派生，不能只继承父批次的粗粒度 Target。

取消验证不等于验证失败。Source Validation 的执行归属关闭后，若候选 Endpoint 未被并发修改，新 Source 回到 `candidate`，已有生产 Endpoint 的修复回到 `repairing`；二者在恢复 scope 后都可以重新验证，不写虚假的 `invalid` 质量结论。候选 Recipe 的 Listing、Detail 或 Discovery 验证取消后回到 `draft`，已发布 Recipe 的 rollout sample 只关闭本次 sample，不回退 Recipe。上述上层状态变化与 Work、Attempt、Permit 和 validation run 的取消在同一事务提交，并写出可审计事件。

Source 级 cancel 必须保持同 Company 其他 Source 的隔离。若 Backfill 的 Target 本身就是该 Source，则取消整个 Backfill；若 Company 级 Backfill 同时包含多个 Source，则只把被暂停 Source 的成员记为 `canceled`，其他成员与父批次继续运行。`canceled_items` 与 succeeded、accepted gap、failed 一起参与数据库事实重聚合，但取消不能伪装成用户接受的 gap；最后一个未终态成员结算后 Backfill 可进入 `completed`，只要存在 scope-canceled 成员，父 Work 就以 `terminated` 而不是 succeeded/accepted_gap 结案，并保留逐项取消事件。

Scope cancel 与 Backfill 自身的通用 cancel coordinator 共享同一 `canceling` 栅栏，但不共享不透明的进程状态。两者可以按任意顺序接续有界成员页：父 Work 已被另一协调路径取消属于幂等成功，其他终态仍视为矛盾；最后一页只能生成一个稳定 ID 的 `backfill.canceled` 事件。若结果先于 pause 事务提交则保留成功事实，若 pause execution fence 先提交，即使 Work 投影尚未运行，迟到结果也只能保存 rejected Artifact，不能生成 Output。

上述提交顺序规则适用于全部执行类型，不是 Backfill 特例。结果事务先完成时，后续 scope cancel 只能处理仍未终态的 Work，不得把已经 completed/succeeded 的 Source Discovery、Source Validation、Detail、Diagnostic 或 Recipe validation 及其证据反向改写为 canceled；scope execution fence 先提交时，晚到结果必须统一返回 fenced，并在业务事务零写入的前提下保留 bounded rejected Artifact。结果入口必须先校验静态 result kind、Artifact/Work 归属，再校验 Attempt/Work/scope fence，最后才判断 owner 是否仍为 running；否则 cancel 已关闭 owner 后会提前返回普通状态错误，丢失本应进入拒绝审计层的晚到证据。Baseline 的页面提交是进度截点而非成功终态：已接受页保持 Attempt 证据，cancel-first 的晚到页被拒绝，只有完整 finalize 后的成功事实才享有终态不可逆语义。

恢复必须先确认对应 pause operation 已达到可恢复终态。被冻结的历史 DailyRun/Occurrence 不逐日重开：旧日报保持 excluded/exception，系统只按当前配置和当前时间创建至多一个显式 catch-up occurrence；已有仍可安全继续的 manual/repair Work 按原因果身份恢复。Company 恢复时按 Source 游标分别判定，不能用一个大事务扇出全部 Source；Source 恢复不得影响同 Company 的其他 Source，且任何 pause/resume 都不移动 Incremental Checkpoint。
- 产品不判断岗位下架，不因岗位从列表中消失而更新或删除已有岗位。
- 移除 Source 实际执行可恢复归档：停止其后续调度，保留 Endpoint、Recipe Assignment、Checkpoint、岗位和运行历史。再次添加相同规范入口时优先提示恢复；恢复先验证身份、Recipe 和 Checkpoint 兼容性。

公开 `source.add` 遇到同 Company 下由已归档 Source 占用的 `canonical_source_key` 时，不创建第二个 Source，也不把普通唯一键错误留给 UI 猜测；它返回可机读 `source_restore_required`，并在错误 detail 中给出保留的 Source ID/version 和 `source.restore` 下一步。该拒绝事务不保留新 Source、command receipt 或新增事件。仍为活动状态的重复入口继续返回普通业务键冲突，不能把两个活动 Source 错误合并。

`source.restore` 把归档 Source 恢复为 `paused + repairing`，从最后一个 active Endpoint 复制新的验证 candidate，同时保留 Source ID、Company 归属、Assignment、Checkpoint、Job 和历史观测。恢复事务还创建一个使用归档后 execution fence 的 cancel-mode `ScopeControlOperation`，有界取消/收束归档前遗留 Work；只有该操作完成，后续 `source.resume` 才可逆转它。这里的 resume 只开放人工修复执行：readiness 仍为 `repairing`，所以 Source 仍不进入 DailyRun。操作者必须用保留的 Assignment version 启动真实 Source validation、提交 Attempt/Artifact 证据并发布新的 ContractAssessment；只有形成 `active + ready + verified contract` 后才重新具备每日调度资格。整个流程不重建、不移动也不倒退原 Incremental Checkpoint。
- 公司“删除”默认是可恢复归档：停止公司及 Source 调度，保留审计和历史岗位。
- 公司恢复进入 `paused`，经入口验证后再显式恢复调度。公司及子数据的物理删除属于 M5 独立合规操作，必须展示影响范围、执行权限/审批、保留期和删除结果；岗位下架或普通采集失败不得触发它。
- 批量更新、删除和 Recipe 发布必须逐项记录结果，允许部分失败重试，不能只返回一个模糊的整体成功。
- 第一版公司合并只建立带生效区间的 canonical/alias 映射，不批量改写历史事实；拆分和 Source 改归属使用预览、二次确认及显式归属分配。入口身份改变时默认归档旧 Source、创建带 `supersedes/split_from` 谱系的新 Source。
- `company.merge.preview` 同时承担合并和逻辑撤销预览：输入 canonical、1—500 个 alias 及全部 Company expected version，输出规范排序的成员、当时的 Source/Job/未终态 Work 影响计数、mapping version 和 `preview_hash`。`company.merge.confirm` 只接受精确 preview version/hash，并在一个短事务内重新锁定全部 Company 与 mapping version；合并追加新的 active alias 生效区间，撤销只关闭原区间，两者都不修改 Company/Source/Job、Assignment、Checkpoint 或历史日报。活动 alias 的 Source 不进入后续每日截点，`company.get` 明示 canonical 映射，并在事务内拒绝继续向 alias 新增 Source；撤销后才恢复新增和次日调度。已经截点或在途的 Work 继续按原冻结身份完成，避免把治理操作伪装成历史迁移。
- 同一 Source 的入口修正不直接覆盖 active Endpoint。`source.update` 先生成更高且永不复用的 endpoint revision；已有 active Endpoint 时，同时追加不可变 `SourceEndpointChange`，明确 `redirect|correction`、旧新入口、操作者、理由和 staged Source version。已有调用方未声明类型时按 `correction` 兼容，但 UI 应要求操作者显式选择。Change 只是变更意图，不能表示入口已经生效；候选被拒绝或被后续候选取代时保留 Change，且永远没有对应 Activation。
- Endpoint cutover 仍复用 Source validation 状态机和同一种 Listing 执行能力，不增加专用 Worker。真实验证 Work/Attempt/Artifact 证明身份、分页、活动时间倒序和更新置顶契约后，`source.validation.publish` 才在更新 active Endpoint 的同一事务追加不可变 `SourceEndpointActivation`，绑定 Change、旧新 revision、Listing Assignment、Source version 和证据 Artifact。任何失败都会回滚 Endpoint 与 Activation；切换不移动 Incremental Checkpoint。公开 `source.endpoint.history` 返回 staged change 与可选 activation，使运营员能区分“提出过”“验证生效”和“拒绝/废弃”。
- 跨 Company 的归属纠正不能修改既有 `Source.CompanyID`。`source.reassign.preview` 冻结旧 Source/目标 Company 版本、active Endpoint、Listing Assignment version、Checkpoint version、影响计数、新 Source ID 与 `supersedes|split_from` 关系，并产生精确 hash；`source.reassign.confirm` 必须再次锁定当前事实并匹配 version/hash。确认后创建目标 Company 下独立的 candidate Source 和不可变 `SourceLineage`；Job、Assignment、Checkpoint、历史 Observation/Artifact 均留在旧 Source，不复制也不改写。`supersedes` 同事务归档旧 Source并创建 cancel-mode ScopeControlOperation，`split_from` 保留旧 Source 活动且不创建取消操作。新 Source 必须独立完成验证和基线后才能进入每日调度。
- 原始 `ListingObservation`、已验证详情版本和 `CuratedOverride` 分层保存。有效字段优先级为 `manual override > verified detail > listing observation`；人工覆盖可撤销或过期，后续抓取仍保存来源事实但不静默覆盖有效人工值。公开 `job.correct` 的 set/clear 命令同时使用 Job version 与当前 override-head identity/version 双重 CAS；每次变更追加不可变 override version，并把命令 receipt 和不含字段值的审计事件原子提交，绝不改写 Job、Observation 或 DetailVersion。`job.correction.get` 按 Job+field 返回当前 head 与有界历史，让其他会话无需数据库权限即可安全接续；撤销只移除人工优先级，底层最新 verified/listing 事实会重新生效。
- 历史回填必须声明 `artifact_recompute|live_refetch`。后者只是重新访问当前网页，不得声称恢复历史快照；普通回填永不读取或推进日常 Incremental Checkpoint。

`artifact_recompute` 的选择单位是时间范围内每一个已接受的 `JobDetailVersion`，不是 Job 当前指针，也不是每个 Job 只取一条；同一岗位的多个历史版本必须分别冻结 `detail_version_id + Artifact + observed_at`，输出只能把该原始观测时间声明为其历史时点。`live_refetch` 的选择单位是时间范围内出现过 Listing Observation 的唯一 Job，冻结当前 Job/Source/Recipe fence 后重新访问当前详情页；其输出只记录实际 refetch 时间且 `claims_historical_snapshot=false`。两种模式均建立独立不可变输出谱系，不调用正常 Detail 接受事务、不修改 Job 当前详情版本或 refresh generation；若需发布到当前业务视图，必须另走数据纠正与重算的显式 plan/审批。

回填预览按稳定 seek cursor、每块最多 500 项冻结，不能在确认时重新执行范围查询。preview hash 使用绑定 Target、模式、半开时间范围、字段、Recipe/policy 版本和全部冻结项的增量 SHA-256 accumulator；因此内存和事务大小不随总项数增长。预览结束后父 Work 才进入 `waiting_human(preview_ready)`，用户必须同时提交精确 Backfill version、父 Work version 和 preview hash 才能启动。历史 Artifact 缺失、已拒绝或不可读必须成为逐项可审计 gap/failed 事实，不能悄悄退化为 live refetch。

回填运维沿用 Atoll 的 Work、Attempt、Artifact 和 durable reconcile 原语，不增加专用 Worker。暂停采用 drain：停止新成员物化与领取，已经运行的成员仍可按确认时的不可变围栏结算；恢复只允许没有未处置失败项的人工暂停批次。失败成员可由用户接受为永久 gap，或在修复后创建有因果链接的新 Work；已经在物化前因版本漂移失败的成员不得重用旧预览。运行中取消先写入 `canceling` 栅栏，再由协调器以每轮最多 500 项的小事务取消成员、拒绝活动 Attempt 和释放预算，所有成员有明确终态后才把父 Work 标为 canceled。Output 列表只返回血缘元数据，正文按单条、有界查询读取；gap 报告必须同时保留未处理失败与 accepted gap，后者不能计入成功覆盖率。

### 5.10 浏览器插件与 Recipe 复用闭环

Recipe 是已经确认的核心产品资产，不只是待选技术。其价值是把一次性分析成本转换成可重复执行的代码：

```text
没有可用 Recipe
  → 浏览器插件/Agent/用户在真实网站发现流程
  → 捕获 DOM、网络请求、分页、字段映射和必要交互
  → 生成版本化 Recipe 代码
  → 用真实样本执行和质量验证
  → 发布 active Recipe

日常运行
  → 直接查找 active Recipe
  → 直接执行代码
  → 不在执行前调用 AI 分析页面

Recipe 失败或质量退化
  → 保存 Failure Artifact
  → 确定性兼容规则尝试
  → 必要时浏览器插件/Agent/用户修复
  → 验证新版本
  → 灰度发布并保留回滚版本
```

Recipe 至少分为：

- **Listing Recipe**：给定已保存的岗位列表 URL 和上次成功 Checkpoint，从顶部按最新活动时间倒序读取，输出边界前的岗位及边界证明；
- **Detail Recipe**：给定岗位 URL，输出结构化岗位详情；
- **Discovery Recipe/Procedure**：辅助从公司官网等入口找到候选岗位列表 URL，只在接入、入口失效或人工要求复核时运行。

每日主链路使用已保存的岗位列表 URL 和 Listing Recipe 获得旧活动边界之前的新增、更新岗位 URL，再执行 Detail Recipe。发现岗位列表 URL 不是每日步骤；边界之后的历史岗位不每日重复获取。

Recipe 可以是 HTTP 请求流程、Browser DOM 操作、Extension 录制脚本或它们的组合。代码执行仍有网络和计算成本，但不再承担重复的 AI 推理与流程发现成本。

Browser Extension 保留为 capability，而不是系统必经层：能由 HTTP/API Recipe 完成的每日任务不启动浏览器；只有需要 JavaScript、登录/Profile、网络捕获、交互录制或现场修复时才使用 Extension/Browser。

### 5.11 活动边界增量契约

产品明确采用以下来源条件：

1. 岗位列表按岗位最新活动时间倒序排列；
2. 新岗位一定进入列表顶部；
3. 历史岗位更新后一定按照新的活动时间重新进入列表顶部；
4. 岗位有稳定来源 ID，或有可规范化的稳定详情 URL；
5. 置顶、广告等非时间排序项能够被 Recipe 识别和排除；
6. 延迟插入和分页抖动必须落在 Recipe 配置的安全重叠窗口内。

若某个 Source 经过真实验证后不满足这些条件，则该 Source 不支持本产品的可靠日常增量，进入 Recipe 修复或人工处理；系统不退回到每日全量扫描冒充增量。

这里的 Checkpoint 不是从网站读取的通用“水位”，而是 Recruiting Actor 在上一次成功运行后保存于其 Resource 的增量检查点：

```text
IncrementalCheckpoint
  source_id
  listing_recipe_id
  listing_recipe_version
  strategy = activity_desc
  frontier_activity_at?
  frontier_job_keys[]       # 有顺序的边界签名，不是无序集合
  boundary_match_min_items?
  overlap_pages / overlap_items
  last_success_occurrence_id
  committed_at
```

- 网站能输出可靠 `updated_at/published_at` 时，使用 `(activity_at, source_job_id)` 作为排序边界，其中 `activity_at` 表示网站用于倒序的最新活动时间。
- 网站不输出活动时间但保证更新后重新置顶时，使用有顺序的 `frontier_job_keys` 作为边界签名，不能遇到第一个已知岗位就停止。旧边界签名稳定出现之前的已知岗位视为可能更新并重新抓取详情；重叠窗口内的已知岗位也按 Recipe 策略复核。
- 活动时间可提取时可以精确判断更新；仅依赖顺序签名时属于经过验证的站点策略，必须明确其边界长度和可接受漏检风险，不能宣称具有时间字段时同等的严格性。
- 普通分页的 `next_cursor` 只用于同一次运行续页，不自动成为跨日 Checkpoint；只有网站明确提供 change cursor/sync token 时才可跨日保存。

`previous_frontier_reached` 的含义随策略确定：时间策略表示扫描结果已经跨过旧 `frontier_activity_at`；无时间字段策略表示匹配到足够长且顺序稳定的旧边界签名。岗位删除虽不在产品范围内，但可能令 key-only 边界消失；这种情况只能扩大扫描并重新建立边界，不能把边界缺失解释成岗位删除。

Listing Recipe 输出增量观测和进度证明，而不是全量快照：

```text
ListingDelta
  observations[]
    source_job_key
    detail_url
    activity_at?
    listing_fingerprint?
    relation_to_frontier: before | boundary | overlap

  progress
    previous_frontier_reached
    overlap_completed
    ordering_contract_held
    pages_scanned
    termination_reason
    candidate_frontier
```

日常算法：

```text
读取 Source 的已提交 Checkpoint
  → 从列表顶部开始
  → 对旧边界前的每个岗位：
       新 source_job_key      → 创建详情 Work
       已知 key 且活动时间变大 → 创建详情更新 Work
       已知 key 但无时间字段   → 视为可能更新并创建详情 Work
  → 到达旧边界
  → 完成安全重叠窗口
  → 验证倒序契约未被破坏
  → 原子保存岗位观测、创建 Detail Work 并提交 candidate_frontier
```

只有 `previous_frontier_reached && overlap_completed && ordering_contract_held` 才能推进 Checkpoint。中途失败、长期找不到边界、发生异常逆序或 Recipe/身份提取规则变更时，旧 Checkpoint 保持不变，并进入扩大扫描、重新校准或人工修复。

首次全量的目的之一是建立第一个 Checkpoint；第二次校准用于验证稳定身份、排序、更新后重新置顶、同时间岗位和重叠窗口，而不是用于日常集合差集。

基线全量采用 `baseline_generation` 和持久 staging 分页续跑；中间游标失效时从头重扫并按来源岗位键收敛，中间结果不能冒充已提交 Checkpoint。完成口径分为 `listing_finalized`、`details_pending` 和 `completed`，列表安全完成后可提交 Checkpoint，详情失败则保留旧可用数据并形成明确缺口。第二次校准把稳定身份、分页、当前排序和“历史更新后重新置顶”分别给出 `verified|unverified|violated` 结论；没有真实更新样本时，不能把“更新置顶”标成 verified。

同一 `activity_at` 跨页时必须读完整个同时间组；网站没有稳定次序时不能人为拼接 `(time,id)` 后提前停止。扩大扫描必须有页数、时长和成本上限，超限转校准、修复或人工处理。Checkpoint 提交使用 `source_id + expected_checkpoint_version` 的 CAS；列表观测、Detail Work 意图和新 Checkpoint 要么原子提交，要么通过可重放意图恢复。

### 5.12 逐场景模拟结论

2026-09-07 使用 25 个相互独立的场景 Agent，分别扮演运营用户，模拟正常路径、重复命令、定时与人工并发、崩溃、陈旧结果及人工介入。该演练验证的是设计完整性，不替代真实网站或容量测试。

| 场景组 | 结论 | 本版收敛的关键规则 |
|---|---|---|
| 公司新增、批量导入、Source 发现/维护 | 可行但原契约不足 | 命令 schema、业务去重、候选拒绝、逐项结果、Source 草稿验证后发布 |
| 首次全量、第二次校准 | 可行但不能把“两次”当证明 | baseline generation、分段 staging、分阶段完成、四项独立校准结论 |
| 每日列表增量、新/变化岗位详情 | 核心算法成立 | Checkpoint CAS、同时间组、详情 generation、原始观测与有效数据分层 |
| Listing/Detail/Profile 修复 | 可行但需共享故障收敛 | failure domain、单飞 Repair Work、Recipe quarantine、Profile 安全 Resource、积压有界恢复 |
| Company/Source 更新、暂停、恢复、归档 | 可行但控制语义不足 | 控制/可用性/健康正交、pause mode、截点口径、一次 catch-up、逻辑归档 |
| 公司合并/拆分、Source 改归属 | 不支持首版物理迁移 | canonical/alias 和谱系映射；复杂拆分走预览、审批及受控迁移 |
| 数据纠正、重算、历史回填 | 可行但需血缘与模式 | Observation/Override 分层、plan hash、规则版本、回填不推进 Checkpoint |
| Recipe 批量升级 | 可行但需逐 Source 发布 | Recipe Assignment、contract hash、灰度、Checkpoint 兼容矩阵与回滚 |
| 临时运行、重试、人工结案 | 可行但必须消除歧义 | 三种 run mode、重试策略快照、结构化 resolution、日报补偿记录 |
| 10K 公司/20K Source 日常运行 | 逻辑架构成立，生产能力待压测 | 持久 SourceOccurrence、控制面短事务、Executor 多实例、共享熔断、预算许可、紧凑消息 |

演练没有推出新的角色型 Worker。一个 Recruiting Actor 表示一个公开领域权威，不表示一个单线程进程；一个 Executor class 也不表示一个实例。真正需要增加的是版本化事实、接受条件、故障域和用户操作契约。

## 6. 核心设计原则

### 6.0 招聘产品只能扩展 Atoll，不能修改 Atoll

这是开发与架构的最高优先级约束：招聘产品必须使用 Atoll 已公开的 Actor、Message、Channel、ledger、timer、Resource 与 Driver 能力实现，不修改 `protocol/`、`runtime/`、`lib/`、`platform/`、`registry/` 的代码或架构语义。吞吐、调度、状态机、Recipe、浏览器和人工运维能力均属于招聘扩展自身；不得为了业务便利向 Atoll core 塞入招聘消息、字段、队列、Worker 或特例。

如果实现过程中发现公开能力不足，该功能停止在扩展边界内，输出可复现的能力缺口与替代方案，由独立的 Atoll 架构决策处理；招聘功能分支不得自行修改核心。

### 6.1 Atoll 是控制权威

业务命令、Actor 间控制交互和有协作价值的领域事件通过 Atoll Message 发生，受 Channel membership 和 capability 约束。Recruiting Actor 是领域行为权威；大规模事实可委托给其控制的 Resource，但 Resource 不是第二控制面。

HTTP、Web、飞书或 CLI 可以作为 Gateway/Driver。禁止的是绕过 Atoll 身份、权限、因果和领域校验，不是禁止 HTTP。

### 6.2 Actor 是身份与生命周期边界

Actor 不是模块、进程池或微服务的同义词。第一版将规划、调度判断、结果提交和恢复作为 Recruiting Actor 内部 handler。只有出现明确身份、授权、位置、故障或生命周期边界时才拆分。

### 6.3 Channel 是协作与权限边界

Channel 不作为数据库分片或队列分区。第一版默认一个招聘业务 Channel。只有参与者、可见性、权限或上下文确实不同才增加 Channel；执行吞吐先由 Resource data plane 和执行协议处理。

### 6.4 确定性优先

- 已验证 Recipe 的正常执行不调用 LLM；
- 能用 HTTP/API 时不启动浏览器；
- 能用规则判断时不调用 AI；
- AI 生成的 Recipe 必须真实执行和质量验证；
- AI 只能调用公开能力，不能直接写数据。

### 6.5 Work 与 Attempt 分离

Work 是用户能理解和运维的业务工作；Attempt 是对 Work 的一次机器执行。执行可能重复，业务结果幂等生效。旧 Attempt 的延迟结果不能覆盖后来已经接受的结果。

分页执行状态也属于 Attempt，而不是 Work：每个 Attempt 的页码从 1 开始，页面 Artifact 和已接受 Observation 作为历史证据保留；新 Attempt 不续用旧 Attempt 的进程内游标或页码，最终质量证明只汇总本 Attempt 的页面。Work 负责把这些执行历史聚合为同一个可运维业务工作。

### 6.6 先定义不变量，再选分发协议

第一版不冻结 Pull、Push 或 Lease。任何方案必须满足：

1. 只有授权且能力匹配的 Executor 可以执行；
2. 一个 Work 可有多个 Attempt，但结果按当前接受条件生效；
3. 执行进程消失后 Work 能恢复；
4. 重复命令、交付和结果不重复产生业务数据；
5. 取消、暂停和人工修正不被旧结果覆盖；
6. 能实施站点、凭证和全局资源预算。

是否需要 lease、heartbeat 和 token，根据任务时长、Atoll incarnation、跨进程恢复和故障测试决定。即使不用 lease，也必须使用 `attempt_id` 和版本条件隔离陈旧结果。首版不增加 Executor 自报 heartbeat：Recruiting Actor 只读取 Atoll 已有 `system.member.list` 的 substrate-owned `present/uptime` 作为快速失效信号；它只能加速回收，不能替代 Attempt/incarnation/acceptance/domain fence，长期无进展超时仍是兜底。

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
│ Domain facts / Recipe / Artifact / HTTP / Browser / AI      │
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
| Target、Recipe、Work、Attempt 接受规则和招聘数据 | Recruiting Actor |
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

第一版只冻结一个 `recruiting-executor` Actor class，可声明 `http.fetch`、`browser.recipe`、`browser.profile`、`extension.capture`、`recipe.validate` 等 capability。同一执行者可具备多项能力；步骤种类不等于 Executor 类型。浏览器插件只捕获用户确认的 Candidate Recipe、selector trace 和 Artifact，不成为每日执行所依赖的 Worker；固定流程发布后由 HTTP 或受控 Browser Driver 重放，插件不能直接激活 Recipe、修改 Source 或推进 Checkpoint。

### 8.3 首批公开领域词

```text
recruiting.company.add / update / pause / resume / archive / delete
recruiting.company.import / import.get / import.items / import.confirm / import.cancel / import.item.resolve / merge.preview / merge.confirm / restore
recruiting.company.get / list
recruiting.source.add / update / validate / pause / resume / archive / restore
recruiting.source.discover / get / list
recruiting.job.get / list / correct
recruiting.work.create / get / list / pause / resume
recruiting.work.correct / retry / cancel / resolve
recruiting.run.diagnostic / join_occurrence / production
recruiting.recipe.inspect / validate / approve / reject / rollout / rollback
recruiting.recipe.rollout.batch / batch.get / batch.items / batch.confirm / batch.resume / batch.rollback / batch.cancel
recruiting.execution.offer / accept / started / result / failed
recruiting.daily_run.get / list / summary / occurrence.exclude
recruiting.jobs.search
recruiting.system.status
recruiting.capacity.status
```

`offer/accept` 可映射为推送、拉取或其他机制；公开词不暴露数据库领取语义。

所有修改命令至少携带 `command_id`、Target、`expected_version` 和原因；批量命令再携带输入 Artifact 哈希、schema/policy 版本，并产生逐项结果。`command_id` 只处理传输重放，业务重复还要使用各场景稳定键。高风险批量变更采用 `preview_hash + expected_versions` 确认，审批后选择范围变化则必须重新预览。

公司导入的第一阶段采用 `company-import.v1` CSV Resource（`company_id,name,website`），`recruiting.company.import` 只在控制面原子创建父 Work、导入聚合、receipt、event 和定向 dispatch，不把文件正文放进 Message、Actor State 或 MySQL。仍是同一个 `recruiting-executor` Actor class 的 `company.import` capability 读取 File Resource、复核原始字节 SHA-256、解析并以最多 500 项的结果 envelope 提交；控制面以 `batch_version + chunk_sequence` 原子保存分片，最终从已落库项重新计算 `preview_hash`。Executor 重启按持久 `item_count` 继续，分片大小改变也不重传已确认项。预览 Attempt 成功后父 Work 进入 `waiting_human(preview_ready)`，不能提前冒充整批完成；用户通过 `import.get/import.items` 分页审阅，并以 `preview_hash + expected_version` 执行 `import.confirm`，从而把批准严格绑定到所见内容。

确认事务本身不修改 Company，而是原子启动一个 `company_import_apply` 协调 Work。它仍由同一 Executor class 和 `company.import` capability 领取，每次 immutable offer 最多携带配置的 500 个待处理项；Executor 只确认这一有界 envelope，Company 的权威写入留在控制面。每个预览项各用一个独立事务创建自己的 `company_import_item` 子 Work、Company（适用时）、逐项 outcome 和 event：单项业务冲突进入 `waiting_human`，输入内重复项为 `skipped`，不会回滚其他成功项。进程在中途退出后，已存在 outcome 的项被幂等跳过；仍有待处理项时同一协调 Work 进入 `waiting_retry` 并产生下一次持久 dispatch；若最后一个 item 已提交但页级回执丢失，恢复后的空 finalizer offer 负责完成汇总，不会留下无 pending item 的悬挂批次。全部项终结后，`CompanyImport` 保存结构化汇总；无异常时父 Work 成功，有失败或等待人工时父 Work 保持 `waiting_human`，不得把部分成功冒充整批成功。

等待人工的导入项通过 `recruiting.company.import.item.resolve` 处理，不重开整批，也不修改输入 Resource 或预览 `state_json`。`retry` 只允许原本为 `ready`、但应用时发生数据库业务键冲突的项；运营员先通过正常 Company 运维命令消除外部冲突，再以精确 batch/item version 重试原项。输入本身不合法的预览项不能在导入历史上改字段，只能由用户明确 `skip` 并记录身份和原因；若需要纠正字段，应上传新 Resource 建立新批次。每次处理在一个短事务内锁定 Batch、Item、子 Work 和父 Work，写 Company（重试时）、逐项 outcome、Work resolution、command receipt 和审计 event，再从全部规范化 item outcome 重算 `BatchOutcome`。仍有异常时父 Work 继续等待；最后一项解决后父 Work 才成功结案。相同命令并发到达收敛为一次提交和一次稳定重放。

`recruiting.company.import.cancel` 适用于预览或应用中的批次。请求事务先把批次推进到 `canceling`，取消已有协调 Work 并提升 acceptance fence，使旧 Executor 结果立即失效，再创建同一 capability 的高优先级取消协调 Work。已经成功、跳过或等待人工的逐项事实保持不变；尚未开始的项目仍按有界页和独立事务创建 canceled 子 Work/outcome，不能以一次大范围 UPDATE 伪造逐项控制边界。全部未开始项终结后批次和父 Work 才进入 `canceled`。

Recipe 批量发布使用 `recipe-rollout-sources.v1` JSON Resource，正文只含相同 `schema_version` 和最多 20,000 个唯一规范 `source_ids`；命令另携带原始字节 SHA-256、policy version、目标 active Recipe、canary size 和不超过 500 的 wave size。Recruiting Actor 可读取 KV 或 File Resource，严格拒绝未知字段、尾随 JSON、重复或带空白的 Source ID，并以 `SHA-256(batch_id|source_id)` 形成与输入排列无关的确定性 canary 顺序。预览按最多 500 项的短事务逐块重读 Source、当前 Assignment/Recipe 和目标 Recipe；最终 preview hash 同时绑定输入 hash、schema/policy、目标 Recipe 的 kind/scope/contract/capability 及每个 Source/Assignment version。完成预览时父 Work 进入 `waiting_human(preview_ready)`。确认只原子启动批次和父 Work、开放第一个 canary 范围，不提前修改任何 Source，也不产生 Executor dispatch；后续逐 Source 发布、校验与 wave 推进由 Recruiting Actor 的有界 reconcile 完成。

Reconcile 的“有界”同时约束数据库工作与公平性：每个 timer tick 的总成员预算固定，按本轮活动 Batch 数量分配，而不是让第一个大 Batch 独占。成员工作清单只返回当前能推进的状态；已经绑定且 Work 仍为 `open/running/waiting_retry` 的 validation 不反复占用扫描配额，Work 到达 `waiting_human/completed/canceled` 后又重新成为可处理项。这样一个慢站点不会阻止同 wave 后续 Source 创建验证，也不会长期饿死其他 scope 的批次。Wave 进度事件属于版本单调的 `recipe_rollout_batch` 聚合，父 Work 只在暂停、恢复和终结时变更，避免第二个 wave 复用父 Work version 造成事件冲突。

Listing 与 Detail 共享上述批次、成员、Attempt 和 wave 状态机，但验证策略属于业务领域，不强行抽成一种动作。Listing Assignment 切换后 Source 离开每日调度并执行完整 Source validation，成功发布新的活动倒序、分页和身份校准。Detail Assignment 切换不改变 Source 的 Listing 就绪状态；控制面从该 Source 当前 `available` 岗位中按稳定主键选择一个真实样本，使用目标 active Recipe 创建独立 `recipe_validation` Work。该执行只保存 response/trace Artifact、抽取字段数和规范化哈希，不写 Job、DetailVersion 或刷新代次；样本 Job、Source、Company、Assignment、Recipe 或 Profile 任一 fence 改变都会拒绝结果。没有可用样本不是“默认成功”，而是暂停该 wave 并等待人工补齐或选择回滚。

批量发布任一 wave 失败后，固定该 wave 并暂停父 Work，用户可以在修复后显式 `batch.resume`，也可以显式 `batch.rollback`。批量 rollback 的产品含义是回滚该批次已经发布的完整前缀，而不是只回滚报错成员：此前成功 wave 与当前 wave 中已经切换的成员都恢复到各自预览时冻结的 Assignment；尚未发布的后续成员不产生无意义版本。回滚按确定性逆序、每批最多 500 项推进，每个 Source 仍复用逐 Source Assignment/Checkpoint 原子切换，追加新 Assignment version，绝不删除目标版本、验证证据或已接受岗位事实。Listing 回滚后必须用旧 Recipe 创建新的 Source validation generation；Detail 回滚保持 Source ready，但也必须用恢复后的旧 Assignment 创建新的 evidence-only Job sample validation。只有全部已发布成员重新验证成功，批次才进入 `rolled_back` 并释放 active scope。进程退出只续跑未完成成员；版本漂移、旧 Recipe 被隔离、活动验证未终结或回滚验证失败都会暂停 rollback 并交给人处理，不得跳过后把父 Work 伪装成成功。

## 9. 最小领域模型

第一版冻结业务数据模型和运行控制模型两个层次。业务对象不能为了架构简洁而被抽象掉；运行模型包含 Recipe、Recipe Assignment、Incremental Checkpoint、SourceOccurrence、Work、Attempt、Artifact 和受控 Profile Resource，不复制历史项目的多层任务结构。

### 9.1 Company、Recruitment Source 与 Job Posting

```text
Company 1 ── 0..N Recruitment Source 1 ── 0..N Job Posting
```

- Company 保存稳定主体身份、规范名称、官网、别名、控制状态和版本；名称不是唯一键，规范官网域名或受信外部主体 ID 只作为确定重复及人工提示的依据。
- Recruitment Source 保存所属公司、来源类型、规范入口键、版本化 Endpoint、招聘类别、刷新策略、Recipe Assignment、最近成功 Checkpoint、控制状态和就绪状态。候选配置先验证，再原子发布；验证期间保留最后可用生产配置。
- Job Posting 先表达 `SourceJob`：保存 Source、来源岗位 ID、规范化详情 URL、列表观测、详情版本、首次发现时间和最近来源活动时间。跨 Source 的同一真实岗位使用有证据的映射/投影关联，不能靠改写来源身份合并。

Source 的 `canonical_source_key` 由规范 Endpoint、逻辑参数/招聘类别和规范化规则版本组成，在 Company 内唯一；同一规范入口跨 Company 冲突必须人工裁决，不能静默共享或改归属。Discovery 候选至少包含规范入口、最终跳转链、逻辑类别、归属证据、置信依据、Artifact 和发现时间。

Company 和 Source 的最小控制状态为 `active | paused | archived`，并分别保存接入/可用性状态和独立健康事实。Job Posting 只表达已发现、详情待获取、可用和更新待获取，不表达下架或删除。详情更新以 `refresh_generation` 隔离跨 Work 的迟到结果，相同规范化内容哈希不产生重复内容版本。

Target 是 Work 对可调度对象的统一引用（`target_type + target_id`），不是用来代替三类业务实体的万能表。

### 9.2 Recipe

Recipe 是版本化、可直接执行的确定性代码资产，描述如何完成列表枚举、详情获取、来源发现辅助或验证。它可以调用 HTTP、Browser、持久 Profile 和 Browser Extension capability，也可以组合这些能力。

Recipe 的生产入口是“按 scope 查找 active 版本并执行”，不是“先让 AI 分析网站”。只有无可用 Recipe、Recipe 执行失败或质量检查失败时，才进入发现或修复流程。

```text
draft → validating → active → superseded
  ↑         │          ├→ quarantined → validating
  └─────────┘          └→ disabled
       validation_failed
```

Recipe 至少保存 `recipe_id`、`kind=list|detail|discovery`、适用 scope、ABI 版本、不可解析的代码/Resource 引用、transport、所需 capability、输入输出契约、版本、内容哈希、验证证据和状态。上述执行契约属于 Recipe version 的不可变内容，不能在原版本上把 HTTP 静默换成 Browser 或改变 capability；变化必须创建新版本并重新验证。Listing Recipe 还必须保存活动倒序、稳定身份、边界、重叠窗口和异常终止契约。同一 scope/kind 可以有一个默认 active 版本，但生产执行由版本化 `SourceRecipeAssignment` 决定，从而支持逐 Source 灰度和回滚。

Assignment 固定 `source_id + kind + recipe_version + contract_hash + effective_at/version`。`contract_hash` 覆盖岗位身份、排序、分页和边界语义；变化时必须证明 Checkpoint 兼容，或者重新校准。每次发布、灰度或回滚都追加不可变 Assignment 历史；回滚引用一个确实存在的历史版本，但产生的新 Assignment version 必须继续单调递增，不能把当前版本号倒退，也不能从事件日志猜测已经丢失的旧配置。

Recipe 共享故障时先把该 Recipe version 原子推进到 `quarantined`。隔离不对所有 Source 做扇出更新：执行领取时必须重新要求 Recipe 为 active，因此不会基于已隔离版本创建新 Attempt；现有 Source Assignment 仍保留为“当时选择了什么”的事实，方便查询影响面和逐 Source 恢复。Source 回滚必须选择同 kind、同 contract hash、同 capability 且当前为 active 的历史 Recipe，并以 Source version 与当前 Assignment version 双重 CAS 原子更新 Source 投影、当前 Assignment、Assignment 历史、命令 receipt 和审计事件。Listing Recipe 回滚若存在 Checkpoint，还必须确认它精确绑定当前 Assignment；事务只把 Checkpoint 的 Recipe ID/version 重绑定到兼容的历史 Recipe 并提升 fencing version，frontier、同时间组、overlap 和最后 occurrence 等水位事实保持不变。无论是否已有 Checkpoint，回滚后的 Source 都进入 `repairing`、清除旧校准结论并暂停每日调度，只有重新验证通过才能恢复 `ready`；contract 或 capability 不同则拒绝普通回滚，进入重新基线/迁移流程。

候选正文必须先由操作者或插件保存为权限受控的 `recipe://`/`artifact://` Resource，再通过 `recipe.propose` 进入领域系统。Listing/Detail 提案以 Source 为并发目标，使用 `expected_version` 和 Endpoint revision 绑定来源；Discovery 提案则以 Company 为并发目标，用 Company version 与官网 URL 绑定来源，不能为了复用 Source 围栏而创建假 Source，也不携带 Endpoint revision 或 Extension Capture。Actor 自己读取 Resource，并与 Executor 共享同一个 256 KiB、拒绝未知字段/尾随 JSON 的 ABI 解码器。客户端只能声明 Resource 引用及预期 content hash，不能提交 scope、contract hash、状态或 Assignment；Actor 从实际 Spec 计算规范 content hash，从 transport、Extraction、pagination 和 Listing boundary 计算兼容性 contract hash。事务只创建 immutable draft、command receipt 和审计事件，不改变 Company/Source/Assignment/Checkpoint。插件路径仅用于已知 Source 的 Listing/Detail，并另外提交严格有界的 Extension Capture Resource；Actor 将其中的 capture identity、消息信封操作者、Source/version、active Endpoint revision/URL、Candidate hash 与 Recipe Resource 逐项交叉校验，并把 trace/evidence 引用作为独立不可变 Proposal 事实和 Draft 原子保存。`recipe.inspect` 可审计其来源；该事实没有激活或生产写入权限。开发预览插件通过 loopback-only、随机令牌配对的 Recruiting Bridge 使用普通用户现有协议，不放宽 Atoll 的同源 WebSocket 防线，也不新增 Worker/Actor；声明式 HTML Listing 与 Detail 的真实 DOM 捕获内核和 Bridge→Atoll 链路已验证。Listing 页面由 active Endpoint 围栏并从其推导 scope；Detail 页面必须绑定已属于该 Source 的 Job ID，并由 Bridge、Actor 和提交事务逐层复核当前 Job version/detail URL，再从详情页推导 scope，避免把列表 Endpoint 与详情样本 URL 混为一谈。整包浏览器自动加载门、Browser Broker/Profile 及交互型网站仍是后续实现。

候选 Recipe 的验证不是一个直接改状态的管理动作。`recipe.validate` 必须选择一个已 ready 的真实 Source，把 draft/quarantined 的候选版本、当前生产 Endpoint、Company/Source version 和一个“当前 Assignment version+1”的拟议 Assignment 冻结进独立 `ListingRun(mode=recipe_validation)`，并经统一 Work/Attempt/Permit/capability dispatch 交给同一个 Recruiting Executor。拟议 Assignment 只作为执行 fence，不写入当前 Assignment 或历史；验证结果只保存 page/trace Artifact 和客观质量证明，不写 Job、ListingObservation 或 Checkpoint。验证执行成功与候选质量通过是两个不同结论：即使 identity、ordering 或 pagination 为 false，Attempt/Work 仍可成功完成，从而保留可审计的反证。

`recipe.approve` 是其后的独立证据闸门。它必须锁定同一 validating Recipe，反查调用方指定的 validation Work/run 是否精确冻结该 content/contract/execution version，并要求恰好一个成功 Attempt。Listing 要求 page/trace 数量一致且 identity、ordering、pagination 当前样本证明成立；Detail 要求绑定一个冻结 Job version/URL、恰好一条规范结果、提取字段数与候选 Spec 一致，并只保存 response/trace。Discovery 则以 Company 官网作为真实样本，冻结 Company version/URL、候选 Recipe 和预期字段数，经同一个 Executor 的 Discovery Driver 执行；它没有 Source、Assignment 或生产 discovery generation，也不会调用会保存候选 Source、改变 Company onboarding 状态的生产结果事务。验证结果仅保存 response/trace、候选数量、字段数与规范内容 hash；零候选是有效反证，允许 Work 成功收口但不得通过审批，审批至少要求一条且至多 500 条有界候选。三类验证的 Artifact 都必须属于该 Attempt 且未被拒绝。任何串版、证据缺失、重复成功 Attempt 或质量失败都返回 `quality_rejected`，Recipe 保持 validating，Company/Source Assignment 与业务数据保持不变。单次样本仍不能证明历史岗位“更新后重新置顶”；`update-retop` 继续属于 Source 多次校准契约，不能由 Recipe 审批冒充已验证。

操作者可用 `recipe.reject` 明确放弃本次候选，但必须引用创建它的精确 validation Work。若该 Work 仍为 open/running/waiting，或仍有 offered/accepted/running Attempt，拒绝返回 `waiting_human`；操作者需先使用已有 Work cancel/resolve 流程关闭执行权。只有 Work 已终结且候选快照仍匹配时，拒绝才把 validating Recipe 返回 draft，并原子保存命令 receipt 与审计事件。该动作不删除 Work、Attempt 或 Artifact，后续修改候选必须创建新不可变 Recipe version，或在未改变不可变内容的前提下重新验证同一 draft version。

Attempt 固定引用 `recipe_id + recipe_version`，不能静默漂移版本。

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
work_id, parent_work_id?, target_type, target_id
purpose, trigger, initiator_actor_id
cause_message_id?, cause_work_id?
status, waiting_reason?, priority, expected_version
recipe_id?, recipe_version?, not_before?, deadline_at?
summary, created_at, updated_at
```

多步骤可以使用子 Work 或步骤进度；是否需要独立 Task 由纵向切片决定。

`recruiting.work.correct` 只修正尚未被领取的 `open` Work 的调度 placement：`priority`、`not_before`、可清除的 `deadline_at` 和可清除/替换的 `profile_id`。它必须以 Work version 做 CAS，并同时提升 `acceptance_version`、保存 receipt/event、按新 placement 追加一次持久 wake；已有活动 Attempt、running/paused/waiting/终态 Work 均拒绝原地修正。`business_key`、Target、purpose、capability、origin、Recipe 和业务输入绝不能从这个通用词修改：公司、Source URL、Recipe、导入项或岗位字段错误必须走各自领域命令，再按对应状态机恢复或创建新 Work。这样 Work Center 的“修正调度”不会成为绕过领域 fence 的任意改任务后门。

父 Work 是用户可见聚合，不替代子 Work 的状态和幂等边界。批量场景逐项保存 `succeeded|failed|skipped|waiting_human`，父级以结构化 outcome 表达部分成功，取消只阻止未开始项并 fence 仍在运行的结果。`completed` 还必须带 `resolution=succeeded|accepted_gap|skipped|terminated`、决定人、理由和证据；接受缺口不能计为 coverage 成功。

取消不能只改变 Work 展示状态。对于可执行 baseline，`recruiting.work.cancel` 必须在一个事务内取消 Work 和 BaselineGeneration、提升 acceptance fence、拒绝活动 Attempt、释放 BudgetPermit、写协作事件和容量释放 wake；在途 Executor 的迟到页只能保存为 rejected Artifact，不能建立 Checkpoint。取消后的 generation 是终态历史，不重新打开；Company 可在相同 `initializing` 阶段以新的 Work 和更高 generation 重新开始。

详情失败转人工后，用户拒绝接受缺口时可以用 `terminated` 关闭旧 Work，但这一步不得核算 baseline 成员。后续 `work.retry` 创建新的因果 Work，并在同一事务把仍为 `pending` 的成员账本从旧 Work 重绑到 Retry Work；新 Work 的真实详情成功才将成员计为 `succeeded`。已经 `accepted_gap` 的终态成员不被重绑，避免事后修复改写当时的基线结论。

失败按可审计分类决定下一步：瞬态错误有界重试，确定性 Recipe/数据错误转 repair，认证错误阻塞于 Profile，质量证明不足进入人工。终态 Work 不重新打开；后续恢复或用户再次运行创建带 `cause_work_id` 的新 Work。对于尚未终结的每日列表 `SourceOccurrence`，人工重试必须在同一事务创建新 `listing_sync` Work、把 occurrence 从旧 Work 重绑到新 Work、写入审计事件和执行 dispatch；旧 Work/Attempt 保留为不可变历史，新 Work 才拥有完成该 occurrence 的执行权。已终结 occurrence 不允许通过 retry 重开。日报保留关闭时结论，后续成功只追加恢复关联。

典型业务幂等键包括：每日列表 `source_id + schedule_date + policy_version`；详情 `source_id + source_job_key + refresh_generation + purpose`；基线 `source_id + baseline_generation`；共享修复 `failure_domain + failure_signature + failing_version`。它们与网络重放用的 `command_id` 是两层不同规则。

### 9.4 Attempt

Executor 对 Work 的一次具体执行。

```text
offered → accepted → running → succeeded / failed / expired / rejected
```

保存 `attempt_id`、`work_id`、Executor identity/incarnation、capability 快照、Recipe 版本、状态、接受版本、时间和结果/失败 Artifact 引用。`acceptance_version` 是逻辑 fencing 条件，但不要求一定使用 lease token。

Attempt 还固定 Company/Source 配置版本、Recipe Assignment、Checkpoint 版本、Job refresh generation 和 Profile 版本中与本次工作相关的接受条件。Work fencing 不能代替这些领域对象自己的 CAS。

控制面在成功 offer 后记录 authenticated concrete Executor Actor ID。首次可信 present 观测以 `observed_at - uptime - 1s` 推导保守的本次绑定下界；只回收严格早于该下界的 Attempt。`present→absent` 或成员从 catalog 消失时，以观测时刻回收旧 Attempt；`absent→present` 或 uptime 明显回退时，按新绑定下界回收前一 incarnation。在线信号不授予新执行权，也不接受任何结果；其职责只是提前关闭已经由数据库 fence 定义的旧执行权并唤醒持久 dispatch。

### 9.5 Artifact

Resource 中大对象或批量结果的稳定引用，包括页面、截图、响应、原始职位、失败证据、候选 Recipe、验证报告和 trace。至少保存 ID、内容哈希、类型、创建者、关联 Work/Attempt、访问和保留策略。

持久 Browser Profile 是安全 Resource，不是聊天凭证。至少保存 `profile_id`、站点/安全域、授权设备、版本、认证状态和最近验证时间；Cookie、密码和 OTP 不进入 Message、普通 Artifact 或 AI 上下文。控制面接受绑定当前 Profile 版本的 `auth_expired` 或 `captcha` 失败时，必须在保存失败证据、关闭 Attempt、释放预算、阻塞 Work 和创建/加入 RepairIncident 的同一事务内，把 Profile 从 `ready` 推进到 `repairing` 并增加版本；Secret 引用和设备绑定不变。此后新的 Work 不得用该 Profile 领取，已在途的旧 Attempt 可以保存失败证据并加入同一修复，但不得重复增加 Profile 版本，也不得再次熔断已修复或禁用的 Profile。Profile 故障按失败版本和稳定签名单飞，共享该 Profile 的 Work 只产生一个对应修复事项。

登录失效和验证码不能靠通用文字猜测或“页面没有岗位”推断。站点 Browser Recipe 的不可变 Browser Plan 可分别声明最多 10 个 `auth_expired_selectors` 和 `captcha_selectors`；selector 必须是合法、去空白且跨类别不重复的 CSS selector，并随 Plan 进入内容/兼容哈希。真实 Chrome 在初次导航及每个受限动作后检查这些信号，验证码优先于笼统登录标记；命中后仍先返回受限 DOM 给 Artifact 链，再以精确失败类别进入上述 Profile/修复事务。未声明信号的旧 Recipe 行为不变，修改信号必须作为 Recipe 版本/兼容性变化重新校验，不能由运行时临时注入。

Profile 与 Source 的关系必须是独立、可版本化的 `SourceProfileBinding`，Listing 与 Detail 分别绑定，不能把一次人工命令中的临时 `profile_id` 当作长期配置。绑定只保存 `source_id + recipe_kind + profile_id + effective_at + version`，不复制 Secret；当前投影与不可变历史和 Source 版本在一个事务提交。Listing Profile 改变了列表观测环境，必须把 Source 置为 `repairing`、清除旧四维校准并重新验证；Detail Profile 的可用性由该 Profile 自身的站点 canary 保证，不因此伪造 Listing 重校准。仍使用 `browser.recipe` 的 Assignment 不允许解绑；应先切换到不需要 Profile 的已验证 Recipe，再清除绑定。

每日截点把当时有效的 Listing Profile ID 冻结进 `SourceOccurrence`，随后创建的 Work 继承该 ID，Attempt 再冻结 Profile version。Profile Work 的 dispatch 只能投递到 Profile 的授权 `device_id`；Offer 事务必须再次验证 authenticated Executor 与该设备匹配，不能把定向 wake 当作权限。列表页产生详情 Work 时，按 Source 的独立 Detail Profile 赋值并在同一事务唤醒其设备，所以列表和详情可以安全地运行在不同设备。Source 重新校准也默认读取持久 Listing 绑定，不要求用户每天或每次修复重复输入 Profile。任何 Profile 非 `ready`、安全域与 Recipe scope 不同、设备身份非法或绑定/Source 投影不一致的情况都 fail closed，不进入当日日程或执行领取。

授权设备上的日常 Profile Provider 是同一 `recruiting-executor` 的本地适配器，不是插件 Worker，也不是远程浏览器服务。它使用 `0400/0600` 注册表把 `profile_id + profile_version + security_domain` 映射到 owner-only Chrome `user-data-dir/profile-directory`；注册表只保存本机路径和每 Profile 绑定令牌的 SHA-256，不保存 Cookie、密码、OTP 或明文绑定令牌，并在每次租用时重读，使修复后的版本轮换无需重启 Executor。修复插件必须从这个受管目录运行并以独立绑定令牌连接，Bridge 只向同 ID 连接投递任务；修复与验证 canary 成功分别以明确版本 fence 原子推进 registry，不能只合成一个无法解析的 SecretRef。Provider 要求 Profile 引用、Attempt 版本和 endpoint host 精确一致，同一 Profile 使用进程内与跨进程双重租约；插件修复浏览器和每日 Chrome Session 也不能并发打开同一目录，必须等待 Chrome 释放后才开放租约。Profile 页面离开设备前先删除表单控件、脚本、iframe、事件处理器、敏感属性和敏感 URL 参数，再进入既有 Artifact/Recipe 解析链；普通 `browser.public` 仍使用一次性临时目录且拒绝任何 Profile 引用。

### 9.6 领域状态机

状态机是确定的设计组成，不依赖 Staircase，也不会因为采用 Atoll 而消失。第一版至少定义：

```text
Company onboarding_status:
new → discovering_sources → initializing → ready
discovering_sources → blocked_no_sources → discovering_sources
initializing → blocked → initializing

Company control_status:
active ↔ paused → archived
active → archived
archived → paused

Recruitment Source readiness_status:
candidate → validating → ready
ready → repairing → validating
validating → invalid → validating
candidate / validating / invalid / repairing → rejected

Recruitment Source control_status:
active ↔ paused → archived
active → archived
archived → paused（恢复后先验证）

Recipe:
draft → validating → active → superseded / quarantined / disabled
validating → draft（验证失败并保留证据）

Incremental Checkpoint:
absent → establishing → committed
committed → candidate → committed（边界证明成立）
candidate → rejected（旧 committed 版本保持有效）

Job Posting:
discovered → detail_pending → available
available → update_pending → available

Browser Profile auth_status:
ready → repairing → verifying → ready / disabled

Work:
open → running → completed
running → waiting_retry / waiting_human / failed
waiting_retry / waiting_human → running
任意非终态 → paused / canceled

Attempt:
offered → accepted → running → succeeded / failed / expired / rejected

Daily Run:
planned → running → completed / completed_with_exceptions
```

Company 只有 `onboarding_status=ready` 且 `control_status=active` 才进入正常运行；Source 只有 `readiness_status=ready` 且 `control_status=active` 才属于每日应运行集合。正交状态避免把“用户暂停”和“页面坏了”混成同一个枚举。

Source Discovery 可产生 0、1 或多个候选，候选逐项验证和拒绝，不做整体事务。其幂等代际为 `company_id + discovery_generation`；强制重新发现显式递增 generation。Source `ready` 至少要求归属已确认、生产 Endpoint 已发布、Listing Recipe Assignment 有效，并以真实样本验证分页、稳定岗位键、活动倒序、边界和异常停止。

`source.validate` 不是单纯把状态改为 `validating`：命令必须在同一事务冻结 Candidate Endpoint、active Listing Recipe、Company/Source/Assignment 版本和可选 Browser Profile，创建 `purpose=source_validation` 的 Work、Listing validation run、receipt、事件和 capability dispatch。它复用统一 Executor 与 Listing Recipe 执行面，不增加专用 Worker；成功执行只保存页面/trace Artifact 和客观质量观测，不写 Job、不派生详情、不创建或推进 Checkpoint。若稳定身份、活动倒序或分页质量证明不成立，结果事务完成证据 Work 的同时把 Source 推进到可重新校验的 `invalid`，而不是卡在 `validating`；这表示“校验执行成功、候选契约失败”，不应伪装成执行故障。校验过程中修正 Endpoint 会把 Source 推进到 `repairing` 并增加 revision/version，旧 validation Work 不再可领取、已在途结果被 Attempt fence 拒绝；随后必须创建引用新版本的新 validation Work。用户也可通过 `source.validation.reject` 明确放弃 candidate；若有旧 active Endpoint 则恢复旧 Source ready，否则进入 rejected，所有既有证据仍保留。人工发布命令再引用已成功完成的 Work 证据，对 identity、pagination、ordering、update-retop 分别作判断；发布闸门必须反查 validation run、Attempt 质量结果，并确认 Endpoint revision、Recipe/contract 和拟发布 Assignment version 完全一致。单次执行不能观察到历史岗位更新时，`update-retop` 必须保持 `unverified`。

Company 有至少一个 active/ready Source，且每个准备投产的 Source 已完成 listing baseline、详情均成功或有用户明确接受的缺口时，才进入 onboarding `ready`。零候选进入 `blocked_no_sources`；部分 Source 成功不阻止其余候选独立失败或等待人工，但用户必须看见未投产项。

首次 baseline 的分页进度与 staging 都必须绑定 Attempt。Baseline 每页响应必须先保存 Artifact、完成确定性解析并把该页结果提交控制面隔离 staging，确认后才允许请求下一页；不能在 Executor 内攒完整列表后一次提交。页面确认后立即释放完整岗位字段，只保留跨页去重所需的来源身份+内容哈希、Checkpoint 前沿、质量计数和轻量 Artifact/页元数据，因此内存不随岗位正文总量线性复制。日常增量在尚无通用 Attempt staging 时，必须先完成整次质量证明再发布页面，不能为了流式而提前写 Job；后续若解除内存上限，必须先补通用 staging/finalize，而不是牺牲质量失败的零业务副作用。Recipe 可以使用响应中的同源 next URL，也可以声明同源 offset pagination：有 total/limit/offset 元数据时逐项校验响应，只有短页终止语义时必须把 offset/limit query 名和 page size 固定进 Recipe hash。JSON 根数组必须显式声明，不能把“未配置 collection”误当成根集合。Executor 在页间退出后，新 Attempt 从第 1 页重扫；旧 Attempt 的 Artifact/页/staging 仍是诊断证据，但 finalize 只冻结成功 Attempt 的 `listing_attempt_id`，只按该 Attempt 核对条目数并建立 Checkpoint，后续 Job/Detail Work 物化也只读取该 Attempt 的行。禁止仅按 generation 汇总多个 Attempt 的 staging，因为这会把不同时间的列表快照拼成一个伪基线。

状态转换规则写在纯 Recruiting domain model 中，输入为当前状态和领域命令，输出为新状态及领域事件。Recruiting Actor 是唯一有权接受转换结果的行为边界：

```text
Atoll Message（身份、cause、command_id、expected_version）
  → Recruiting Actor
  → 纯状态转换函数
  → Actor 控制的 Resource 保存新事实
  → Atoll ledger 记录协作结果与因果
```

Atoll timer 只负责可靠地产生到期命令，不直接修改状态；Executor 只提交 Attempt 结果，不直接把 Work 或 Job 改成成功；Human 和 Agent 也使用相同命令和版本校验。

如果不基于 Atoll，仍然应该采用相同领域状态机，但还需要另行建设身份认证、授权、命令总线、定时器、持久事件记录、Actor 生命周期和人工协作入口。Atoll 替代的是这些通用运行与协作基础设施，不替代招聘业务状态机。

### 9.7 不冻结物理表

系统必须可恢复地保存 Company、Recruitment Source、Job Posting、Observation/Override、Recipe Assignment、Incremental Checkpoint、SourceOccurrence、Work、Attempt、Artifact、命令幂等记录、Profile 和站点预算事实。这不等于每个概念必须对应一张表，也不提前确定 `human_reviews`、`work_bundles`、`worker_slots`、`origin_budgets`、`domain_outbox` 等物理结构。表、索引、事务和存储产品根据 Resource API 与访问实测决定。

## 10. 大规模调度

### 10.1 负载假设

```text
约 10,000 家公司
平均约 2 个招聘入口
约 20,000 个入口每日检查
接入阶段一至两次全量
日常详情量取决于当天增量，不等于历史职位总量
```

每日逻辑工作量可表达为：

```text
每日必须覆盖的列表同步数
  = 当日截点 active Recruitment Source 数

每日详情同步数
  = 新岗位数
  + 更新后重新进入旧边界之前的历史岗位数
  + 安全重叠窗口中按 Recipe 要求复核的岗位数
  + 合法重试与修复重跑数

每日总 Work
  = 列表同步
  + 详情同步
  + 修复、重试、人工和数据维护
```

20,000 个列表同步是当前规模下每天的确定性基线；详情和异常 Work 是随站点变化的放大量，必须用真实数据测量。这些数字用于容量实验，不是已知吞吐事实。

用于首轮容量实验的示例而非 SLA：20,000 次列表同步摊在 8 小时约为 0.7 次/秒，按 5 倍峰值约 3.5 次/秒。若 80% HTTP 平均 5 秒、20% Browser 平均 45 秒，理论有效并发约 13，初始可用 20–40 个 Executor 实例做阶梯压测。真正的放大风险是无时间字段 Source 的重叠复核：每个 Source 复核 20 个岗位即可产生约 400,000 个详情 Work；因此必须实测重叠详情比例和控制消息放大，不能只按 20,000 个列表任务估容。

### 10.2 到期工作

保存 Source 的刷新策略、下次到期时间、最近成功 Incremental Checkpoint 和幂等周期键。每日运行不依赖进程内 cron 和记忆。第一阶段由 Recruiting Actor 使用 Atoll durable one-shot timer 保存下一截点；招聘 extension 只保存权威 timer ID 和已冻结的本次日程 payload。重启或重复 fire 由 timer ID 栅栏、schedule date 唯一键和日切事务共同收敛，不复制 Atoll scheduler。

每个每日 `SourceOccurrence` 是持久事实，使用稳定业务键 `source_id + schedule_date + schedule_policy_version`，并保存 Daily Run、截点时 Company/Source 版本、Endpoint、Assignment、Recipe/内容哈希、ABI/transport/capability/origin、调度策略版本、确定性 `due_at`、当前状态、关联 Work 和最终结果。仅保存当前聚合版本号不足以支持延迟执行，因为 Source 在窗口内改变后可能已无法重建旧输入；因此 occurrence 必须自带最小且无秘密的执行快照。Daily Run 与截点时全部 occurrence 必须在一个 Repeatable Read 事务内原子生成；事务失败时两者都不存在，成功时 `expected_sources` 必须精确等于 occurrence 数，不能接受调用方提供的部分集合来冒充每日名单。昂贵 Work 按 `due_at` 在窗口内渐进物化；窗口末只需核对已冻结 occurrence 的执行结果，不在历史截点后补猜名单。

临时手工运行不得复用一个含糊的“手动执行”语义：`diagnostic` 只保存证据，不写 Job、不派生详情、不推进 Checkpoint；`join_occurrence` 收敛到当日 SourceOccurrence；`production` 是独立运行，可以推进 Checkpoint，但必须通过 Checkpoint CAS 与并发定时运行竞争。`join_occurrence` 只接受 running DailyRun 中尚为 planned 的 occurrence 及其 expected version，复用截点冻结的 Source/Endpoint/Recipe/contract 输入，并在窗口结束前原子创建立即可运行的 listing Work、推进 occurrence、保存命令 receipt/event 和 capability dispatch；若 timer 已先物化、日报已关闭或版本已变化则明确拒绝，不能另造重复 Work。通用 `recruiting.work.create` 只创建 discovery、baseline、repair、reconcile 和 data-maintenance 等非采集控制 Work；它必须拒绝直接创建 `listing_sync`/`detail_sync`，因为缺少运行模式、occurrence 或独立执行快照的 Work 即使收到 dispatch 也不可执行。列表/详情手工抓取只能由上述专用 run 命令原子建立完整执行上下文。

独立人工运行使用招聘扩展自己的 `ListingRun`，不伪造成 DailyRun 或 SourceOccurrence。它与 Work 一对一，冻结 Source/Company 版本、Endpoint、Assignment、Recipe 内容/契约、执行能力、origin 以及发起时读取的完整 Checkpoint。`diagnostic` 的 Executor Offer 使用该快照运行相同 Recipe，但只提交有界 page/trace Artifact 和质量摘要；接受事务只完成 Attempt、Work、ListingRun、Permit、receipt 和事件，明确不写 ListingObservation、Job、Detail Work 或 Checkpoint。失败仍进入统一的分类重试/人工处理状态机；终态非成功 Work 被人工重试时，ListingRun 必须与新因果 Work 原子重绑。`production` 只允许用于具备已验证增量契约且已有基线 Checkpoint 的 Source；它复用同一上下文和 page ingestion，成功时写入 Observation/Job/Detail Work，并以冻结版本对当前 Checkpoint 做最终 CAS。并发日常运行先推进水位时，production completion 被 fence，不能完成 Work/ListingRun 或覆盖新水位。

优先级的业务顺序默认是：

1. 接近运行窗口截止时间的每日 listing sync；
2. 已发现的新岗位和变化岗位 detail sync；
3. 阻塞正常增量链路的列表/详情修复；
4. 有明确 deadline 的人工工作；
5. 初始化全量、校准和普通重试。

站点预算和人工紧急提升可以改变实际顺序，但不能令普通 active Source 长期饥饿。

| 候选方案 | 优点 | 风险 |
|---|---|---|
| 每 Target durable timer | 语义直接，无集中扫描 | 要验证大量 timer 的存储、恢复和唤醒成本 |
| 少量 timer + 到期索引 | timer 少，易做窗口规划 | 扫描器可能成为热点 |

M1 选择最简单可工作的方案，M4 用 20,000 个 Recruitment Source 的测试决定生产方案。

### 10.3 执行分发

```text
到期 Work
  → Work 事务按 capability 写入持久 execution-dispatch intent
  → Recruiting Actor 有界投递一次 wake 给匹配的 Executor 实例
  → Executor 单次领取时校验状态、预算和 capability，并创建 Attempt
  → Executor 获取 Recipe 与输入 Resource
  → 执行并上传 Artifact
  → Recruiting Actor 按 attempt_id + acceptance_version 接受或拒绝，并写入释放容量的下一次 wake
  → Executor 处理一份 Work 或确认当前无 Work 后回传 completion acknowledgement
```

第一版选择“持久意图 + 单次 push wake + Executor 内部单次 pull offer”的混合方式。它不为每个任务建立轮询器，也不让一次 wake 隐式 drain 整个队列：日程物化时最多按该 capability 的可用 Executor 数创建初始 wake；每次 Attempt 成功或失败时，在同一业务事务写入一个容量释放 wake，可重试 Work 另写一个到 `retry_not_before` 才到期的 wake。Recruiting Actor 通过已有 reconcile durable timer 有界投递；Executor 只有在完成一份 Work 或明确 idle 后才回传 acknowledgement。dispatch ID 在整个恢复生命周期稳定，每次 delivery attempt 使用不同消息 ID；offer command 在同一 delivery 内稳定、跨 redelivery 必须变化，使丢 completion acknowledgement 后的新投递重新判断当前队列，而不会按旧 receipt 取回已经完成的 Offer。结果命令继续按 Attempt 稳定幂等，未确认投递按有界次数和退避恢复。这样大量 Work 保留在有索引的领域队列中，控制消息量与实际并发和完成速率相关，而不与积压总量同时爆发。

Executor fleet 是招聘扩展配置中的 `actor_target + capability` placement，不是新 Worker 类型。`actor_target` 可使用 Atoll 稳定两段地址，由 Atoll 在投递时解析当前三段成员；Executor completion 必须由 envelope 中匹配该目标的 authenticated 三段 sender 提交。这样控制面与 Executor 声明不需要预先知道对方本次 seating ID，也不会以放宽身份校验解决部署循环。初始唤醒按 capability 过滤并确定性分散到实例；实际领取仍在数据库事务中执行全局 priority、`not_before`、origin/Profile 条件和 BudgetPermit 检查，所以唤醒只代表“可以尝试领取”，不代表绕过领域状态机或预先授予执行权。没有匹配 Executor 时 Work 保持持久可运维状态，不丢弃、不降级到错误 capability。

Recruiting Actor 只做短时、确定性的校验与领域事务，不在 Actor handler 内等待网站网络请求。一个逻辑 Actor 可由多个无状态 handler 实例和按 Source 键并发的 Resource 事务实现；若容量实验证明单一接受路径饱和，可增加内部路由或数据分片，但保持一个公开领域权威。`recruiting-executor` 是一个 Actor class，可以部署许多实例并按 HTTP、Browser、Profile 安全域 placement。

### 10.4 Batch 只是优化

多个 Work 可形成 Execution Batch 以减少往返，但 Batch 不改变单个 Work 的状态、取消、优先级和幂等边界；不要求用户可见、永久表、固定大小或独立 Channel；只有基准测试证明收益后才实现。

这里的 Execution Batch 与用户发起的“公司批量导入”不是同一个概念：后者是必须可审阅、确认、取消和逐项追踪的业务聚合，因此可以有持久 `CompanyImport`/父 Work；前者只是把多个既有执行单元合包传输的性能优化，仍不预设实体或 Worker 类型。

用户级命令、人工决策、分类失败、Work/Attempt 终态和 DailyRun/修复/批次等聚合摘要逐条进入 ledger。高频 execution offer、accept、started 和 listing page 不逐条生成领域 event：它们分别由 MySQL 中的不可变 Attempt、command receipt、page progress、Observation 与 Artifact 构成可查询的执行审计层，终态 completion/failure 再生成协作事件并携带这些事实的稳定引用。这个分层不是省略身份或恢复能力；每个高频动作仍绑定 authenticated Executor、incarnation、Attempt、command ID、请求哈希和领域 fence。它避免每日 Source 数乘分页数、再乘执行状态转换数直接放大 Channel ledger；若以后需要跨系统逐页流分析，应从这些事实构建有界投影，而不是把原始页面正文或每次内部状态转换补写进 ledger。

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

执行前必须原子获得覆盖 `origin + Profile + Browser capacity + company fairness` 的预算许可，许可不是新 Worker 类型。首次全量、校准和历史回填使用独立可配置容量上限，不能挤占每日列表覆盖。

### 10.6 分级执行与扩容

默认按成本从低到高：列表已有内容、公开 HTTP/API、页面实际 API、变化职位详情、Browser、AI。它们是 capability 和决策顺序，不是不同 Worker 类型。

扩容观察最老可运行 Work、deadline、能力利用率、执行耗时/错误和可运行积压。处于 cooldown、等待人工或权限不匹配的 Work 不能靠扩容解决。

```text
所需并发 ≈ 可运行工作量 × 平均执行秒数 ÷ 时间窗口秒数 ÷ 目标利用率
```

是否拆 HTTP/Browser template、placement、进程池或 Actor class，只能由安全域、资源隔离和压测决定。

### 10.7 重试与熔断

退避参数按站点和错误配置，不冻结统一时间序列。第一版以规范 `origin + failure_class` 精确选择版本化策略；不做域名后缀或通配符匹配，避免一个宽泛规则意外覆盖不同安全域。每条覆盖完整声明最大自动次数、基础/最大延迟和 throttled 最短延迟，未命中才回落全局默认。失败事务把实际采用的 policy version、Attempt 次数和 `retry_not_before` 冻结进 Work，所以配置后续改变不会重写已经作出的决定。

| 失败 | 默认决策 |
|---|---|
| 超时、偶发 5xx | 有界退避 |
| 429 | 降速并延迟 |
| 403、验证码、登录失效 | 停止对应站点/Profile 并请求处理 |
| 页面结构变化 | Recipe 修复和真实样本验证 |
| 空结果、字段异常 | 质量检查，不能直接当成功 |
| 确定性数据错误 | 进入修复，不盲目重试 |
| 多轮不收敛 | `waiting_human` |

失败按 `origin | recipe_version | profile | single_target` 归入故障域。相同 `failure_signature` 只允许一个活动 Repair Work，受影响 Work 通过 `blocked_by_repair_work_id` 关联；修复成功后由现有 Recruiting reconcile 每次自动开放一个、至多 100 个 Work 的批次，按最久未处理的 Incident 轮转。是否仍需恢复不是运行时遍历历史 Incident 推测，而是 resolve/恢复事务维护的显式 `recovery_pending` 队列事实和专用索引；每批提交后，大 Incident 排到队尾，最终批次原子清除队列标志。每个批次仍有确定 command receipt、版本围栏、一个聚合事件和每种 capability 至多一个初始 wake，真正领取继续受全局、站点、公司和 Profile 预算约束；人工 `repair.recover` 保留为可审计的运维兜底，并与自动协调竞争同一版本，不能形成双重恢复。`auth_expired`/`captcha` 进入 Profile 故障域时还必须原子熔断其接受版本，派发查询和领取时都只允许 `ready` Profile；`budget_revoked` 即使按 Profile 聚合也只是预算/策略故障，不能把认证状态误改为失效。重试策略版本、Attempt 计数和预算耗尽原因随 Work 保存；人工重试不能绕过站点或 Profile 安全预算。

## 11. 一致性与恢复

### 11.1 必须保持

- 命令 ID 在作用域内唯一；
- 同一周期对同一 Target 创建 Work 幂等；
- `command_id` 重放返回第一次接受的稳定响应；公司、Source、基线、详情和修复还分别执行自己的业务唯一键或 generation 约束；
- Incremental Checkpoint 只能在旧边界已到达、重叠已完成且活动倒序契约成立时推进；
- Incremental Checkpoint 提交必须比较 `expected_checkpoint_version`；手工和定时运行都不能覆盖并发先提交的新版本；
- 推进 Checkpoint 与持久化本次发现的岗位及其详情 Work 之间不能留下不可恢复的间隙；
- Recipe 版本或岗位身份提取规则改变时，旧 Checkpoint 必须经过兼容验证或重新建立；
- Attempt 只在接受条件仍成立时推进 Work；
- Job 详情结果还必须比较 `refresh_generation`，Source/Recipe/Profile 结果比较对应配置或 Assignment 版本；
- 职位使用来源 ID 或规范化 URL 作为业务键；
- 保存结果与推进 Work 不留下不可恢复的半完成状态；
- ledger 与 Resource 的非原子间隙有明确恢复协议；
- 恢复只依赖持久事实。

### 11.2 结果接受

```text
验证 identity / incarnation / attempt_id / acceptance_version
  → 验证 Target 配置、Recipe Assignment、Checkpoint、Job generation、Profile 等相关版本
  → 验证 Artifact 哈希与权限
  → 幂等写入职位和质量事实
  → 生成必要后继 Work
  → 推进 Attempt 与 Work
  → 产生可恢复的领域事件意图
```

若 ledger append 与领域事务不能原子提交，必须实现可重放交付或等效机制；Transactional Outbox 是候选，不是唯一答案。

### 11.3 对账

Recruiting Actor 的恢复 handler 检查长期无进展 Work/Attempt、已上传但未接受的 Artifact、状态投影差异、失效 incarnation、废弃 Recipe 引用和长期未进入 ledger 的事件意图。只有出现独立权限、生命周期或故障边界后才拆 Reconciler Actor。

失效 incarnation 对账复用同一个 durable reconcile timer。Actor 启动时从活动 Attempt 有界恢复需要观测的 concrete Executor ID，之后只跟踪实际成功领取过 Work 的成员；每 tick 调用 Atoll 公共成员查询，按排序游标公平轮转，数据库 sweep 数和 Attempt 回收数都受 `attempt_recovery_limit` 约束。回收、Permit 释放、running Work 转 `waiting_retry`、`attempt.expired` 事件和对应 dispatch 提前到期在短事务内完成。Actor 状态丢失或成员查询失败不会误判执行权，只退化到原有 stale timeout。

首次全量的大批量结果先按 generation 分块写 staging，最终用一次 fencing finalize 使其可见；Detail Work 使用可重放意图逐步补齐。批量导入、纠正和发布采用父 Work 加逐项结果，默认不要求跨所有 Target 的大事务。

### 11.4 Atoll 暂时不可用

- 不创建新 Work，不作新分发或人工控制决定；
- 已开始的外部请求可以安全结束；
- Executor 可将结果和 Artifact 暂存在受控持久位置；
- 恢复后按 Attempt、版本和幂等键提交、接受或拒绝；
- 不允许旁路控制面继续推进领域状态。

## 12. 消息、权限和界面

### 12.1 消息与数据

`work.created/waiting_human/resumed/completed`、`company.blocked`、`source.blocked`、`origin.circuit_opened`、`recipe.review_requested/published`、`capacity.degraded` 等协作事件进入 Channel。

执行消息是否逐条进入 Channel，要根据控制价值和 ledger 容量实测决定，但不能绕过 Atoll 身份、授权和可恢复因果。允许 Message 引用 Resource 中的紧凑批量 envelope。

页面正文、截图、响应、完整职位、细粒度进度和高频指标只进入 Resource 或指标系统。

### 12.2 权限

- 权限由 Atoll membership/capability 执行，不依赖 prompt；
- Agent 只能调用注册能力；
- Executor 只能执行符合其 capability、设备和安全域约束的 Work；
- 凭证和 Cookie 不进入 Channel；
- Profile 绑定授权设备或安全域；
- Profile 修复通过有领取权、设备绑定、超时和审计的一次性安全会话完成；无权限用户只能看到“认证阻塞”，不能看到秘密或敏感截图；
- 发送给 AI 的 Artifact 必须过滤、脱敏和限量；
- 人工命令使用 `expected_version`，旧操作不能覆盖新状态；
- 批量影响 Target 或共享 Recipe 的动作可配置二次批准。

Profile 修复不是新的 Worker 类型，也不是把 Cookie、密码或 OTP 发给 Agent。它复用统一的 `Work → Attempt → Artifact → RepairIncident` 权威链，并由 capability=`browser.profile.repair` 路由到 Profile 已绑定的可信设备 Actor。普通运营员只能创建一个有期限、可审计的修复会话；只有与 `device_id` 精确匹配的已认证 Tool Actor 才能领取对应 Work。会话和公开投影只保存身份、版本、状态和截止时间，凭证始终留在设备侧；控制面只接受新的不可解析 `secret://` 引用和已脱敏验证 Artifact。

修复采用两阶段恢复：设备提交新引用后，Profile 从 `repairing` 进入 `verifying`，原交互 Work 成功结束，并创建独立 `profile_verify` Work；同一授权设备必须在 Profile 的确切 `security_domain` 上提交 authenticated canary。Canary 成功只把会话标记为 `verified`，Profile 仍保持 `verifying`，所以每日任务不能提前恢复。运营员再以该验证 Work 启动 RepairIncident validation，并在 `repair.resolve` 事务中同时完成唯一 Repair Work、解决 Incident、将 Profile 推进到 `ready`；随后才按既有有界 recover 协议释放受影响 Work。失败的交互/验证仍归属于原 RepairIncident，不递归创建新的 Profile 故障；验证失败会把 Profile 退回 `repairing`。过期会话由现有 Recruiting reconcile 定时器分批取消 Work、失效 Attempt 并释放唯一活动会话键，不增加 Reconciler 或 Worker 类型。

Authenticated canary 不是“同域页面能打开”、Cookie 数量、DOM 中没有密码框，也不是与上次页面快照比较。每个 Profile 必须冻结一个站点专用的版本化 Browser Recipe：精确 HTTPS endpoint、Recipe ID/version、正文 hash、兼容 contract hash、kind 和最小完整记录数都进入 Profile/Attempt fence。Executor 在接受 Work 前通过 Atoll Resource 读取并校验该 Recipe，授权扩展只在精确 endpoint 上执行声明式字段提取；边界外只返回 Recipe 身份、完整记录数和通过结论，任何字段值、Cookie、密码、OTP 或 storage 都不进入 Bridge、Message、Artifact 或 AI。验证页面或 Recipe 改版必须先产生新 Profile 版本，旧 Attempt 不能借新页面通过。

### 12.3 Work Center

用户可按 trigger、purpose、状态、等待原因、Target、发起者和时间筛选 Work，并查看输入、Recipe、Attempt、Artifact 和因果链；执行创建、领取人工项、暂停、恢复、取消、修正、重试、跳过、批准、拒绝或终止。界面必须分别显示列表覆盖、详情待处理、当前仍可用详情、失败原因和下一动作，不能把详情失败显示成列表漏采。

`recruiting.work.list` 明确区分两个用途：默认或 `view=operational` 是面向人的 Work Center，按 `updated_at, work_id` 倒序 seek 分页，支持 `status`、`purpose`、`trigger`、`waiting_reason`、完整 Target、`initiator_actor_id` 和更新时间半开区间；`waiting_reason` 必须与 `status=waiting_human` 同时使用。响应返回 Work、以 `work_id` 为键的 placement 以及 opaque page cursor，cursor 与全部筛选条件绑定，改变条件后不得复用。`view=runnable` 是 Executor/诊断使用的有界候选查询，要求 `due_at` 和 capability，可选 origin/Profile；历史调用只要携带 `due_at` 仍按 runnable 语义执行。两种视图不得混合字段，Work Center 查询不领取、不修改 Work。

批量导入、基线、纠正和发布还显示总数、成功、失败、跳过、等待人工、取消及可重试项；手工运行显示 `run_mode`、关联 SourceOccurrence、读写前后 Checkpoint、边界证明、数据变化、派生详情和预算消耗；人工结案显示 resolution、决定人、理由和证据。

Review Queue 是 `status=waiting_human` 的 Work Center 视图，可再按结构化等待原因收窄；Capacity 是 Executor 和预算的投影，不要求独立 Actor。

## 13. 可观测性

业务指标包括 Target 健康、职位变化、Recipe 覆盖与修复率；调度指标包括可运行量、等待原因、最老年龄、deadline、Attempt 结果、站点预算和能力利用率；可靠性指标包括跨 ledger/Resource 未完成意图、状态差异、陈旧结果拒绝和恢复时间。

`recruiting.system.status` 从一个只读一致性快照返回 Work/Attempt/DailyRun/Repair 状态计数、Repair 自动恢复队列长度、当前 runnable 与最老等待、deadline miss、领域事件和 execution dispatch 的 pending/due/exhausted；同一快照还从最近一小时按索引最多读取 1,000 个 Attempt 和 1,000 个 rejected Artifact，返回 Attempt 结果、端到端 P50/P95/P99、过期后同 Work 成功恢复数与恢复时延。每类样本分别返回 scan limit 和 truncated，截断值只能解释为近期样本下界，不能冒充全量计数或 SLO。`recruiting.capacity.status` 返回全局、capability、origin、company、Profile 的活动 BudgetPermit 用量，以及按 capability/origin/Profile 有界聚合的 runnable 数和最老等待，同时展示配置的预算上限与 Executor fleet 数。容量分组不能用“限制返回行数”的无界 `GROUP BY` 扫描全部积压；首版分别从 `open`/`waiting_retry` 的 runnable 索引最老端最多读取 5,000 条，并显式返回 scan limit、实际扫描数和 `runnable_counts_exact`，截断结果只是容量压力下界。二者是现有事实的投影，不领取 Work、不创建容量 Actor，也不能把配置实例数冒充在线心跳。长期时序、Executor 利用率和外部依赖指标仍应进入独立 metrics Resource/监控系统，不能反向把高频样本写入 Channel。

必须贯穿 `command_id`、`correlation_id`、`target_id`、`work_id`、`attempt_id`、Recipe 版本和 `artifact_id`。只有真正引入 Batch 等实体后才增加对应 ID。

## 14. 实施阶段

### M0：最小契约

- 冻结 Company、Recruitment Source、Job Posting 三类业务实体，以及 Recipe/Assignment、Incremental Checkpoint、SourceOccurrence、Work、Attempt、Artifact、Profile 安全 Resource 等运行事实；
- 冻结公司接入、首次全量、活动边界增量、详情同步、修复、人工接管和数据维护的业务语义；
- 冻结 Listing Recipe 的活动倒序、边界锚点、重叠窗口和 Checkpoint 提交契约；
- 冻结幂等、版本校验和陈旧结果拒绝不变量；
- 冻结暂停/恢复/归档、三种手工运行、批量逐项结果和结构化 resolution 契约；
- 建立 Actor、Message、Resource、Driver 边界；
- 不冻结 Batch、Lease、Outbox、物理表或 Channel 分片。

### M1：纵向切片

```text
用户添加公司
→ 创建 Company 和 source_discovery Work
→ 找到并验证 Recruitment Source
→ 创建 baseline Work，完整遍历列表
→ 为发现的 Job Posting 获取详情
→ Executor 上传真实 Artifact 和结构化数据
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

完成 Source Discovery、列表 Recipe 与详情 Recipe 的生成/验证/发布/回滚、Failure Artifact、自动修复，以及人从 `waiting_human` 恢复或完结 Work。

同时完成按 origin/Recipe/Profile 的共享故障聚合、Recipe Assignment 灰度、Profile 安全修复会话和积压有界恢复，避免一站故障产生大量重复修复项。

### M4：10K 容量决策

- 对比 20,000 个 Recruitment Source 的 timer 方案；
- 用真实增量比例建模；
- 压测单 Channel 和 Resource data plane；
- 测试 capability 匹配、公平性、热点和重试风暴；
- 只有证据表明需要时引入 Batch、存储分片、Channel 拆分、独立调度 Actor 或不同 Executor placement/template；
- 形成可复现容量基线和 SLA 建议。

### M5：生产准备

完成备份恢复、权限凭证审计、故障演练、数据保留删除与合规策略，并根据容量基线确定部署规模。

## 15. 验收

### 15.1 功能与可靠性

- 用户可新增、更新、暂停、恢复和归档 Company；物理删除只按 M5 合规流程验收；
- 用户可发现、添加、拒绝、验证、更新、暂停、恢复和归档 Recruitment Source；
- 用户可管理 Work，并分别追踪列表同步、详情同步和修复；
- manual、timer、event 使用同一状态和审计路径；
- 全量初始化完整遍历当时所有有效列表页，并获取所有可访问岗位详情，产生可对账的基线；
- 一至两次全量后，每个日运行窗口内为截点时所有 active Source 产生唯一 occurrence，并成功到达旧活动边界或记录明确等待/失败原因；
- 基线可以在大分页中断后从持久 generation 恢复，列表完成、详情待处理和最终完成分别可见；
- 第二次校准不会在缺少真实更新样本时把“更新后重新置顶”错误标成已验证；
- Listing Recipe 能验证岗位按最新活动时间倒序，新岗位和历史更新岗位都会进入旧边界之前；
- 日常只对边界前的新增、更新、重叠复核和合法重试岗位同步详情；
- Checkpoint 只有在找到旧边界、完成重叠且排序契约成立后才推进；失败重跑不会漏过岗位；
- 产品不会因为岗位未出现在增量范围内而推断其下架或删除已有岗位；
- 列表 Source 和岗位详情 URL/Recipe 可以分别自动修复或进入人工处理；
- 浏览器插件能够在真实页面捕获/辅助生成 Listing Recipe 和 Detail Recipe，并保存版本与验证证据；
- 已存在 active Recipe 时，日常执行直接运行其代码，不先调用 Agent/LLM 分析；
- Recipe 能执行并产生结构化职位；
- 失败生成 Artifact，人工能在原上下文处理；
- 可从命令追踪 Work、Attempt、Recipe、Artifact 和数据；
- 旧 `expected_version` 明确拒绝；
- 重复命令、交付和结果不产生重复业务结果；
- Executor 退出后 Work 可恢复；
- Executor 的可信 presence 消失或 incarnation 更换能加速恢复，成员查询失败时仍由无进展超时兜底；
- 旧 Attempt 不能覆盖新事实；
- Checkpoint CAS、Job refresh generation、Source/Recipe/Profile 版本分别拒绝跨 Work 的陈旧结果；
- shared Recipe 或 Profile 故障只产生一个修复事项，恢复时不会形成重试风暴；
- 在 ledger/Resource 每个故障点注入退出后可对账恢复；
- Atoll 不可用时不产生新调度，已开始效果安全结束并暂存，恢复后幂等处理；
- Actor 重启不依赖私有内存。

### 15.2 容量基准场景

以下不是预先承诺的 SLA：

- 保存约 10,000 家公司和 20,000 个日常入口；
- 构造活动边界增量、首次全量、修复、人工等待和 cooldown 混合负载；
- 验证历史岗位更新后重新置顶、相同活动时间、置顶项、分页抖动、边界缺失和重叠窗口；
- 测量 timer 创建、唤醒和恢复；
- 测量单 Channel、Resource data plane 和 ledger 增长；
- 逐步增加 Work 和并发，记录瓶颈，不预设每秒提交数或 Slot 数；
- 验证公平、站点预算、无事实丢失、无陈旧覆盖和无重复结果；
- 根据真实 Recipe 耗时分布计算每日窗口所需容量。

报告必须记录硬件、数据库、Atoll 版本、数据分布、Recipe 类型、预算、并发、P50/P95/P99、错误率和恢复时间，才能作为拆架构的依据。

截至 2026-09-12，容量验证已经分成互不冒充的四层证据：L0—L4 证明 MySQL 中 20K Listing/400K Detail Work 的计划与有界物化；P1 证明普通用户、Server、daemon、四个同 class Executor、Resource 和 Channel ledger 的控制/数据面能完成 1,000 Company；A1 证明同一 Executor class 可并发读取 200 个冻结 response Resource、执行既有 Recipe 并实际写出 200 个 derived Artifact；H1/H2 则在无出站路由的受控 origin 上保持生产 HTTP Driver 的地址安全、robots、terms 和 GET-only 约束，真实抓取并持久化到 1,000 个 16 KiB response Artifact。A1/H1 的单页初始 Work 唤醒按 Executor 数压缩为 4 条，H2 的两个 500 项物化页各向 8 个 Executor 唤醒一次，验证“大量 Work 不等于大量逐 Work 初始消息”的设计。容量等待器去除人工 reconcile 洪泛并持续消费不参与判定的实时 feed 后，未批量 H2 正确性通过但 8 Executor 的 5.88/s 与 H1 四 Executor 的 5.99/s 持平；Recruiting 数据库的观察时间确认平台来自逐岗位串行控制往返与下一任务补给。按照这一证据，优化落在招聘扩展而非 Atoll core：同一个 `recruiting-executor` 以最多 32 项的 supply batch 完成 offer/claim/result envelope，claim 原子执行 accept+start，capacity-release 按 supply batch 合并；每个 Attempt 的状态机、fence、Permit、command receipt、结果和 Artifact 仍独立持久化，Listing、多结果流程、Browser 及未声明安全的 capability 继续走单项协议。复测 H2 为 1,000/1,000 成功、8 Executor 全部使用、16 MiB response 逐对象校验通过，业务完成 74.583 秒，即 13.41/s；相对 5.88/s 提升 128.1%，ledger message 从 14,233 降至 1,638。该结果距 400K/8h 的 13.89/s 算术参考仍差 3.5%，且 batch 下阶段延迟包含整批等待与批内顺序执行，不能与未批量的逐项往返分位数直接比较。四层证据仍不能替代 Browser、第三方延迟/限流分布、远程对象存储、5 倍执行峰值、20K Listing + 400K Detail 完整执行或长期 SLO。

### 15.3 真实网站验收

发现、Recipe、HTTP/API 和 Browser 以真实公开招聘网站验收。本地只注入不能安全施加给第三方的并发、重复、崩溃和数据库故障。

候选站型：

| 站型 | 样本 | 验证点 |
|---|---|---|
| 国内大型 SPA | 腾讯招聘、百度招聘 | 动态列表、筛选、分页、API 映射 |
| 独立招聘站 | 爱奇艺、网易相关招聘站 | 招聘类型、空职位、入口失效 |
| 通用 ATS | Moka、飞书招聘、北森公开入口 | Recipe 复用、租户差异、分页 |
| 国际站/ATS | ASML、Greenhouse 公开职位页 | 多语言、地区、时区、归一化 |
| 无职位页 | 当前公开的官方入口 | 正确识别 `no_open_jobs` |

样本需版本化保存官方 URL、origin、站型、预期结果、允许访问方式、人工确认时间。验收职位唯一性、字段质量、分页、列表详情映射、幂等和页面变化证据，不依赖固定职位总数。

每个用于增量生产的 Source 还必须验证并记录：

- 列表跨页按最新活动时间单调倒序；
- 新岗位进入顶部，历史岗位更新后重新进入顶部；
- 活动时间字段或有序边界签名能够稳定提取；
- 同一活动时间的岗位不会因翻页而遗漏；
- 置顶/广告项能够被识别，不参与边界判断；
- 到达旧边界、完成重叠和提交新 Checkpoint 可以被复现；
- 边界找不到或排序违约时不会错误推进 Checkpoint。

测试分为 Live Smoke（1–3 站）、Nightly Canary（5–10 站）和 Weekly Coverage（20–50 站）。每次保存 URL、HTTP 状态、Recipe 版本、trace、字段/去重统计、必要截图、失败分类、预算决定和抽样结论。

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
6. 业务层冻结 Company、Recruitment Source、Job Posting；运行层冻结 Recipe/Assignment、Incremental Checkpoint、SourceOccurrence、Work、Attempt、Artifact 和 Profile 安全 Resource；Target 只是统一引用。
7. trigger、purpose、initiator、cause 正交表达工作，不使用混合 source。
8. 人工审核是 `waiting_human` Work 的状态和视图。
9. 一至两次全量后，每日对全部 active Source 从列表顶部扫描到旧活动边界，只处理边界前的新增、更新和安全重叠详情。
10. 正常生产确定性优先，不逐页调用 LLM。
11. 执行可重复，结果按幂等键和 Attempt 接受条件生效。
12. 站点预算和熔断优先于扩容。
13. 大对象使用 Resource，Message 保存控制、因果和稳定引用。
14. Gateway/Driver 不能绕过 Atoll 权限、消息和领域校验。
15. Snowland/Staircase 只提供业务证据、样本和可选叶子实现。
16. 每日运行的完成口径以 active Source occurrence 成功提交 Incremental Checkpoint 为准，不每天抓取全部历史岗位。
17. Source Discovery、列表增量、岗位详情、列表修复和详情修复是不同业务 Work，但不等于不同 Executor 类型。
18. 岗位下架判断和岗位删除不在产品范围内；公司删除默认采用可恢复归档，物理删除是受控合规操作。
19. 公司和岗位列表 URL 是一对多关系；Source Discovery 只在接入、入口失效或人工复核时运行，不是每日任务。
20. Recipe 是版本化的核心执行资产；Browser Extension 是发现、修复和必要浏览器执行的可选 capability，不是每日固定依赖。
21. Company、Source、Recipe、Job、Work、Attempt 和 Daily Run 都由显式领域状态机约束；Atoll 提供状态转换所需的身份、消息、timer、ledger 和权限边界。
22. Listing Recipe 的已验证契约是：新增或更新岗位按最新活动时间进入列表顶部；系统使用每 Source 的边界锚点和重叠窗口增量运行，不进行每日岗位全集差分。
23. Daily Run 截点、SourceOccurrence 和历史日报不可变；截点后的变更用明确例外或补偿 occurrence 表达。
24. 用户控制、接入/配置可用性和运行健康正交；暂停必须有 mode，恢复默认一次 catch-up，归档默认可恢复且不删除岗位。
25. 手工运行固定为 diagnostic、join occurrence 和 production 三种；任何可推进 Checkpoint 的路径都执行同一个 CAS。
26. 一个 Recruiting Actor 和一个 Executor class 都是逻辑身份，不限制 handler 或执行实例数量；不因扩容引入角色型 Worker。
27. 共享 origin、Recipe 或 Profile 故障采用单飞修复和有界恢复，不为每个受影响岗位创建独立修复流程。
28. 公司合并第一版采用可逆逻辑映射；完整拆分、历史物理迁移和合规硬删除不属于普通首版操作。

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
- Recipe 自动发布风险阈值；
- 数据保留、删除和合规；
- 单地域或多地域；
- 查询使用领域能力还是搜索分析投影。

待决策项只有记录测试条件、观测结果、替代方案和取舍后，才能转为已确定决策。

当前 Atoll 实现仍处于 pre-release 阶段；Jobs、审批、配额以及数据安全/备份恢复能力必须以代码和故障测试确认。本文确认的是目标产品可以遵循 Atoll 架构实现，不等于当前仓库已经具备生产承诺。
