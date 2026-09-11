# Atoll Recruiting 运行与故障处置手册

状态：预生产版本。已实现的控制命令可以按本手册演练；文末列出的生产准入项未关闭前，不得把系统标记为生产就绪。

## 1. 不可突破的边界

- Recruiting 是 Atoll 扩展。不得为排障修改 `protocol/`、`runtime/`、`lib/`、`platform/` 或 `registry/`，也不得增加第二套调度权威。
- 操作员只通过 Atoll 登录身份和 Recruiting 公开消息操作。不得直接修改招聘表、伪造 Executor 结果或手工移动 Checkpoint。
- migration 与 runtime 使用两个非 root MySQL 账号。runtime 只有业务表的 `SELECT/INSERT/UPDATE/DELETE`，没有 DDL、用户管理或全库权限。
- 真实网站只允许 GET/HEAD，只访问公开信息或明确授权 Profile；遵守 robots、条款、429 与 `Retry-After`，不绕过验证码、不提交表单。
- Job 缺席和岗位删除不属于当前产品结论。只有可靠的顶部增量、边界与重叠证据才能推进 Checkpoint。

## 2. 部署前检查

1. 从秘密管理器向 migration 进程注入 `ATOLL_RECRUITING_MYSQL_DSN`，运行 `bin/atoll-recruiting-migrate`。不要把 DSN 写入仓库、消息、Actor config 或命令行参数。
2. 改用 runtime DSN 启动 Atoll Server。启动后以普通运营员调用 `recruiting.system.status`；数据库不可用、migration checksum 不一致或 schema 缺失时停止发布。
3. 检查 Recruiting Actor 的版本化配置：时区、cutoff、运行窗口、policy version、每 tick 物化上限、Attempt TTL、预算和按 origin 的失败策略。
4. 对每种已发布 capability 至少配置一个 `recruiting-executor` 实例。Browser Profile 的 Executor 必须是 Profile 绑定的设备，不能落入普通 fleet。
5. 调用 `recruiting.capacity.status`，确认 runnable age、deadline miss、活动 Permit、各层预算和 dispatch/event outbox 没有启动前积压。
6. 在扩大流量前依次执行：

```bash
make recruiting-boundary-check
make recruiting-model-test
make recruiting-mysql-test
make recruiting-process-capacity
make recruiting-artifact-capacity
make recruiting-live-smoke
```

Nightly、Weekly、L2—L4 和备份恢复按发布层级执行；第三方网络测试必须显式启用，不得在普通单元测试中隐式访问公网。

## 3. 新公司接入

接入必须沿着公开状态机推进：

```text
company.add
  → source.discover 或 source.add
  → discovery candidate accept/reject
  → Listing Recipe propose → validate → approve
  → source.validate → source.validation.publish
  → Detail Recipe propose → validate → approve
  → recipe.assign（只创建首次 Detail Assignment）
  → baseline.start
  → 等待 listing finalize、Job/Detail Work 有界物化和详情核算
  → Company ready
```

关键检查：

- 一个 Company 可以对应多个 Source；不要覆盖旧 URL 来表达第二个来源。
- Discovery 只用于接入、入口失效和人工复核，不是每日任务。
- Source validation 的 identity、pagination、ordering、update-retop 四项分别记录。没有真实更新样本时，`update-retop` 必须保持 `unverified`。
- `recipe.assign` 只用于不存在 Detail Assignment 的新 Source。已有绑定的更新走验证后的 `recipe.rollout` 或 `recipe.rollback`。
- Browser Recipe 若响应要求先绑定 Profile，先执行 `profile.register/repair` 和 `source.profile.bind`，不得伪造 Profile ready。
- Baseline listing 完成不等于全部详情完成。只有成员全部 succeeded 或由用户逐项接受缺口后，Company 才能 ready。

## 4. 每日运行

durable timer 到达 cutoff 时冻结当日 eligible Source roster，形成不可变 DailyRun 和 SourceOccurrence。后续 Company/Source 修改不能重写当日分母。

值班检查顺序：

1. `recruiting.system.status`：检查数据库、运行批次、Attempt、RepairIncident 和两个 outbox；同时查看最近一小时 `execution_health` 的结果分布、终态/恢复时延和 rejected Artifact。若 `attempts_truncated` 或 `rejected_artifacts_truncated` 为真，该数值只是最近 1,000 条的下界，必须转到外部 metrics 查询完整时序。
2. `recruiting.daily_run.list/get/summary`：比较 expected、planned、listing success、exception、excluded、uncovered 和 recovered。
3. `recruiting.capacity.status`：检查 oldest runnable age、deadline miss、capability/origin/Profile backlog 与活动预算。
4. `recruiting.work.list`：按 status、purpose、trigger、waiting reason、Target、initiator 和时间游标下钻。
5. `recruiting.repair.list/get`：按共享故障查看成员，不把单飞 Incident 的种子数组当作完整受影响集合。

每日列表从顶部扫描至旧 Checkpoint 边界，并完整消费同时间组与安全重叠。成功页面、Observation、Detail Work 意图和 candidate frontier 经过版本围栏提交；排序异常、边界消失、超页数或中断均保留旧 Checkpoint。

窗口关闭后，未覆盖项留在不可变日报中。修复后若需要补产，使用带 `recovery_of_occurrence_id` 的 `recruiting.run.production`；恢复只增加 `recovered`，不能把原来的 `uncovered` 改成成功。

## 5. 人工运行的选择

| 目的 | 命令 | 是否改变生产事实 |
|---|---|---|
| 立即执行当日仍 planned 的项 | `recruiting.run.join_occurrence` | 加入同一 occurrence，遵守原围栏 |
| 检查 Recipe、页面或修复效果 | `recruiting.run.diagnostic` | 否，只保存证据 |
| 独立生产同步或关闭日报后的补偿 | `recruiting.run.production` | 是；补偿时必须引用原 occurrence |

不要用 diagnostic 结果人工写回 Job。不要创建第二条相同日报缺口的恢复谱系。

## 6. 常见故障处置

| 现象 | 首要检查 | 安全动作 | 禁止动作 |
|---|---|---|---|
| 429/403/5xx 激增 | origin、Retry-After、circuit、失败策略版本 | 保持自动退避；必要时暂停 Source/Company | 提高并发、绕过 robots |
| 列表排序或边界失败 | page/trace Artifact、Recipe/Assignment、旧 Checkpoint | diagnostic；候选 Recipe 验证；重新校准 | 手工推进 Checkpoint |
| 同 Recipe 大量解析失败 | RepairIncident、failing version、影响成员数 | quarantine；验证修复版；canary rollout；有界 recover | 为每个 Work 建独立人工单 |
| 单岗位详情失败 | 原始 response 与 failure Artifact、Job generation | 终结旧 Work；发布兼容 Recipe；创建 causal retry | 重开旧 Attempt、覆盖旧详情版本 |
| Profile 过期或验证码 | Profile state、授权设备、redacted Artifact | `profile.repair.begin`，由绑定设备验证后 resolve | 把 Cookie/OTP 放入消息或 Artifact |
| runnable age 上升 | capability fleet、origin/Profile 预算、dispatch backlog | 增加同 capability Executor；修复配置或预算瓶颈 | 增加 Actor/Worker 类型规避背压 |
| event/dispatch outbox 积压 | Server/daemon presence、最后错误、重放次数 | 运行/等待有界 reconcile；确认稳定 ID 收口 | 删除 outbox 行或伪造 delivered |
| DailyRun uncovered | occurrence 终态、失败 Incident、window close | 修复后创建唯一 production recovery | 改写历史 DailyRun summary |

共享修复的标准顺序：

```text
repair.get
  → 修复候选形成独立 Work/Attempt
  → repair.validation.begin（引用真实成功证据）
  → repair.resolve
  → repair.recover（每次最多 100，反复执行直到收口）
```

恢复仍受 capability、origin、Company 和 Profile 预算约束；不要为了快速清空人工队列关闭这些上限。

## 7. 暂停、取消、归档和回滚

- Company/Source pause mode：`drain` 停止新调度并让在途结算；`finish_causal_chain` 允许当前因果链完成；`cancel` 提升接受围栏并取消适用的未完成工作。选择前必须用查询记录影响范围。
- pause/resume 响应中的 `scope_control_operation_id` 是后续操作的权威身份；用 `recruiting.scope_control.get` 查询 operation 终态和分页 catch-up decisions。对应 pause 未完成时 resume 会整体拒绝，不要靠重复 resume 或猜测下一次 reconcile 的返回项判断完成。
- resume 先有界恢复属于该 pause 的安全 Work，再按恢复截点冻结的 Source 上界逐 Source 创建至多一个当前 production catch-up。`active_listing_work_exists`、`source_not_currently_eligible`、`checkpoint_not_established`、`profile_not_ready` 等 skipped reason 必须显式处置；系统不会按暂停天数补造 DailyRun，也不会在 catch-up 规划时移动 Checkpoint。
- Work pause/resume 只管理一个业务 Work；有活动 Attempt 或不允许转换时，使用专用领域命令，不直接改状态。
- cancel 会保留历史和 rejected Artifact。旧 Executor 的迟到结果不得恢复业务事实。
- archive 是逻辑归档，不删除 Job、Observation、DetailVersion、Artifact 引用或审计事实；restore 后先验证再 catch-up。
- Recipe quarantine 是常量规模的版本熔断。Listing rollback 保留 frontier 但令 Source 进入 `repairing`；Detail rollback 不改变 Listing readiness。二者都必须引用不可变 Assignment 历史。
- 批量 Recipe rollout 先预览并确认 hash，只开放 canary，再按固定 wave 推进。失败后显式 resume 或 rollback，不能跳过暂停 wave 扩散到未来成员。

Company 合规物理擦除不得使用普通 archive 或直接执行 SQL：

1. 先归档全部 Source、结算活动 Work、关闭活动 alias，再提交 `recruiting.company.erasure.preview`；保存 policy version、`execute_after`、请求人和原因。
2. 用 `recruiting.company.erasure.get` 等待有界预览进入 `awaiting_approval`，由另一 human principal 核对 Source/Job/Work/Artifact 影响量及精确 preview hash 后调用 `recruiting.company.erasure.approve`。同一用户换会话或 actor incarnation 仍不能自批。
3. 保留期内不得恢复 Company/Source。到期后由 durable reconcile 固化 Resource 清单并分阶段擦除；用 `recruiting.company.erasure.resources` 分页取得对象。Actor 没有替用户删除 Resource 的权限，必须由创建者或频道 owner 通过原生 Resource delete 执行。
4. Resource delete 后调用 `recruiting.company.erasure.resource.verify_absent`；对象仍存在时命令必然拒绝。继续 reconcile，直到 `company.erasure.get` 返回 `completed`、控制 Work 为 `succeeded` 且 proof hash 可读。
5. proof、冻结成员、Resource 清理记录以及政策允许的最小 command/event/dispatch 审计是故意保留的合规事实；Company ID 是永久 tombstone，不得重新创建。任何阶段失败都从当前 phase 重试，禁止跳 phase、改 hash 或手工删控制表。

## 8. 历史回填

- `artifact_recompute` 从已冻结的原始 Artifact 离线重算，声明原观测时间；`live_refetch` 读取当前页面，只能声明当前结果。
- 预览 hash、范围、Recipe 和逐项输入在确认前必须由用户复核。
- 回填使用独立 workload 预算，不更新日常 Checkpoint、Job 当前详情或历史日报。
- pause 采用 drain；cancel 先写 fence，再由 reconcile 分批收口。失败项只能 `retry` 新因果 Work 或显式 `accept_gap`。

## 9. 备份与灾难恢复

生产备份至少联合覆盖三个存储域：Recruiting MySQL、Atoll ledger、Artifact provider。只恢复其中一个不能证明业务因果完整。

恢复顺序：

1. 停止生成新的 occurrence 和新的 Executor offer，记录最后可见 DailyRun、outbox 和 Checkpoint 版本。
2. 从一致时间点恢复 MySQL、ledger 与 Artifact；校验 migration ledger/checksum，不运行逆向 DDL。
3. 以 runtime 非 root 身份只读核对 Company/Source/Work/Attempt、receipt、event outbox、dispatch outbox 和 Artifact 引用。
4. 在隔离环境重放稳定 command ID，证明 receipt 返回原 outcome；运行 reconcile，证明 ledger/event 与 dispatch 不重复产生有效结果。
5. 先 diagnostic，再单 Source production，然后按 10、100、1,000、10,000 Company 分级恢复。

数据库逻辑恢复入口为 `make recruiting-backup-restore`；停止写入后的 MySQL + Atoll ledger + daemon File Resource 联合恢复入口为 `make recruiting-joint-restore`。后者必须恢复到不同数据库名和不同 Server/daemon 目录，禁止用原地重启冒充恢复。两个机制演练均不替代生产规模 RTO/RPO、在线一致性点、加密异地保留、binlog 时间点恢复、远程 Artifact provider 和地域灾难演练。

## 10. 发布与回滚门

发布依次经过本地 fixture、隔离 MySQL、单 Source diagnostic、单 Source production、10 Source canary、100、1,000、10,000 Company；每一级至少观察一个完整窗口。

每次发布保存：Git revision、migration 版本、Actor/Executor 配置 hash、Recipe/Assignment 版本、真实站点清单版本、容量档位、边界检查、测试报告和已接受 gap。

发布候选必须运行 `make recruiting-security-audit`，阻断提交的高置信私钥/云服务凭证，以及证据和 workload JSON 中的 credential-shaped 字段或 `secret://` 引用。该仓库门不扫描已经从当前工作树删除的 Git 历史，也无法验证部署 Secret Provider、环境变量和挂载文件；生产发布必须另外保存这些部署侧扫描结果。

应用回滚不能删除已接受事实。旧二进制只有在 schema、Message 和 Recipe ABI 兼容检查通过后才可启动；否则停止新调度并前向修复。

## 11. 当前尚未关闭的生产门

- 已通过的 Women’s Aid 活动倒序/更新置顶真实纵向样本仍需纳入 Nightly/Weekly 持续监测；单次通过不代表第三方契约永久不变；
- 生产 OS/container 级出站隔离和真实授权登录站点 canary；
- HTTP/Browser 外部执行与 response capture、20K Listing + 400K Detail 完成吞吐与全部故障注入矩阵；本地 derived Artifact 基线已经通过，不得外推为远程对象存储吞吐；
- 生产数据量的 RTO/RPO、binlog PITR、加密异地保留，以及远程 MySQL/ledger/Artifact 联合恢复；
- 仓库级非 Recruiting 核心 E2E blocker 的上游修复。

上述项目存在时，系统可以继续开发和受控验收，但不得宣称 P9 或生产准入完成。
