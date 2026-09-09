# Atoll Recruiting P5 接入与基线验收记录

状态：进行中，未达到 P5 退出门。

## 已形成的业务能力

- `recruiting.source.discover` 显式创建不可变 discovery generation，并原子提交 Work、命令 receipt、领域事件和 Executor dispatch；它只用于接入、入口失效或人工复核，不进入每日调度。
- HTTP Executor 按 Company seed、active discovery Recipe、capability、origin、robots/terms 和 Attempt incarnation 执行一次有界发现；每批最多 500 个候选，响应 Artifact 与候选证据绑定。
- `recruiting.source.discovery.candidates` 提供 generation 绑定的 seek pagination；每个候选具有独立版本和终态，接受或拒绝一个候选不改变兄弟候选。
- `recruiting.source.discovery.candidate.accept` 在同一事务接受候选并创建 `candidate` Source；`reject` 只记录带认证操作者与理由的拒绝。两者均支持稳定命令重放。
- 同一规范 Source key 已由另一 Company 使用时，接受事务不创建 Source、不改变 Candidate、不保留 receipt，返回业务键冲突供人工确认归属。
- `recruiting.source.validate` 在一个事务内把 Candidate/Repairing Source 推进到 `validating`，冻结 Candidate Endpoint、active Listing Recipe、Company/Source 版本和可选 Profile，创建专用 `source_validation` Work、validation run、receipt、事件与 capability dispatch。它不增加 Worker 类型，而由统一 Executor 按 Listing Recipe 执行。
- validation result 保存有界页面 Artifact、trace 和客观质量观测，完成 Work/Attempt 并释放预算；它不写 Job、Listing Observation 或 Checkpoint，也不代替人工判断 `update-retop`。
- identity、ordering 或 pagination 的客观质量证明不成立时，结果与证据仍被接受，并在同一事务把 Source 从 `validating` 推进到 `invalid`、写失败事件；操作者修正后可从保留的 Candidate 再次启动校验，不会以超时恢复代替业务失败。
- `recruiting.source.validation.publish` 是独立的证据发布闸门。它重新锁定 validating Source、active Listing Recipe、当前 Assignment 版本和全部证据 Artifact，验证四项契约结论均为 `verified` 后，原子发布 active Endpoint、Listing Assignment、SourceContractAssessment、receipt 与事件。
- 验证证据必须属于目标 Source 已成功完成的 `source_validation` Work/run，且 Endpoint revision、Recipe/contract、拟发布 Assignment version 完全一致；Artifact 还必须未被拒绝、不是 failure-only。引用缺失、伪造的普通 Work、旧 Endpoint/Recipe 或串线证据时整个事务回滚。
- `recruiting.baseline.start` 已建立首个可执行列表基线：命令把 Company `discovering_sources → initializing`、不可变 BaselineGeneration、Work、receipt、两类事件和 capability dispatch 原子提交。Baseline 冻结 Company/Source/Assignment/Recipe/Endpoint 版本，以及从已验证 Source 契约复制的 Checkpoint strategy/overlap。
- 统一 Executor 已能领取 baseline listing，沿用同一个 HTTP/Browser Recipe 执行面；每页最多 500 条，只写 generation staging。terminal completion 重新核对 Attempt fence、完整分页/排序/边界证明和 staging 数量，然后原子建立首个 Checkpoint并完成 Baseline listing、Attempt 与 Work；命令与结果均可稳定重放。
- finalized staging 已由 Recruiting Actor 的既有有界 reconcile 渐进物化为 Job 和幂等 Detail Work，不增加专用 Worker 类型。BaselineGeneration 保存版本化 `materialization_cursor`、已处理数和完成标记；每批最多 500 条，岗位事实、详情 Work、游标 CAS 和按 capability 聚合的 Executor 唤醒在同一短事务提交。并发协调循环只能有一个推进版本；缺少有效 Detail Recipe 的基线保留等待修复，但不会阻塞其他 Company。
- 每个 baseline Detail Work 在物化事务中写入独立成员账本；详情成功事务同时接受 JobDetailVersion、完成 Work/Attempt、释放 Permit、把成员由 pending 变为 succeeded，并以 Baseline CAS 增加核算数。终态异常先进入 `waiting_human`；只有认证用户通过既有 `recruiting.work.resolve` 明确提交 `accepted_gap` 和理由，才在同一命令事务核算缺口，运行中的 Work 不能直接伪装成缺口。Company 不在每条详情事务中加锁；Recruiting Actor 的既有 reconcile 只在所有 active/ready Source 的最新 baseline 均完成或缺口已被接受时，原子推进 `initializing → ready` 并写领域事件。

## 已执行证据

- 真实网站：`https://www.mongodb.com/careers`，实际规范入口为 `https://www.mongodb.com/company/careers/see-jobs`。
- 真实链路：普通用户通过 Atoll Portal/WebSocket 新增 Company；Recruiting Actor 建立 generation；真实 daemon 上的 Recruiting Executor 访问公开页面并保存 Artifact；用户接受候选后回读到归属正确、readiness 为 `candidate` 的 Source；发现与接受命令重放均稳定。
- 隔离数据库：MySQL 8.4；migration 与 runtime 使用不同非 root 账号。合同测试覆盖 discovery 执行、候选独立裁决、跨 Company 冲突回滚、validation 原子创建/offer/Attempt/结果、零业务数据副作用、验证证据绑定、发布原子性和命令重放；baseline 覆盖 start、offer、page staging、completion、并发物化、持久游标、Job/Detail Work 唯一性和结果重放。
- 工程检查：招聘扩展 race、vet 通过；核心边界脚本以 `a94d2b8d` 为冻结基线通过。

## 尚未通过的退出项

- Source validation 的专用 Work、统一 Executor 执行、evidence-only 结果协议和质量违反后 `validating → invalid` 已通过隔离 MySQL 合同，但尚未在真实站点贯通；执行失败后的重试、Recipe/Endpoint 变更围栏和人工修复仍需场景验收。
- MongoDB 单次真实样本不能证明“历史岗位更新后重新置顶”，因此不得把它标记为 `update_retop=verified`，也没有借此发布为每日增量 Source。
- baseline generation 的有界分页、staging/finalize、首次 Checkpoint、Job/Detail Work 有界物化、详情成功/人工接受缺口核算和 Company ready 已通过隔离 MySQL 合同，但尚未贯通真实站点；详情最终失败后的重试修复和拒绝接受缺口场景仍需完整验收。
- 零 Source、1 万岗位、baseline 分页中断、详情部分失败和用户取消仍需加入 P5 场景验收。

只有完成 `discovery → validation → baseline → detail` 的至少一个允许访问的真实站点，并证明同一命令重放不改变岗位数，P5 才能标记完成。
