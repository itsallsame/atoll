# CvMax 真实页面验收记录

## 2026-09-13：字节原生解析责任边界快速复验

- Task：`e6a70df3-57c5-42e5-a0ec-9312940eaec9`；ResumeFact：`7732f4c8-2df9-4de8-aaa4-f37ddcbf94ff` revision 4。
- 结果：before state 为 `finished`，最终 `finished / partial`、revision 85；最终提交许可为 false。
- 持久化的 nativeParse 结果与 Coverage 审计已足以证明关键简历事实正确，因此服务重启清理私有页面快照后，`run_recipe` 没有重新访问浏览器。总调用约 172ms、服务内约 148ms、浏览器 actionCount 为 0。
- 用户提示：“网站原生简历解析成功，关键简历事实已核验；CvMax 已结束自动处理，页面其余内容请手动处理。”
- 该样本证明字节原生解析成功后的责任边界，不证明无原生解析站点或跨站能力；后者仍需单独真实回归。

## 2026-09-10：通用交互执行器与热更新架构回归

- Chrome 扩展版本：`0.1.88`；实际页面快照应回传该版本。
- 从字节岗位详情页进入申请表，路径为 `detail → /campus/resume/:id/apply`，未由外部浏览器自动化代替插件操作。
- 已知表单精确命中经验，网站原生解析成功，16 个有简历来源的动作全部回读核验；结果为 `partial`，仅留下隐私政策人工项。
- 页面打开到终止 20.838 秒，首次可见进度 2.925 秒，Agent 未接管；最终提交调用为零。
- 自动化回归证明 interaction release 可由同一 Service 在下一次观察时获取并执行，无需重启 Service；扩展源码门禁确认不含公司域名及简历替换/上传成功等站点业务语义。

日期：2026-09-06
目标：字节跳动校园招聘申请页
边界：填写并核验，不勾选隐私同意，不执行最终提交。

## 有效执行链

本次有效证据全部来自 `CvMax Skill/Client → Atoll 应用服务 → CvMax Chrome 插件 → 招聘页面`。开发期间曾使用浏览器自动化产生的填写结果已作废，本次从新的空白申请页重新执行，没有使用该路径。

## 已核验结果

- 首轮完整填写使用插件 v0.1.21；字段级进度与后续正式入口复验使用当前 v0.1.22。
- 原生解析优先：新申请页首次观察没有解析入口；插件只上传 PDF 后，页面才展示“解析并覆盖”。插件复用已上传附件触发网站解析，没有二次上传。
- 原生解析核验：网站解析产生 57 个字段变化，展开了 2 段实习、6 段项目及语言能力；CvMax 通过解析前后表单摘要确认解析真实生效。
- 基础资料：姓名、手机号、邮箱、教育起止时间、学历类型、学校、学历、专业均在新观察中保留。
- 自定义下拉：学历类型为“统招全日制”，学历为“本科”；虚拟列表节点选择后均由目标字段回读确认。
- 简历附件：页面显示 `resume.pdf` 和更新时间；插件核对了附件大小与 SHA-256，验收记录不保存可关联原文件的摘要值。
- 实习经历：2 条，共 10 个核心字段，最终观察无空值；网站改写的两段描述已按授权事实纠正并回读。
- 项目经历：6 条，共 30 个核心字段，最终观察无空值；网站改写的项目名称、角色和六段描述已按授权事实纠正并回读。独立项目链接没有单独的授权事实，因此保持为空。
- 原生解析漏填的学历类型已由 CvMax 补为“统招全日制”；网站下拉回执超时后通过只读 reconcile 确认值已保留，没有重复执行。
- 页面错误列表为空；所有最后执行的操作状态为 `verified`。
- 最终提交调用为零，隐私政策复选框保持未选。

## 缺项与终止状态

任务以 `partial` 正常结束。内推码、个人证件、期望工作地点、学院、实验室、领域方向、导师、独立项目链接、招聘信息来源及其他渠道均未被页面证明为必填，因此允许保持空白，不进入 `missing`，也不创建用户等待问题。网站原生解析填写的语言和熟练度没有进入本次授权事实快照，保持原值且不作为 CvMax 核验成果。

个人证件虽然属于敏感字段，但当前页面未证明其必填，因此不要求用户补充。隐私政策属于最终法律同意，服务将其归类为 `manual:f23` 人工步骤，并在不勾选的情况下结束任务。页面 `canAdvance=false`，因此本轮交付为 `partial`，不进入提交阶段。

## 本轮修复并回归的应用层问题

- 字节自定义下拉的显示值读取与虚拟列表节点选择。
- 下拉字段滚动定位、异步渲染等待和唯一语义匹配。
- 动态添加操作使用添加前后的分组字段数量核验。
- 页面重渲染后按稳定分组恢复区块名称。
- DOM 引用变化后，以语义序号或已保留值复核重复的起止时间字段。
- 旧 `add` 回执只在唯一安全分组出现重复行结构时恢复，保持防重放边界。
- 支持“首次上传后才出现原生解析入口”，并复用网站已保留的附件。
- 将站点初始单选/复选状态识别为原生解析可更新的默认值，同时继续保护文本草稿和人工同意项。
- 支持附件区域内由非标准可点击元素实现的精确“解析并覆盖”动作，并排除提交、投递和法律同意动作。
- 终止判断改为服务端最终页面快照：只允许已证明必填的空字段创建 Question；选填、必填性未知、已由对应经历满足的否定选项和最终人工同意均不阻塞。
- 安装声明加入应用运行时代码摘要；服务源代码变化会触发 Atoll 原生成员升级和重启，相同版本重复安装保持无操作。

## 经验复用与正式入口复验

- 两个不同字节岗位的完成/部分完成 run 已归入同一页面家族：`jobs.bytedance.com + /campus/resume/:id/apply + apply`；岗位 ID 与跟踪参数没有造成经验分裂。
- 当前经验存储包含 39 个字段策略，其中 13 个经过至少两个不同 run 核验后成为 reusable，最高独立成功 run 数为 4；存储检查未发现简历事实原值或自由文本反馈。
- 后续由正式 `cvmax submit` 发起的三次链路均从岗位详情链接直接打开已验证的申请表路由，页面经验状态为 `exact`、覆盖率为 1，并返回 2 个适用的经验建议。
- 页面同时具备网站原生解析能力，因此执行顺序仍是原生解析优先。三次解析均由插件触发并回读到 57 个字段变化；经验没有越过该策略与网站解析竞速，而是保留给解析后的核对和补填。
- Atoll 节点正常重启时，已发送但结果未知的动作进入只读 reconcile；已验证动作与确定未派发动作安全排队接续。真实任务恢复后没有重复页面写入。
- 旧流程曾错误等待用户填写页面未标必填的个人证件号码；该任务已停止并作废。
- 修复后两次正式链路均未进入 `waiting_user`。最终复验从申请页创建到终态约 112 秒，原生解析改变 57 个字段；任务自动进入 `finished / partial`，`missing=[]`，仅报告 `unsupported=["manual:f23"]`，下一步负责人为用户，最终提交调用仍为零。

最新自动化回归数量以 `npm test` 输出为准；JavaScript 语法检查和 `git diff --check` 纳入最终交付检查。

## v0.1.24 区块可视化验收状态

插件 v0.1.23 已实现岗位页打开后的即时准备状态，以及执行中的大区块名称、页面高亮框、动态连接线、滚动跟随和终态撤销。服务进度协议同步增加有界的 `sectionLabel` 与当前文档临时 `targetRef`，不携带事实值。

本轮真实链路未形成运行证据：同一 `clientRequestId` 的委托多次取得 `atoll_message_accepted`，但 `cvmax.status` 始终没有业务 task。日志先显示 Hosted Agent 的 Codex thread history projection 序号不一致（期望 312、实际 311）；通过公开 `system.member.delete/create` 重建 CvMax Agent 后该错误停止，但新成员仍未消费 `agent.ask`。Service 与浏览器连接正常，且没有浏览器 `open` 或页面写入发生。

经用户授权，随后完整重启共享 Atoll/Codex 运行时。重启后 `cvmax doctor` 确认 Atoll、私有频道、Hosted Agent、Service 和浏览器插件均在线。频道持久记录进一步证明，新 Agent 已消费 20:34:45 的 `agent.ask`，随后调用 `cvmax.status` 和 `cvmax.open`，并于 20:35:15 正常结束 Agent turn。此前在异步委托结束前查询状态，且运行日志不记录正常 turn，导致误判为核心调度仍阻塞。

实际终止点是插件 v0.1.23 的 service worker：新增的打开后即时进度逻辑调用了 `normalizeOrigin`，但 `background.js` 漏掉对应导入，`cvmax.open` 因 `normalizeOrigin is not defined` 返回失败。v0.1.24 已补齐导入，并增加回归断言；77 项自动化测试、JavaScript 语法检查和 diff 检查通过。

按照产品架构边界，本轮没有绕过 Hosted Agent 直调 Service，也没有修改 Atoll/Codex 核心代码。真实页面上的区块定位、连接线位置及打开到首帧时间仍待插件 v0.1.24 重载后复验，不能以自动化测试冒充通过。

### v0.1.24 重载后真实复验

用户重载插件后，通过正式 `CvMax Client → Hosted Agent → Service → Chrome 插件` 链路重新执行同一岗位，未使用直接浏览器控制。请求于 20:56:14 被 Agent 接管；`cvmax.open` 于 20:58:38 发出、20:58:41 成功，随后完成 create、claim、observe、apply 与 finish。任务于 21:00:55 进入 `finished / partial`，Agent 于 21:01:03 终止。

原生简历解析成功并产生 57 个页面字段变化；自动执行 3 个动作，经验为 exact match，复用 1 个建议动作。最终缺失事实为 0，仅保留一个人工步骤 `manual:f23`，没有进入等待用户状态，也没有点击最终提交。插件 v0.1.24 的 `normalizeOrigin` 修复得到真实链路验证。

本轮墙钟时间仍不合格：委托到 Agent 终止约 4 分 49 秒，`cvmax.open` 发出到 finish 约 2 分 17 秒；任务记录的页面活跃预算约 59 秒。主要额外等待来自模型响应流断线重试，以及 Agent 首次把附件 MIME 字段误写为 `m` 后再次调用 create。区块进度事件已随真实动作派发，但视觉位置和首帧体感仍需用户侧观察确认。

## 2026-09-06 已知页面高速路径

在相同字节申请页家族和同一份已授权测试简历上，应用服务新增 `run_recipe`：一次工具调用内完成任务认领、上传、网站原生解析、exact 经验纠错、回读与终止。中间动作继续经过原有事实来源、已有值、预算、租约、页面版本与 no-final-submit 门禁。服务同时识别上传后自动解析，并把已经保留的同一附件作为 preserved，避免经验重复上传。

去掉动作命令已携带进度之后的重复进度确认往返，正式链路从任务创建到终态由约 100 秒降至 26.059 秒；页面活跃预算为 21.230 秒。最终一次请求暂存协议复验为 27.302 秒、页面活跃预算 21.403 秒，完成上传、原生解析和 12 项经验纠错；结果为 `partial`、`missing=[]`、仅 `unsupported=["manual:f23"]`，最终提交调用为零。

Client 随后改为先用 `request_save` 在应用服务保存授权范围，只向 Hosted Agent 发送 clientRequestId；Agent 用 `request_get` 读取无事实值范围，`create` 从暂存记录冻结资料。89 项自动化测试通过，正式链路证明协议可用且事实没有进入委托消息。但这没有缩短 Agent 首次接单：最终样本从 Client 接收到业务任务创建为 97.224 秒，端到端为 124.526 秒。该等待发生在页面任务创建之前，继续压缩提示词无效；若要求把端到端首开压到 20 秒内，需要改变“Hosted Codex Agent 统一决策”的现有边界或优化 Atoll 核心调度，因此本应用层不继续绕过。

## 2026-09-09 完整页面观察与简历覆盖修复

回归发现插件 v0.1.49 的字段暴露判断只保留当前视口内的控件。字节申请页下方的教育、实习、项目和语言区域虽然已在 DOM 中，仍被错误排除；服务因此只看见附件、姓名、手机、邮箱、证件和隐私同意六项，并把上传后挂载的空字段误判为网站自动解析结果。由这个错误结构生成的本地经验版本已按 schema fingerprint 单独 suspended，完整字节页面家族和公共发布经验保持 reusable。

插件 v0.1.50 对视口外但仍在布局树中的控件直接纳入观察，实际页面字段数由 6 增至 68，并识别教育、实习、项目和语言四类重复区域。服务同时只把新增的非空值或选中状态计入原生解析变化；上传后只新增空控件时会继续寻找并执行网站“解析并覆盖”，不会提前进入补填或完成。

完成门增加 ResumeFact 投影核验：对同一页面家族历史上已经证明可映射、且本次简历实际提供的 factId，必须全部获得页面回读证据或等值保留证据。页面自己的选填字段和法律同意仍不阻塞。本次任务 `fee57747-9845-42dd-bcdf-53d525d4d75c` 使用简历 revision 4，冻结 84 条事实；从岗位详情自动进入申请页，上传及原生解析均由 CvMax 插件执行。原生解析产生 3 个即时可核验变化，随后经验路径在 13.798 秒内纠正并核验 11 项，最终投影为 14/14、经验 exact、missing 为空。任务以 `finished / partial` 结束，仅保留隐私政策人工步骤，最终提交调用为零。

自动化回归为 145 项全部通过，覆盖空 DOM 变化不算解析成功、上传后显露解析入口、视口外字段观察、schema 版本隔离、完整简历投影阻止误完成，以及解析后首次出现字段的有界纠错。
## 2026-09-09 ByteDance deterministic fast-path regression

- Task: `c14e739d-a22e-465d-aeb5-35086012afe1`
- Entry: known ByteDance job-detail URL; CvMax used the learned Recipe entry transition, Chrome extension, native resume parser, and public Recipe. No ChatGPT browser control was used.
- Result: `partial` / finished; final submission remained disabled. Resume projection was 16/16 with zero missing facts. The only remaining responsibility is the user's page-specific review and consent.
- Timing: 15,648 ms from task creation on the detail URL to completion. Upload receipt to verified native parse was 2,387 ms; native parse to the first deterministic correction was 3,466 ms; all 13 corrections completed 2,100 ms later.
- Evidence: one verified start, one verified upload, one verified native parse with 51 changed fields, and 13 verified fills. The final Recipe match was exact and preserved 17 mappings.

## 2026-09-13 Sinomach no-native-parser entry regression

- The run used only `CvMax Client → Atoll → CvMax Agent/Service → Chrome extension` against the Sinomach campus job-detail page. It did not use CUA or Playwright for filling and never enabled final submission.
- Earlier attempts exposed two application-entry defects: the first learning proposal incorrectly inherited total Application active time and exhausted its 30-second experiment budget before any episode; a resumed task also retained a historical pause gate. The Service now budgets only accepted LearningEpisodes and clears the obsolete control gate on resume.
- A unique safe application entry is now handled as one deterministic, Service-controlled LearningEpisode. This preserves observe → hypothesis → action → verification while removing model deliberation from an obvious transition. A canonical entry that is occluded may only rebind to one exact exposed duplicate already represented by a safe plugin interaction.
- Final evidence task `90ae03c0-ff41-49c4-abeb-3c32af36fd3c` issued its first action 2,806ms after task creation; opening the page to visible took 472ms. It created one episode, used 337ms of learning-active time, and attempted one non-submit action.
- The extension rejected that action with `interaction_target_occluded` at dispatch time. The task never reached the application form, was safely cancelled, and produced no completion claim. This is evidence that long entry analysis has been removed, not evidence that Sinomach autonomous filling is accepted.
- Root cause: the isolated observer assigned the rebound target ordinal inside its actionable interaction set, while the MAIN-world executor rebuilt a broader merely-visible DOM set. Its ordinal `0` therefore selected the covered header duplicate and correctly failed the final hit test.
- Extension 0.2.2 removes the second definition: observation and MAIN-world execution share candidate filtering, normalized interaction identity, and ordinal resolution. Service-generated `start` anchors also derive ordinal from the actionable interaction set rather than the broader visual-control set. A synthetic Chrome regression with a covered first duplicate and clickable second duplicate resolves only the clickable target.
- A real Sinomach rerun remains required after one extension reload. A Service hot update cannot install this browser action-primitive change.

## 2026-09-13 ByteDance Recipe postcondition regression

- Task `6920f1a2-9aa6-4681-8a62-4a39782451a7` used the formal CvMax Client → Atoll → Agent/Service → Chrome-extension path. The first known Recipe action was issued about 6.009 seconds after task creation; the form was visible about 336ms before that action.
- The upload action was accepted, but `run_recipe` returned `recipe_unexpected_result` after observing a transient post-upload state. Channel evidence shows the page reached the transition's exact declared target state roughly seven seconds later. The Recipe and browser action were therefore valid; the deterministic executor had checked the state graph before the page had settled.
- Agent fallback later triggered native parsing and changed 50 fields, but the task had not reached an automatic terminal state after roughly 108 seconds. The run was stopped safely; final submission remained disabled. This is a failed performance/continuity acceptance sample and must not be counted as a successful Recipe replay.
- The Service executor has since been changed to persist and settle each Recipe transition's postcondition before selecting the next edge, including restart-safe continuation without redispatch. Local multi-step and restart integration tests pass. A new real-site run is required before this fix is accepted; no Chrome extension change or reload is involved.

### First run after the transition fix

- Task `74ab8fde-4dbe-4f90-aeaa-b6f570d58b84` reached its first action in 6,295ms. The Recipe-triggered native parser changed 51 fields, and its persisted postcondition matched the declared `f434…` state in 123ms. No `recipe_unexpected_result` occurred, so the original transition-continuity defect is verified fixed on the real page.
- The run then exposed a separate boundary defect: all 67 page fields were still `unreviewed`, while native-parse completion only inspected Coverage rows already marked `mapped`. It therefore handed a correct parsed form to the Agent instead of immediately applying the native-parse responsibility boundary.
- The Service now derives only critical native-parse checks from formal ResumeFact IDs and explicit field identities, independently of whole-page Coverage review. Local regression passes; this second fix still requires deployment and continuation of the same task before acceptance.

### Native-parse boundary retest

- Fresh task `ad1b2be7-8308-497b-844b-ffdb898259be` completed as `finished / partial` in 15,410ms from task creation; Client wall time was 16,966ms and Service `run_recipe` time was 15,318ms. The first action occurred at 6,410ms.
- The Chrome extension performed two actions, native parsing changed 51 fields, the Agent was not engaged, `missing=[]`, and the only remaining item was the user's privacy-policy step. Final submission remained disabled.
- The initial response mislabeled this valid `partial` completion as `path=unavailable`; the persisted task evidence was correct. The generic finished-result projection has been corrected so only a genuinely skipped application reports unavailable, and native-parse completion records `executionPath=recipe`. This classification change is covered locally and does not justify another real page write.

### Strict-state-machine retry and timing audit

- Task `1b476cf2-1c68-4fff-b815-d8a870ecf2bb` exposed a general ordering defect after native parsing: the ordinary observation path tried to apply the native-parse completion boundary while its owned Recipe transition was still `settling`. The completion guard correctly rejected that premature finish, but the transport surfaced only `service_request_failed`. The boundary now reports ineligible until the active transition is settled. After service restart, the same persisted cursor resumed and finished in 5,369ms with `actionCount=0`; no accepted action was replayed.
- A fresh, interference-free task `f1ec4063-9dd4-4ddb-b8d6-f15c98ec3a9d` completed `finished / partial` entirely on the Recipe path with three browser actions, no Agent takeover, 51 native-parser field changes, no missing resume facts, and final submission disabled. Task creation to completion was 23,608ms; open-to-visible was 2,633ms, first action 4,513ms, and form-ready 9,351ms.
- Timestamp audit found the dominant remaining avoidable delay after form readiness: the native parser itself required about 4.3s, while two redundant browser observations used about 3.6s to re-prove facts already present in one serialized snapshot. The Service now lets that one snapshot atomically complete action verification, exact transition matching, and native-parse responsibility-boundary termination. Initial deployment attempts were blocked by the execution environment's approval service; the following checkpoint records the later approved deployment and real timing.

### Post-optimization clean retry

- After explicit approval, only `cvmax-service` was restarted; the Chrome extension was not reloaded. Fresh task `dcf3a184-2e5f-40da-913b-f035a6d003ff` completed `finished / partial` in 21,586ms from task creation and 21,477ms from browser open. It used three Recipe actions, changed 51 fields through the native parser, did not engage the Agent, left only the privacy-policy manual step, and kept final submission disabled.
- Open-to-visible was 3,267ms, first action 5,843ms, and form-ready 10,642ms. Native parsing was verified at `12:10:10.661Z`; its Recipe transition was `matched` at `12:10:10.663Z`. The previous two-observation delay is therefore removed on the real page.
- The original Client call nevertheless received a generic `service_request_failed` after the terminal result had already been persisted. Client recovery previously recognized only errors whose message explicitly contained `outcome unknown`. It now treats the generic service failure receipt as an uncertain outcome, queries the same task, and returns the persisted result without creating a task or replaying an action. Reissuing the same `clientRequestId` returned `recipe / partial`, `elapsedMs=21477`, `actionCount=3`, and `recoveredAfterTimeout=true` in 621ms.

## 2026-09-23 Edge TC-001 开发探针（非正式通过）

- 开发探针任务 `eaf6cbb5-5554-4154-b17f-615805743d4b` 在 Microsoft Edge 京东校招简历页继续执行。网站原生 PDF 上传与解析已确认；六段项目和三段校园经历的已填内容在 Edge 页面上保留。冻结计划 85 项中 67 项已核实、1 项有操作证据的不支持例外、17 项仍待处理；未最终提交。
- 插件 0.2.42 在该页面的观察确认 `fieldInventoryComplete=true`，即页面节点数超过结构证据上限、存在无字段 Shadow Root 和不可见 iframe 时，仍可完整枚举可见字段。仅凭大型页面使 `evidence.complete=false` 不再阻止无字段审核；但旧分区观察及剩余事实仍需逐项重新取证。
- 两项项目结束日期的事实值是“至今”，网页字段是只读日期输入。Agent 的静态检查未发现对应选项，Service 拒绝了未经过动作验证的 `unsupported` 审核，因此这两项仍为 pending。通用“至今”日期选择器探测已通过合成 DOM 正反测试，插件源码升至 0.2.43；实站日期弹层尚未验证，不以模拟测试宣称京东支持或不支持该值。
- 完成闸门已进一步收紧：Coverage 上标为 `unsupported` 不再足以结清事实，必须关联语义失败的 Operation。全量自动化回归（含真实 DOM 夹具）为 300/300 通过，`git diff --check` 通过。尚无独立无 Agent 重放证据，TC-001 未通过。随后 Mac 锁屏阻断 Edge 诊断，等待用户手动解锁后继续；不绕过锁屏、不写入替代日期。
- 后续静态审计发现一次 `navigation:其他信息 → 自我评价` 的结构映射，以及早期插件 `0.2.29` 对科研描述一次 `operation_no_effect` 就结为不支持。两者不能证明对应事实已处理：Service 现要求无字段结论有同名或明确通用别名的区块身份，并在下一次观察时重开旧版或非确定能力失败的 unsupported 项。代码回归已通过，Edge 任务尚未重新观察，因此上述 67/85、17 pending 仍是上次现场快照，不是新规则下的最终计数。
- 插件源码 0.2.44 增加只读“请选择”控件的精确选项探测（针对籍贯等通用选择器），与“至今”日期探测均在合成浏览器中验证；304/304 项全量回归通过，尚未重载到 Edge。Mac 仍锁屏，Computer Use 返回锁屏或窗口不可用，未以其他浏览器或绕过锁屏方式代替实站测试。
- 后续完成闸门审计移除了 `finish` 调用方可自报 `fact:*` 不支持并直接改写责任账本的旧路径；“网站无字段”例外绑定区块与页面证据，字段、可添加控件、文档或插件版本变化时重新变为待处理。305/305 项全量自动化回归通过。此结果仅是代码回归，不代表 Edge 上 TC-001 冷学习或独立重放通过。
- 2026-09-23 Edge 解锁后，无须刷新京东页面即可重新注入插件 0.2.44/0.2.45；站点刷新提示“所做更改可能未保存”，已取消，保留当前表单。0.2.44 观察将旧 `research_desc` 不支持结论重开为待办：67/85 已核实、18 待办、0 有效例外。0.2.45 实站确认籍贯是省市级联选择器，旧版首项选择未回读；插件新增唯一重复地名路径选择并通过合成真实 DOM 测试，但旧探针后续因 600 秒活跃预算耗尽，在派发前停止，故该能力尚无京东页成功回读证据。两个项目“至今”均返回 `option_not_found:ongoing_date`；Service 已修复批量首项失败的证据审核，尚未在旧探针中完成例外重审。明确失败后计时器未关闭造成旧探针累计约 719 秒，现已修复；进度“已核验”改用责任账本数。308/308 项全量回归通过；正式冷学习和独立无 Agent 重放仍未通过。
- 随后以旧探针的真实失败 Operation 重审，`project_4_end` 和 `project_5_end` 各自记录为有证据的 `unsupported`：当前责任账本 67/85 核实、2 项例外、16 项待处理。早期“无上传入口”例外若后来出现上传控件将自动重开，并可由实际上传回执结算。309/309 项全量回归通过。旧探针仍超出执行预算，不作为正式 TC-001 冷学习或独立重放通过证据。
- The final local suite is 162 tests: 160 pass, 2 environment-dependent browser fixtures skip, and 0 fail. `git diff --check -- tools/cvmax` passes.
