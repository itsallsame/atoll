# 数字社会第一期：交付与验收矩阵

状态：第一期验收记录  
模型版本：`digital-society-v1.1`

本文把上位计划中的每项完成条件映射到可检查的代码、运行产物或自动测试。它不把“没有发现问题”当作完成证据。

## 1. 交付物

| 交付物 | 权威位置 | 验证方式 |
|---|---|---|
| 确定性社会内核 | `drivers/tools/society/model/` | 模型单元测试、十年运行 |
| Atoll actor 适配 | `drivers/tools/society/` | actor 测试和真实进程重启 e2e |
| 批量实验程序 | `cmd/society/` | `run`、`compare`、`sweep` |
| Web 控制台 | `web/societyconsole/` | 静态嵌入测试、HTTP 200、用户旅程 e2e |
| 同源页面挂载 | `drivers/gateway/portal/`、`cmd/internal/engineboot/` | portal 和 engineboot 测试 |
| 默认场景与四组制度 | `model/scenario.go` | `TestVariantsShareInitialWorld` |
| 完整 JSON/CSV 数据 | `model/export.go` | `TestExportContainsIdentityAndCompleteMetrics` |
| 十年报告与敏感性 | `digital-society-v1-baseline.zh-CN.md` | 1 次默认四组 + 2×10 种子冲击扫描 |
| 使用与模型文档 | 本目录的 plan、spec、runbook、baseline | 文档关键词与命令复核 |

## 2. 计划条目覆盖

### 社会构成与行为

- 100 名初始居民、家庭、10 家企业、政府、媒体、四个社区和显式社会关系图：默认场景快照可查。
- 财富、收入、债务、就业、食物、住房、健康、风险、公平感、支持、观点、有限记忆和当前策略：均为居民快照字段；货币财富由领域账本持有。
- 工资、税收、福利、生产、定价、购买、消费、家庭互助、信贷、偿还、招聘、裁员、企业退出：均有确定性规则和事件/交易证据。
- 公共服务、媒体—居民信息边、信任变化、制度支持表达、社区迁移、出生、衰老和死亡：均进入状态、每日推进和测试。
- 外部资源冲击按上升—峰值—回落曲线运行；政策和资源干预按逻辑日与唯一 ID 生效。

### 时间、可靠性和可解释性

- 逻辑时间支持运行、暂停、继续、单步、调速、停止和同种子重置；墙钟只用于 durable timer 唤醒。
- 所有资金变动经过整数分账本、唯一交易 ID、余额检查和幂等表；逐日守恒测试覆盖 730 日。
- checkpoint 保存世界、PRNG、指标、事件序号、近期审计、冲击前基线和恢复状态；服务进程替换后继续结果一致。
- 每个重要动作生成稳定事件 ID、主体、原因、变化和关联交易；`trace` 与 `recent` 支持从居民或时间段回溯。
- 快照、摘要和 CSV 保存模型版本、构建版本、完整配置、种子、干预、逻辑时间、指标和自动异常提示。

### 观察与控制

- `/society/` 无登录表单；开放注册的本地实验节点会自动创建浏览器私有研究账户。普通账户在自己的 home channel 创建实验，不借用 root 身份。
- 控制台提供 A/B/C/D 创建选择、运行、暂停、继续、停止、单步、速度、重置、政策/资源干预和下载导出。
- 宏观视图提供人口、就业、产出轨迹、Gini、贫困、信任、支持、极化、企业、财政、税收、福利、出生、死亡、住房、债务、公共服务、关系聚类、孤立和恢复天数。
- 个体视图提供家庭、社区、工作、余额、收入、债务、住房、健康、信息触达、观点、策略、有限经历以及相关事件和交易。
- 四组同轴时间序列比较和 CLI 四组/多种子批量比较均已实现。

## 3. 验收标准证据

| 计划验收项 | 证据 |
|---|---|
| 新环境一条命令启动 | runbook 的 `go run ./cmd/atoll up ... --open-registration`；`GET /society/` 返回 200 |
| 运行至少十年 | 默认 A/B/C/D 均完成 3650 日，见 baseline |
| 暂停/继续/单步/停止 | `TestPauseManualStepResumeAndStop` 和 e2e 用户旅程 |
| 核心指标、主体和关键事件可见 | 控制台 DOM、actor `status/history/recent/trace` 响应测试 |
| 至少两组比较、JSON/CSV 导出 | 四组比较 UI、`compare` 与 `BrowserExport` 测试 |
| 规则和指标可解释 | spec 的结算顺序、事件表、指标定义；事件 `reason/changes/transaction_id` |
| 相同版本配置种子结果一致 | `TestSeededRunsAreExactlyReproducible` |
| 组间初始条件相同 | `TestVariantsShareInitialWorld` |
| 干预时间与幂等 | `TestScheduledInterventionAppliesOnce`、actor command-id 测试 |
| 货币守恒与交易幂等 | `TestDailySettlementConservesMoney`、ledger 测试 |
| 暂停不演化、死亡不再行动 | pause 测试、生命周期后续交易检查 |
| 重启不篡改历史 | checkpoint continuation、event identity、真实进程重启 e2e |
| 研究边界明确 | plan、baseline、runbook 均明确禁止现实政策外推 |

## 4. 本次正式实验与命令

```bash
go run ./cmd/society compare --days 3650 --seed 20260906 --out /tmp/atoll-society-v1.1-recovery-baseline
go run ./cmd/society sweep --days 3650 --seed-start 20260906 --seeds 10 --peak-pressure-bps 6000 --out /tmp/atoll-society-v1.1-recovery-shock-6000
go run ./cmd/society sweep --days 3650 --seed-start 20260906 --seeds 10 --peak-pressure-bps 10000 --out /tmp/atoll-society-v1.1-recovery-shock-10000
```

完整产物保留在上述临时目录；Git 中保存可复现命令和汇总报告，不保存数十 MB 的逐交易输出。

## 5. 验证边界

数字社会相关包、portal 挂载、架构层约束、错误分类、root 用户旅程和自动注册普通研究者旅程均单独通过。全仓短测试还暴露了一个与数字社会无依赖关系的既有 `TestCoderunnerHumanJourney` 设备子节点就绪失败；`check-data-plane-scope.sh` 也会因 `platform/internal/link/pty.go` 的既有注释包含保留词 `coord` 而失败。两者没有被计入数字社会验收通过，也没有在本交付中擅自改写无关子系统。
