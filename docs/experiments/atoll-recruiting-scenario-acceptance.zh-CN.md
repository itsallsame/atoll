# Atoll Recruiting S01—S25 场景验收账本

更新时间：2026-09-12

本账本按产品场景判断完成度，不以代码量、测试总数或某个压力测试代替业务验收。状态含义：

- `完成`：核心断言有自动化证据，关键用户交互有进程级旅程；第三方长期可用性仍由持续 canary 管理。
- `部分完成`：已有领域或 Repository 能力，但缺少场景中的行为、进程级验证或生产环境证据。
- `未完成`：至少一个核心业务行为尚未实现，不能用相邻场景推断完成。

| ID | 状态 | 当前权威证据 | 尚缺内容 |
|---|---|---|---|
| S01 单个公司新增 | 完成 | `TestRecruitingCompanySourceAndWorkControlUsesMySQLAcrossServerRestart`、`TestCreateCompanyCommandIsAtomicAndReplayable` | — |
| S02 批量导入公司 | 完成 | `TestRecruitingCompanyImportPreviewThroughResourceAndExecutor`、`recruiting-company-import-human-resolution-20260911.json` | — |
| S03 Source Discovery | 完成 | `TestRecruitingLiveSourceDiscoveryThroughAtoll`、`TestRecruitingLiveZeroSourceDiscoveryThroughAtoll`、`TestSourceDiscoveryRepositoryContract` | — |
| S04 人工维护 Source | 完成 | `TestRecruitingCompanySourceAndWorkControlUsesMySQLAcrossServerRestart`、`TestSourceValidationCreatesFencedExecutionAndEvidenceForPublish` | — |
| S05 首次全量 | 完成 | `TestCurrentBaselineMaterializerHandlesTenThousandJobsInBoundedPages`、`TestRecruitingLiveBaselinePageRecoveryThroughAtoll`、`TestRecruitingLiveQualifiedWordPressSourceThroughAtoll` | — |
| S06 第二次校准 | 完成 | `TestSourceCannotPublishAnUnverifiedIncrementalContract`、Source validation 四维 assessment 合同、Women’s Aid update-retop 真实证据 | 持续观测由 Nightly/Weekly 管理，不改变一次验收结论 |
| S07 每日列表增量 | 完成 | `TestDailyIncrementalD0ThroughD6PreservesBoundaryAndRefreshInvariants`、`TestRecruitingLiveQualifiedWordPressSourceThroughAtoll` | OS 级全切点矩阵属于 P9 可靠性门 |
| S08 新岗位详情 | 完成 | `TestListingObservationCreatesDetailOnlyForNewOrChangedJob`、qualified WordPress 17 个真实详情 | — |
| S09 变化岗位详情 | 完成 | detail version/refresh generation 合同及 D0—D6 旅程 | — |
| S10 Listing 修复 | 完成 | `TestRecruitingOperatorQuarantinesAndRollsBackRecipeThroughServer`、Listing rollout/rollback 合同 | — |
| S11 Detail 修复 | 完成 | `TestRecruitingLiveDetailRecipeRepairThroughAtoll`、共享 RepairIncident 和有界恢复合同 | — |
| S12 Profile 修复 | 部分完成 | `TestRecruitingOperatorStartsDeviceBoundProfileRepairThroughServer`、真实 Chrome Profile 复用、Extension bundle | 第三方真实授权登录 canary、生产 OS/container 出站隔离 |
| S13 Company 更新 | 完成 | `TestCompanyWebsiteChangeCreatesReviewAndRollbackAppendsHistory`、`TestConcurrentCompanyWebsiteChangesCommitOneCompleteRevision`、`TestSourceDiscoveryResultUsesSeparatedCompanyFences`、`TestRecruitingCompanyWebsiteReviewRollbackAndRediscoveryThroughServer`、`recruiting-company-website-review-20260912.json` | — |
| S14 Company 合并/拆分 | 完成 | `TestRecruitingOperatorLogicallyMergesAndReversesCompanies`、`recruiting-company-logical-merge-20260911.json` | — |
| S15 Company 暂停/恢复 | 完成 | 三种 pause mode、原子 `ScopeControlOperation`、500-Work seek 暂停/恢复、冻结 Source 上界、逐 Source catch-up、公开 operation 查询、跨执行类型 fence/owner/cutpoint；35 组 scope/result 并发与 10 组 scope/general Backfill 协调器并发；`TestCompletedExecutionOwnersSurviveLaterScopeCancel`、`TestScopeCancelRejectsLateExecutionOwnerResults`、`TestScopeCancelAndResultsConvergeUnderConcurrentStress`、`TestScopeAndGeneralBackfillCancelCoordinatorsConvergeUnderConcurrentStress` | — |
| S16 Source 暂停/恢复 | 完成 | 活动因果根、一次当前 production catch-up、Checkpoint 不前移、其他 Source 隔离、验证取消可重试、Company Backfill Source-only cancel、全执行类型 drain/cancel/config/result 和并发协调器合同；`TestSourceCancelIsolatesCompanyBackfillMembers`、`TestBackfillResultAndScopeCancelConvergeAtBothCommitCutpoints`、`recruiting-scope-execution-fences-20260912.json` | — |
| S17 Source redirect/改归属 | 完成 | `TestRecruitingEndpointRedirectActivatesOnlyAfterValidatedCutover`、`TestRecruitingOperatorReassignsSourceAcrossCompanyWithoutRewritingHistory`、`TestSourceReassignmentPreservesHistoryAndCreatesIndependentCandidate`、`TestSourceSplitReassignmentRetainsOldSourceAndSerializesConfirmation`、`recruiting-source-lineage-cutover-20260912.json` | — |
| S18 Company 归档/合规删除 | 完成 | `TestCompanyErasureBuildsBoundedFrozenPreview`、`TestRecruitingTwoOperatorsApproveFrozenCompanyErasureAcrossRestart`、`recruiting-company-compliance-erasure-20260912.json` | — |
| S19 Source 归档/恢复 | 完成 | `TestSourceArchiveRestoreRequiresValidation`、`TestCreateSourceReturnsActionableRestoreForArchivedCanonicalIdentity`、`TestRecruitingArchivedSourceGuidesRestoreAndRequiresRecalibration`、`recruiting-source-restore-recalibration-20260912.json` | — |
| S20 数据纠正与重算 | 完成 | `TestRecruitingOperatorCorrectsAndClearsJobWithoutRewritingCrawlFacts`、override CAS、S21 artifact recompute | — |
| S21 历史回填 | 完成 | `TestRecruitingLiveDetailRecipeRepairThroughAtoll` 中两种 backfill、`recruiting-live-historical-backfill-20260911.json` | — |
| S22 Recipe 批量升级 | 完成 | 20,000 Source 多 wave 合同、`TestRecruitingLiveDetailRecipeRolloutBatchThroughAtoll` | — |
| S23 临时手工运行 | 完成 | join occurrence、diagnostic、production/recovery 与取消合同 | — |
| S24 重试/人工结案 | 完成 | Work retry/resolve、baseline accepted gap、D0—D6、人工修复 E2E | — |
| S25 10K/20K 日常运行 | 部分完成 | L0—L4 数据库计划/物化容量、5 倍到期形状、400K Detail Work；P1 四 Executor/1,000 Company；A1 200 历史 response/derived；H1/H2 受控 HTTP response capture 正确性到 1,000×16 KiB。扩展内 32 项 batch envelope 保留逐 Attempt 状态机/Permit/receipt/Artifact，把 H2 从 5.88/s 提升至 13.41/s（+128.1%）。H3 完成 5,000/5,000 个 4 KiB response 的受控 Backfill 峰值；专用候选查询及 O(n) 父聚合后为 10.65/s。新增每日真实数据面 D0—D3，不再用 Backfill 替代：durable cutoff→SourceOccurrence→10 页 Listing→5,000 Detail GET/Attempt/JobDetailVersion/response Resource→日报结算→Server 重启全部通过；事务批处理后 D3 为 14.24/s，20,480,000 字节逐对象校验，证据为 `recruiting-daily-detail-capacity-20260912.json` 与 `recruiting-daily-detail-transaction-batching-20260912.json`。真实 Chrome B0/B1/B2 已到 100 DOM + 100 trace；B1 2 Executor 1.87/s、B2 4 Executor 1.88/s，确认 Browser 主机饱和且需独立水平池 | 第三方 HTTP/Browser 延迟、限流和脚本复杂度，授权登录 canary，远程 Artifact，多个隔离执行/控制分片，20K Listing + 400K Detail 完成、长期指标与生产 SLO；本机 D3 只越过算术参考，不能替代完整生产负载证明 |

S25/P9 补充（2026-09-12）：真实每日进程矩阵已固定注入一次 503、一次带 `Retry-After` 的 429 和持续 403，证明前两项有界自动恢复、403 单飞进入人工修复且日报按 19 success/1 exception 结算；证据为 `recruiting-daily-detail-http-failure-matrix-20260912.json`。它关闭 HTTP 状态码故障注入项，但不改变 S25 的“部分完成”状态。

S25/P9 补充（2026-09-12，Artifact provider）：独立 File provider 在 Listing Attempt 已 running 且 HTTP 请求已经到达 origin 后被 `SIGKILL`；TTL 回收通过 Attempt 持久绑定的来源 dispatch 收口旧 replay identity，并用新 `attempt_recovered` dispatch 产生新 Attempt。权威旅程中 provider 停机 30.252 秒，最终一个 Listing Attempt expired、一个 succeeded，20 个 Detail 与 20,480 字节 response、日报及 Server 重启回读全部通过；证据为 `recruiting-daily-artifact-provider-recovery-20260912.json`。它关闭本地 File provider 进程恢复项，但远程 provider 仍在 P9，S25 状态不变。

S25/P9 补充（2026-09-12，Browser + Artifact provider）：`make recruiting-browser-artifact-recovery` 使用真实 Chrome 执行 daemon 和另一个已认证 File provider daemon；在 Browser Attempt 已 running、受控 origin 已接受文档请求且尚无 Artifact 时 `SIGKILL` provider。BF0 中 provider 停机 29.844 秒，旧 Attempt expired 且零 accepted Artifact，新 `attempt_recovered` dispatch 产生不同 Attempt；最终一个 DOM response、一个 effect trace 和一个 BackfillOutput 成功，Permit/dispatch 清零，Server 重启后两类 Resource 均可读。正常 B0 同时回归通过。该证据关闭真实 Chrome 执行期间的本地 provider 进程故障组合，但不代表同时杀 Chrome 与 provider、远程对象存储或第三方登录站点；S25 仍为部分完成。证据为 `recruiting-browser-artifact-provider-recovery-20260912.json`。

S25/P9 补充（2026-09-12，Browser 联合进程恢复）：BF1 在同一精确切点同时 `SIGKILL` Chrome 所在执行 daemon 进程组和独立 provider。两个执行侧进程均停止时，在线控制面通过设备失联/incarnation fence 在 601 ms 内把旧 Attempt 收口为 expired、释放 Permit 并持久化恢复 dispatch；两台 daemon 以原设备身份恢复后，新 Executor incarnation 用不同 Attempt 完成唯一 DOM、trace 和 BackfillOutput。两次网站请求可解释，旧 Attempt 零 accepted Artifact，全部 dispatch delivered，Server 重启后两类 Resource 可读；BF0/B0 回归通过。该证据关闭本地双进程联合退出切点，但不代表 Server/MySQL 同时故障、远程存储或第三方站点；S25 状态不变。证据为 `recruiting-browser-joint-process-recovery-20260912.json`。

S25/P9 补充（2026-09-12，事务批处理）：Recruiting 扩展把同一批最多 32 个 Detail 的 Offer、Claim、Result 从“协议批量、数据库逐项事务”收敛为有界事务快速路径，并将共享预算维度按批聚合加减；每个 Attempt、Work、Permit、command receipt、Artifact、结果状态和幂等键仍独立持久化。异常快速路径整体回滚后再走既有逐项语义，Offer 遇到容量或无候选时只提交有效前缀；Listing、Browser 和多结果任务不进入该路径。每日 D2 从 77.079 秒降至 60.645 秒；D3 从 423.655 秒降至 351.175 秒，即 14.24 Detail/s，按该本机受控速率推算 400K 为 7.80 小时。它越过 13.89/s 算术参考，但没有实际执行 400K、20K 同时 Listing、第三方网络、远程 Artifact、多分片或长期 SLO，因此 S25 仍为部分完成。证据为 `recruiting-daily-detail-transaction-batching-20260912.json`。

S25/P9 补充（2026-09-12，Atoll Server 在途恢复）：BF2 在真实 Chrome 请求已进入 origin 且尚无 Artifact 时杀 Server，原执行 daemon OS 进程全程存活。首次运行观察 211 秒后仍有 running Attempt，反向发现周期 reconcile timer 在“fire 已出队、后继 ID 未持久化”的 crash cut 会断链。Recruiting Actor 现仅在 body 启动时替换周期 reconcile timer；每日 cutoff/work/close timer 保留其精确持久 deadline，Atoll core 零修改。加强后的权威复跑证明同一 Executor Actor 以新 incarnation 重连、旧 Attempt expired 且零 accepted Artifact、新 Attempt 唯一完成 DOM、trace 和 BackfillOutput，Permit/dispatch 清零并可再次重启回读；BF1、BF0、B0、D0 均回归通过。该证据关闭 Server 单点在途 Browser 切点，但不覆盖跨每日 cutoff、Server/MySQL 联合故障、远程 Artifact 或生产 SLO，S25 状态仍为部分完成。证据为 `recruiting-browser-server-recovery-20260912.json`。

S07/S25/P9 补充（2026-09-12，每日 cutoff 跨 Server 故障）：DF0 在每日 timer 已持久、cutoff 尚未来临时杀 Server，跨过 cutoff 后确认 MySQL 零旁路 DailyRun；恢复后旧 timer 的原日期、cutoff、window 和 policy 创建唯一 DailyRun/Occurrence。首次 221.93 秒失败定位到“首次 dispatch 已 post、Executor 尚无 Attempt、15 分钟 acknowledgement deadline 无法被后续 presence 提前”；Recruiting 现以 O(fleet+catalog) 把明确配置的 fleet 映射到具体 incarnation，复用已有 dispatch acceleration，不新增 Worker、不改 Atoll core。强化旅程先暂停原 daemon，证明 dispatch pending/已 post 且 Attempt 为零，再恢复同一进程并要求至少两次 delivery；20/20 Detail、20,480 字节 Resource、日报和再次重启均通过。该证据关闭单日 cutoff 的 Server 单点与 Server 先于 Executor 恢复切点，但不覆盖多日停机、Server/MySQL 联合故障或生产规模，S25 仍为部分完成。证据为 recruiting-daily-server-cutoff-recovery-20260912.json。

## 当前实施顺序

1. S12/S25/P9：在授权部署环境完成真实登录、出站隔离、执行吞吐、联合恢复和长期监控。

每关闭一项，必须同时更新本账本、开发验证计划和对应机读证据。账本状态为 `部分完成` 或 `未完成` 时，不得宣称 P8/P9 或整体产品开发完成。
