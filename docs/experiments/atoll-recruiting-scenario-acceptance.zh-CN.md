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
| S25 10K/20K 日常运行 | 部分完成 | L0—L4 数据库计划/物化容量、5 倍到期形状、400K Detail Work；P1 四 Executor/1,000 Company；A1 200 历史 response/derived；H1/H2 受控 HTTP response capture 正确性到 1,000×16 KiB。扩展内 32 项 batch envelope 保留逐 Attempt 状态机/Permit/receipt/Artifact，把 H2 从 5.88/s 提升至 13.41/s（+128.1%）。H3 完成 5,000/5,000 个 4 KiB response 的受控 5 倍执行峰值，8 Executor、20.48 MB、237 条紧凑 dispatch 全部正确，但持续吞吐降至 10.20/s。批次 claim/result 现先做一次有界整批归属预检；H2 只改善 0.85%，wave-limit 对照只改善 0.02% 并已撤回，证据为 `recruiting-batch-preflight-20260912.json`。真实 Chrome B0/B1/B2 已到 100 DOM + 100 trace；B1 2 Executor 1.87/s、B2 4 Executor 1.88/s，确认 Browser 主机饱和且需独立水平池 | 第三方 HTTP/Browser 延迟、限流和脚本复杂度，授权登录 canary，远程 Artifact，多个隔离执行/控制分片，20K Listing + 400K Detail 完成、长期指标与生产 SLO；批量内部逐 Attempt 事务仍是持续吞吐瓶颈 |

## 当前实施顺序

1. S12/S25/P9：在授权部署环境完成真实登录、出站隔离、执行吞吐、联合恢复和长期监控。

每关闭一项，必须同时更新本账本、开发验证计划和对应机读证据。账本状态为 `部分完成` 或 `未完成` 时，不得宣称 P8/P9 或整体产品开发完成。
