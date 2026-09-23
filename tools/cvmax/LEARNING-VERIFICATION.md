# 学习主链实施与验证记录

2026-09-12，分支 `codex/job-application-autofill`。本次工作只修改 `tools/cvmax/`。这是一份中间交付记录，不是上线验收通过声明。

## 当前实证

> 2026-09-14 最新状态：严格 Recipe transition cursor、单写者观察、重启无重放、瞬态状态隔离和责任边界门禁已落地。动态结果页不再迫使 CvMax 审查全部站点专属字段：精确目标未命中时仍保留 transition timeout，但同一 Recipe Operation 的事实动作全部核验、无已知事实缺失/冲突且无后续流程动作即可按责任边界结束，并把真实精确结果登记为终止态。恢复入口先执行 Service 快路径，只有 adaptive 才唤醒 Agent。被替换的实现目录、旧 schema 自动升级和 Experience 数据库记录已删除。完整套件为 217 项，215 通过、2 项环境相关浏览器夹具跳过、0 失败；真实 DOM 合成浏览器夹具需另行显式启用。

- `npm test --prefix tools/cvmax`：114 项，113 通过、1 项浏览器夹具在普通命令中因未配置浏览器运行时跳过；没有失败。
- 对跳过项配置 Playwright 和独立无头 Chrome 后单独运行：1 项通过。使用本地合成 DOM，不连接用户 Chrome，也没有真实招聘写入。
- DOM 夹具覆盖自定义浮层、祖先遮罩、抽屉、局部展开、时间噪声、私有 input 值、iframe 不完整与节点截断。
- 服务级新增回归覆盖：没有假设禁止冷动作；前后证据和判定持久化；遗漏计划的字段仍需审查；已知 Recipe 落入新状态立即转学习、只操作一次、不自动扩展原经验。
- 公共发布负例确认：安装数达到阈值仍不能绕过编译/重放证据门禁；多余私有载荷拒绝进入公共投影。
- `git diff --check -- tools/cvmax` 通过；插件脚本语法检查通过。
- 通过原 Client 暂停 3 个遗留排队任务；通过 Atoll 公开安装接口更新已有 cvmax-agent/cvmax-service，无新增核心组件。

## 关键改动

私有 Application 下保存 Observation、LearningEpisode、KnowledgeCandidate。Hosted Agent 在冷动作前登记问题、假设、预期与观察版本；apply 仍执行原事实、动作权限及任务控制检查。已知 Recipe 检查实际结果分支，陌生结果回到解释阶段。候选编译在临时副本完成锚点重绑定，成功后一次写入，失败不破坏已有流程。

插件 0.2.0 使用证据协议 3：共享交互区域识别；保留未经页面分类丢弃的 rawFields；支持同任务/文档内有界局部补采。采集结果不完整时明确标记，不把无法看到等同于不存在。MAIN 激活先要求隔离环境校验成功，再检查目标遮挡。

恢复性导航不再直接产生“已验证”经验。新增 prelearn 是原 request_save/Client 的参数，不是第二套 Skill 或调度器。learning_report 中未实际发生的场景标记 not_tested，不能据此宣称预训练完成。

## 尚未通过的门禁

1. 插件重载后的 CvMax Client → Atoll → Hosted Agent → Service → Chrome 真实学习闭环。
2. 完整的可修正声明式解释映射、复杂多页简历覆盖、学习预算在真实推理耗时下的体验。
3. 预学习重置复测、不同简历/岗位与新分支的场景矩阵，公共发布的完整反例和深层隐私审查。
4. 同版插件跨站动态经验加载与性能样本；旧逻辑最终清理。

不把“观察到新内容”称为“申请成功”；不把“字段已计划”称为“简历覆盖完整”。真实任务不最终提交。本次尚无新版真实填写耗时可报告。

## 当前用户步骤

无。插件已由用户完成重载；最新责任边界与恢复快路径修改均在 Service/Client，不需要再次重载插件。

## 2026-09-12 首次 v3 字节真实尝试

任务 `414f86b5-7f3d-4190-8fcf-83340f0b1a2e`，原 Client 的 prelearn 模式，已保存测试简历 revision 4。详情页链接为 `https://jobs.bytedance.com/campus/position/7613237638787270917/detail?spread=DWA3PQP`。

- 正式链路成功打开详情页；可见进度约 2.256 秒。初次交接出现 agent_session_reset_failed，单独通过同一 Atoll 接口重试 agent.new 成功；原始失败原因尚未定论，没有据此修改 Atoll 核心。
- 恢复同一任务后，观察器把 1512×80 的固定导航栏当作 blocking dialog，导致标准模型 controls 为空。原始证据保留了屏幕下方“投递”按钮。Agent 提议不存在的 scroll_into_view 动作后任务暂停。
- 本次真实页面点击/填写动作 0，最终提交 0；没有形成通过验收的 Recipe。不能报告真实填写完成或复用速度。
- 修复：证据层区分固定导航区域与阻断交互区域；保留前者原始证据但不作为 activeSurface。学习提议提前拒绝未支持动作，避免产生无效 pending episode；Agent 指令明确现有执行原语会处理目标滚入视口。
- 插件版本 0.2.1。普通套件 115 项：114 通过、1 浏览器夹具按运行时配置跳过；单独真实无头 Chrome 夹具通过，新增完整 content.js 观察断言，确认无 dialog、formStage=detail、投递入口 safe。diff 检查通过。
- 当前仍暂停同一真实任务；等待用户重载 0.2.1 后复测。完整学习架构没有因这些检查而被标记开发完成。

## 2026-09-13 剩余开发集中完成

此节更新前面的中间状态：应用层代码和可自主完成的本地验证已完成；真实站点验收仍待用户重载后进行。没有重新启动真实招聘任务，没有发布新的真实网站经验，也没有修改 Atoll 核心。

完整命令（本机 Playwright 运行时通过环境变量注入）：`npm test --prefix tools/cvmax`。2026-09-13 删除错误身份门槛并修正法律同意分支后的完整回归为 **131 项通过、0 失败、0 跳过**。新增检查覆盖：登录邮箱与简历邮箱相互独立、所选 ResumeFact 覆盖对应的预填联系字段、无关页面答案仍受保护、身份冲突不能再创建 Question、旧身份等待状态自动删除；阻断式法律同意进入一次人工交接，最终提交阶段的同意项只进入人工清单，没有该门槛的网站不进入分支。测试套件时间不代表真实岗位填写速度。唯一 Skill 通过官方 quick_validate.py；git diff 空白检查通过。

新增完成的工程能力：

- `interpretation-rules.mjs`：从实际 rawFields/interactions 绑定受约束的声明式映射。事实不存在、目标重复、敏感字段、提交/支付等受保护控件不可映射为可执行目标。显式修正保留历史，暂停相关旧经验；跨发布的冲突不会静默覆盖。
- `page-model.mjs`：解释映射与原有分区映射经同一数据通路复用；公共定位文本使用摘要。语义修正不新增幽灵 Coverage 项。
- `verification.mjs`：运行时 ref/文档变化不作为成功，错误目标区域和未解释落点不作为已验证转换。原生解析、填写值与结构转换分别检查。
- `recipe-compiler.mjs`：临时副本编译、原页面重绑定、目标移除反例，全部通过才写本机 Recipe。公共准入要求已有终态、独立重放、不同简历变体、至少两次 prelearn 运行和反例证据。数量统计不替代这些条件。
- 公共投影检查嵌套状态、锚点、验证元数据和终态的字段白名单；去掉私人运行和变体标识，保留计数。字段标签、分组、页面路由和定位键转为摘要。撤销 release 清理持久缓存并暂停相同的本地边。
- Hosted Agent 的冷动作必须提议合法原语；Client 交接失败返回原任务 ID 并暂停，不留下假运行状态。迟到回执不能复活停止的任务。
- 预学习沿用一个 Skill 和 Client 的 learningMode 参数。learning_report 区分 observed/not_tested，并返回实际发布资格。
- 删除恢复性导航直接产生 Recipe 的路径及其 pendingRecipeNavigation 状态；运行中的轨迹只经独立验证和候选编译进入 Recipe，不保留第二执行路径。

浏览器集成实证：隔离的合成招聘页运行真实 evidence.js/content-logic.js/content.js，连接 CvMaxService 的测试传输适配器，完成解释→假设→真实 input 写入→回读→终止；重载合成页并换另一份合成简历，命中 Recipe、写入第二份资料并达到发布证据门槛。该测试未冒充 Atoll Hosted Agent 或真实 Chrome 扩展传输；两者的现场链路仍需真实验收。

剩余验收范围：同一最终版本在字节、国机及后续授权站点的登录、详情入口、原生解析、重复经历、复杂弹窗和跨 Tab 路径；真实性能样本；真实公共库发布/跨安装复用。未见过的浏览器能力可能明确报告 unsupported，不能承诺所有网站自动填写成功。暂不扩大到 Web、商业账号或计费。

### 集中部署后的外部阻塞

2026-09-13：Atoll 的 CvMax Agent/Service 已通过原安装接口集中更新，doctor 返回 available；真实任务均为 paused/finished/cancelled。

MySQL 链路随后恢复，`http://127.0.0.1:8791/health` 返回 Registry ready、数据库 `cvmax`、应用账号 `cvmax_app@%`；Atoll doctor 同时返回 Service、Agent、Browser 和 publicRecipe 可用。此前的 read ECONNRESET 记录保留为外部故障证据，但已不再阻塞验收。

## 2026-09-13 责任边界硬规则审计

- 字节任务 `e6a70df3-57c5-42e5-a0ec-9312940eaec9` 使用 ResumeFact `7732f4c8-2df9-4de8-aaa4-f37ddcbf94ff` revision 4，经正式链路得到 `finished / partial`、revision 85。持久化原生解析证据与 Coverage 审计允许服务在浏览器不可用时直接核验收口；`run_recipe` 总调用约 172ms、服务内约 148ms、浏览器动作 0，最终提交仍被禁止。该证据只证明字节原生解析责任边界。
- 审计发现自主填写路径仍依赖调用方主动 finish，且 Coverage 中已有确定 semanticId 的字段可被错误降级为 `no_resume_fact`。Service 现统一使用自主完成门禁：Coverage 全部审完、确定映射不可降级、所有映射事实回读正确、已计划事实无遗漏、可安全继续的流程节点已耗尽；网站专属问题不扩张责任边界。
- `coverage_review` 在门禁满足时直接结束；`run_adaptive_plan` 在最后一批事实写入并回读后直接结束，不再多等一个 Agent 决策。多岗位中间结果继续交给 CvMax 处理下一岗位，只有最后一个岗位把进度标为终态。
- 进度投影统一为“简历内容处理完成”，并明确“页面其余内容由你处理”；不再用“页面全部填完”的措辞。最终提交许可恒为 false。
- 学习预算现在只累计已接受 LearningEpisode 从提出到结算的实验时间；Agent 排队、模型推理、恢复等待不再在首个假设前耗尽 30 秒实验预算。恢复同时清除历史 pause 控制态，避免已重新排队的任务被旧门禁误判为仍暂停。
- 岗位详情页存在唯一安全、非提交且可命中的投递入口时，`run_recipe` 由 Service 直接创建、执行并验证一个受控 LearningEpisode，不等待 Agent 对显然动作做分钟级分析；遮挡的规范节点只允许重绑定到恰好一个同锚点、已由插件标为安全的交互。
- 最新普通回归：155 项，153 通过、2 项环境相关浏览器夹具跳过、0 失败；`git diff --check -- tools/cvmax` 通过。新增集成检查覆盖自主填写自动收口、网站专属必填留给用户、已知映射不可隐藏、终态进度文案和责任人，以及学习预算、恢复门禁、唯一投递入口的受控学习、Recipe 多步连续执行、transition 跨服务重启恢复，以及原生解析在未完成整页 Coverage 标注时直接核验正式关键事实。

## 2026-09-13 国机无原生解析入口回归

- 正式链路使用 CvMax Client → Atoll → CvMax Agent/Service → Chrome 扩展，没有使用 CUA 或 Playwright 代填。最终样本任务 `90ae03c0-ff41-49c4-abeb-3c32af36fd3c` 在岗位详情页创建后 2.806 秒发出首个动作，页面打开到可见为 472ms；没有再等待模型进行分钟级入口分析。
- Service 创建了 1 个受控 LearningEpisode，实验活跃时间 337ms，并执行 1 个非提交动作。扩展在动作瞬间返回 `interaction_target_occluded`，任务没有进入申请表，因此没有伪称简历处理完成；任务随后安全取消，最终提交许可始终为 false。
- 该结果验证了入口低延迟机制和失败边界，但没有验证国机自主填写收口。继续真实验收需要增强 Chrome 动作原语在执行瞬间对同锚点可见目标的安全重定位；这是插件能力变化，服务热更新不能解决，必须在修改后重载一次插件。

## 2026-09-13 Recipe transition 执行器修正

- 字节复验任务 `6920f1a2-9aa6-4681-8a62-4a39782451a7` 的首个 Recipe 上传动作已成功取得浏览器回执，但 Service 随即观察到上传后的瞬时页面状态 `a6c111…`，将其误报为 `recipe_unexpected_result` 并交给 Agent。约 7 秒后页面自然到达该 transition 已声明的目标状态 `9d65f…`。因此异常不在 Recipe 数据，也不在动作本身，而在执行器没有持有 transition 的后置条件等待阶段。
- Service 现在把 `expectedStates`、等待起点和 matched/timeout 结果保存在 operation 中。回执验证后，`run_recipe` 在有界时间内只读观察，命中明确目标后立即执行下一 transition；等待期间通用 observe 不得把 operation 降级为 unknown，也不得从瞬时快照编译新 Recipe。只有超时或真实未知分支才回到自适应流程。
- 完成投影同时拆分 planned actions 与 executed actions：未来 Recipe 动作可以声明预期覆盖，但不能进入 processed。这样多步 Recipe 不会在第一步后提前结束。
- 新增两项服务集成回归：一项在第一步后连续注入三个瞬时状态，确认第二步仍由同一次 `run_recipe` 紧接执行且瞬时状态没有进入 Recipe；另一项在回执后重启 Service，确认从持久化后置条件游标恢复且浏览器写入仍只有一次。
- 上述修复尚未取得修复后的真实站点证据；部署 Service 后需重新执行同一字节路径。它不修改插件协议、动作原语或进度 UI，因此不需要插件重载。

同日首次部署复测任务 `74ab8fde-4dbe-4f90-aeaa-b6f570d58b84` 证明 transition 修复生效：原生解析动作的后置条件于 123ms 内命中明确状态，没有出现 `recipe_unexpected_result`；任务创建后约 6.295 秒首动作，原生解析产生 51 项变化。但解析后的 67 条 Coverage 尚为 `unreviewed`，旧 `nativeParseBoundary` 只消费 `mapped` 行，导致姓名、手机、邮箱和学校均正确时仍返回 `native_parse_insufficient_evidence` 并交给 Agent。这是另一处 Service 责任边界缺口，不是 Recipe transition 异常。

Service 现从正式 ResumeFact ID（含 `name`、`mobile`、`email`、`education_1_school` 及 candidate/dot 形式）与明确字段身份建立关键事实核验投影；只接受固定语义/标签、字段类型、教育分组和唯一序号，不使用相似度。整页 Coverage 仍用于无原生解析的自主填写，网站原生解析成功路径不再等待 Agent 逐项标注。新增真实 ATS 形状的服务回归确认 4 个关键事实可直接核验并零动作结束，冲突仍阻止收口。

## 2026-09-14 transition 终态锁与恢复回归

- 真实冷路径暴露出一个通用执行器问题：Operation 已是 `verified`，但 Recipe postcondition 为 `timeout` 时，apply/yield 门禁仍将它当成活动 transition，因而错误拒绝下一个动作。这不是某个网站的 Recipe 数据异常，而是终态语义没有在所有门禁中统一。
- Service 现只用 `recipeTransitionPending` 判定锁所有权；`matched / timeout / cancelled` 均为已结算终态，不能阻止后续 apply、yield 或安全 resume。取消的 LearningEpisode 同时不再占用假设次数。
- 新增针对性回归后，完整套件为 203 项：201 通过、2 项环境夹具跳过、0 失败。真实字节任务 `c81056cf-7bb9-4198-b6f3-d783ba8f75c3` 从 revision 238 恢复，20.839 秒后以 revision 239、`finished / partial`收口，新增浏览器动作 0、最终提交许可为 false。恢复直接复用已验证的网站原生解析证据，核验关键事实后将页面其余内容交给用户。

修复部署后的新任务 `ad1b2be7-8308-497b-844b-ffdb898259be` 在同一字节申请页于 15.410 秒达到 `finished / partial`；Client 墙钟 16.966 秒，Service `run_recipe` 15.318 秒，首动作 6.410 秒，2 个浏览器动作，原生解析改变 51 项，Agent 未接管，`missing=[]`，仅保留隐私政策人工步骤，最终提交许可为 false。与修复前 229.652 秒的同页样本相比，分钟级 Agent Coverage 等待已经消失。

该真实结果同时暴露一个结果投影错误：`observe` 内完成原生解析责任边界时，`run_recipe` 曾把任意 finished 误写为 `path=unavailable/outcome=skipped`，Client 因实际 outcome 覆盖而显示 `unavailable / partial`。执行器现从已完成 Application 读取真实 outcome：只有 `skipped` 才是 unavailable，原生解析收口为 recipe/partial；`finish_native_parse` 同步写入 executionPath。服务回归覆盖 running 状态下 observe 内收口的分类。本项未增加页面动作，不再重复真实填写。

## 2026-09-14 加载所有权、解析因果与终止状态复验

- 任务 `2b904844-3d46-43cc-a1cb-27720e1f0fd2` 中，入口动作已成功进入申请页，但连续 10 次观察只有加载遮罩；旧实现让普通加载恢复与活动 Recipe transition 分别计时，transition 先超时并错误唤醒 Agent。Service 现让当前 transition 持有加载解释权，在同一游标内最多执行一次受控刷新，刷新后继续等待原目标；仍持续加载则直接以可证明的加载失败结束，不产生另一套工作流。
- 修复后任务 `98d70177-7beb-4053-98d9-b3915afc18cd` 顺序完成入口与上传，网站未返回可核验的解析变化，随后自主填写 8 个事实并以 `finished / partial` 结束。审计发现上传 Operation 仍会消费后续字段 Operation 产生的观察，导致自主写入被误归因为网站解析。Service 现以首个后续字段 Operation 的 `createdAt` 为硬截止点；截止点后的观察既不能新增到该上传尝试，也不能被已有持久化记录用于解析证明。
- 因果修复后的任务 `e56ab425-9157-48a1-a7a0-5219bc217b27` 在上传后保留 16 次 `parsedCount=0` 的观察，正确显示“网站未产生可核验的简历解析结果，已转为 CvMax 自主填写”，最终提示也只声明安全映射事实已填写核验，没有再声称原生解析成功。任务最终 `finished / partial`，10 个浏览器动作，最终提交许可为 false。
- 同一任务仍耗时 185.043 秒并重复执行 8 个字段动作。根因不是学习未记录：前一轮填写边已验证；而是终止节点把 `predecessorKind=fill` 纳入身份，导致由 `upload` 到达的相同已核验页面不能命中终止。终止状态现只使用当前页面的规范结构与 Coverage 精确匹配，忽略字段 populated、控件 active 和到达动作；普通非终止状态仍保留 predecessor 以区分弹窗与迁移上下文。匹配始终是完整规范描述符相等，不使用相似度。
- 随后的编译审计确认上一轮 8 字段候选虽已通过效果验证，但没有进入 Recipe。通用 `matchAnchor` 先用 semanticId 将两个同名“起止时间”字段筛成单项，又用原结构 ordinal=1 对单项结果二次取下标，错误返回空绑定，导致整批候选被拒绝。绑定器现直接要求每个候选的完整锚点（包含原 peer ordinal）精确相等；不会缩小候选集合后再次解释 ordinal。
- Service 重启会重新编译仍处于 verified 的持久候选，但只接受最后一次 Operation、当前 observationId、相同目标 documentEpoch 和相同 verification digest 全部一致的记录。该过程不调用浏览器、不重放动作；真实候选已生成 8 字段 `reusable` transition，包含姓名、手机、邮箱和五项教育事实。
- 修复后的两次真实任务 `58ad7d51-ec91-4686-abca-e7acf086524f` 与 `1a1b19c7-61d7-4ee9-a971-6547f7ea87a3` 均在入口后持续停留于空加载遮罩，一次受控刷新后仍未恢复，分别以 46.695 秒和 39.226 秒安全结束为 `skipped / application_loading_failed`。两次均只有入口与刷新 2 个动作，上传/填写为 0，Agent 未接管，最终提交许可为 false。因此它们验证了弱网失败边界，但不能作为完整 Recipe 填写速度通过证据。
- 为排除单岗位故障，任务 `e894ca9f-ea32-4e63-9f85-18f38ce421b9` 改用岗位 `7683366345501608245`；结果仍在入口后的空加载遮罩持续不恢复，服务内 45.560 秒后以相同的 `application_loading_failed` 结束。动作仍只有入口与一次刷新，上传/填写为 0。两个岗位、三个独立任务的一致证据表明当前真实验收阻塞在招聘站申请页可用性，而不是岗位级 Recipe 分支。
- 字节任务 `d95de7af-edf2-4a80-87dc-8c72a03f72e3` 经正式链路执行上传 1 次和已知 Recipe 事实动作 8 次。动态结果结构未命中旧精确目标，原 transition 正确保留 `timeout`；Service 使用实时回读和同文档原子动作回执核验 8 项事实，没有把站点专属字段扩张为 CvMax 责任。修复部署后任务在 revision 73 达到 `finished / partial`，浏览器动作仍为 9、无重复动作，终态提示页面其余内容由用户处理，最终提交许可为 false。
- 真实回归同时暴露插件回执的正式结构是 `{action, evidence}`：动作身份在 `action`，接受结果、字段引用、写入值和 document epoch 在 `evidence`。Service 现联合校验两部分；控件仍可见时以实时值为权威，只有控件离开可观察 DOM 时才接受同文档、同动作、同字段、同写入值的回执，跨文档或冲突值均拒绝。
- Client 的恢复入口不再固定唤醒 Hosted Agent：`resume` 完成版本化状态变更后先调用 Service `run_recipe`，只有返回 adaptive 才委托 Agent。已知 Recipe、责任边界终止和 deferred 状态均不会产生 Agent 排队。
- 最新完整回归为 217 项：215 通过、2 项环境相关浏览器夹具跳过、0 失败；`git diff --check -- tools/cvmax` 通过。

## 2026-09-20 完整业务责任边界重建

- 历史“原生解析后关键事实正确即可结束”和“Recipe 终态即可收口”的证据不再满足当前产品口径。它们证明局部技术路径可用，但不能证明项目经历、校园经历、附件及其他冻结事实已完整处理。
- 任务现在从用户补充资料 revision、本次唯一简历 revision、任务答案和附件建立不可缩减的 `ApplicationPlan`，并用 `ObligationLedger` 保存逐项结论。Recipe 只负责页面操作顺序，不能定义或缩小业务范围。
- 已删除 `finish_native_parse`、关键事实终态 bypass 和 Recipe 结果专用完成分支；原生解析、Recipe、自适应填写及重启恢复全部经过统一完成闸门。只读 `observe` 不得擅自结束任务。
- 新增回归覆盖：附件与全部事实独立建计划、计划只能单调扩展、多简历隔离、用户补充资料复用、服务重启后复用持久证据、项目和校园多记录动态添加及回读、完整区块无字段证据，以及存在 add 能力时禁止声称 `site_no_field`。
- 本轮正式现场验收固定为 Microsoft Edge、TC-001 京东、`林知远_人工智能算法工程师.pdf`。必须先冷学习完整流程，再从零创建独立任务重放；第二次不得启动 Agent，且两次最终提交调用均为零。完成前不继续 TC-002。
