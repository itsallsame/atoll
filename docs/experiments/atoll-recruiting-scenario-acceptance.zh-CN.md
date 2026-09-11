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
| S13 Company 更新 | 部分完成 | `TestCompanyUpdateKeepsIdentityAndReportsWebsiteImpact`、公开 `company.update` E2E | 官网变化后的自动失效/重 discovery、显式回滚旅程 |
| S14 Company 合并/拆分 | 完成 | `TestRecruitingOperatorLogicallyMergesAndReversesCompanies`、`recruiting-company-logical-merge-20260911.json` | — |
| S15 Company 暂停/恢复 | 部分完成 | Company 状态机、原子 `ScopeControlOperation`、500-Work seek 暂停/恢复、逐 Source 短事务 catch-up、冻结 Source 上界、公开 operation/catch-up 查询、跨执行类型的独立配置/取消 fence、`TestCompanyResumeProjectsAllAndOnlyItsSourceWorks`、`recruiting-scope-execution-fences-20260912.json` | 全部业务聚合的 cancel 联合终态与完整截点竞态矩阵 |
| S16 Source 暂停/恢复 | 部分完成 | Source 状态机、活动因果根捕获/结算、暂停范围候选隔离、一次当前 production catch-up、Checkpoint 不前移；Listing/Detail 具备 drain/cancel/config 联合证据，Baseline、Backfill、diagnostic、Recipe sample 已验证 drain；`TestDetailResultUsesSeparatedScopeFences`、`recruiting-scope-execution-fences-20260912.json` | Source discovery/validation/Recipe validation/Backfill/Baseline 的 cancel 与配置变化组合矩阵，以及统一联合终态 |
| S17 Source redirect/改归属 | 未完成 | Endpoint correction 能建立 candidate 并 fence 旧验证；Company logical merge 不改写 Source 归属 | redirect 谱系/cutover 已有部分事实，但没有显式 Source 改归属命令与兼容性验收 |
| S18 Company 归档/合规删除 | 部分完成 | 逻辑 archive/restore 和归档后拒绝新增 Source | M5 合规硬删除流程、保留策略和联合 Resource 清理 |
| S19 Source 归档/恢复 | 部分完成 | `TestSourceArchiveRestoreRequiresValidation`、公开 archive/restore E2E | 同 URL 新增时引导恢复的用户可见合同和完整重新校准旅程 |
| S20 数据纠正与重算 | 完成 | `TestRecruitingOperatorCorrectsAndClearsJobWithoutRewritingCrawlFacts`、override CAS、S21 artifact recompute | — |
| S21 历史回填 | 完成 | `TestRecruitingLiveDetailRecipeRepairThroughAtoll` 中两种 backfill、`recruiting-live-historical-backfill-20260911.json` | — |
| S22 Recipe 批量升级 | 完成 | 20,000 Source 多 wave 合同、`TestRecruitingLiveDetailRecipeRolloutBatchThroughAtoll` | — |
| S23 临时手工运行 | 完成 | join occurrence、diagnostic、production/recovery 与取消合同 | — |
| S24 重试/人工结案 | 完成 | Work retry/resolve、baseline accepted gap、D0—D6、人工修复 E2E | — |
| S25 10K/20K 日常运行 | 部分完成 | L0—L4 数据库计划/物化容量、5 倍到期形状、400K Detail Work | 真实 Executor/Artifact/ledger 吞吐、长期指标与生产 SLO |

## 当前实施顺序

1. S15/S16：补齐 Listing 之外执行类型的 pause/cancel/result 联合终态与全截点竞态矩阵。
2. S13：Company 官网变化必须使旧 discovery 依据失效，形成新 discovery/人工确认路径；补回滚。
3. S17/S19：补 Source redirect/归属迁移和“相同 URL 应恢复而非新建”的公开交互。
4. S18：按独立 M5 合规流程设计保留、擦除、Resource 删除和证明，绝不把普通 archive 伪装成硬删除。
5. S12/S25/P9：在授权部署环境完成真实登录、出站隔离、执行吞吐、联合恢复和长期监控。

每关闭一项，必须同时更新本账本、开发验证计划和对应机读证据。账本状态为 `部分完成` 或 `未完成` 时，不得宣称 P8/P9 或整体产品开发完成。
