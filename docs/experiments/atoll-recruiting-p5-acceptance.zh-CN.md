# Atoll Recruiting P5 接入与基线验收记录

状态：进行中，未达到 P5 退出门。

## 已形成的业务能力

- `recruiting.source.discover` 显式创建不可变 discovery generation，并原子提交 Work、命令 receipt、领域事件和 Executor dispatch；它只用于接入、入口失效或人工复核，不进入每日调度。
- HTTP Executor 按 Company seed、active discovery Recipe、capability、origin、robots/terms 和 Attempt incarnation 执行一次有界发现；每批最多 500 个候选，响应 Artifact 与候选证据绑定。
- `recruiting.source.discovery.candidates` 提供 generation 绑定的 seek pagination；每个候选具有独立版本和终态，接受或拒绝一个候选不改变兄弟候选。
- `recruiting.source.discovery.candidate.accept` 在同一事务接受候选并创建 `candidate` Source；`reject` 只记录带认证操作者与理由的拒绝。两者均支持稳定命令重放。
- 同一规范 Source key 已由另一 Company 使用时，接受事务不创建 Source、不改变 Candidate、不保留 receipt，返回业务键冲突供人工确认归属。
- `recruiting.source.validation.publish` 是独立的证据发布闸门。它重新锁定 validating Source、active Listing Recipe、当前 Assignment 版本和全部证据 Artifact，验证四项契约结论均为 `verified` 后，原子发布 active Endpoint、Listing Assignment、SourceContractAssessment、receipt 与事件。
- 验证证据必须属于以目标 Source 为 target 的 Work，并且 Artifact 未被拒绝、不是 failure-only；引用缺失或串线证据时整个事务回滚。
- `recruiting.baseline.start` 已建立首个可执行列表基线：命令把 Company `discovering_sources → initializing`、不可变 BaselineGeneration、Work、receipt、两类事件和 capability dispatch 原子提交。Baseline 冻结 Company/Source/Assignment/Recipe/Endpoint 版本，以及从已验证 Source 契约复制的 Checkpoint strategy/overlap。
- 统一 Executor 已能领取 baseline listing，沿用同一个 HTTP/Browser Recipe 执行面；每页最多 500 条，只写 generation staging。terminal completion 重新核对 Attempt fence、完整分页/排序/边界证明和 staging 数量，然后原子建立首个 Checkpoint并完成 Baseline listing、Attempt 与 Work；命令与结果均可稳定重放。

## 已执行证据

- 真实网站：`https://www.mongodb.com/careers`，实际规范入口为 `https://www.mongodb.com/company/careers/see-jobs`。
- 真实链路：普通用户通过 Atoll Portal/WebSocket 新增 Company；Recruiting Actor 建立 generation；真实 daemon 上的 Recruiting Executor 访问公开页面并保存 Artifact；用户接受候选后回读到归属正确、readiness 为 `candidate` 的 Source；发现与接受命令重放均稳定。
- 隔离数据库：MySQL 8.4；migration 与 runtime 使用不同非 root 账号。合同测试覆盖 discovery 执行、候选独立裁决、跨 Company 冲突回滚、验证证据绑定、发布原子性和命令重放。
- 工程检查：招聘扩展 race、vet 通过；核心边界脚本以 `a94d2b8d` 为冻结基线通过。

## 尚未通过的退出项

- 尚未实现 Source validation Work 的专用创建、执行与结果协议；当前 publish 只接受已经存在且正确归属的证据，不能替代验证执行。
- MongoDB 单次真实样本不能证明“历史岗位更新后重新置顶”，因此不得把它标记为 `update_retop=verified`，也没有借此发布为每日增量 Source。
- baseline generation 的有界分页、staging/finalize 和首次 Checkpoint 已通过隔离 MySQL 合同，但尚未贯通真实站点；staging 到 Job/幂等 Detail Work 的有界物化、详情核算和 Company ready 尚未接通。
- 零 Source、1 万岗位、baseline 分页中断、详情部分失败和用户取消仍需加入 P5 场景验收。

只有完成 `discovery → validation → baseline → detail` 的至少一个允许访问的真实站点，并证明同一命令重放不改变岗位数，P5 才能标记完成。
