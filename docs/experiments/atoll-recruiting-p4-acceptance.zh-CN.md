# Atoll Recruiting P4 验收记录

状态：进行中，尚未通过退出门

日期：2026-09-08

## 已完成：Recipe ABI v1

- ABI 位于 `drivers/tools/recruitingexecutor/recipeabi`，是招聘扩展内部协议，不向 Atoll core 增加字段或消息；
- 控制面的 Recipe 领域对象强制保存与 Executor 一致的 ABI version、opaque content ref、transport 和 required capability；跨包测试阻止 ABI 字符串漂移，Recipe Repository 阻止同一 version 静默改变执行方式；
- 固定执行输入：Target、版本化 Endpoint、Recipe Assignment/contract hash、可选 Checkpoint、不可解析的 `profile://` 引用、预算许可，以及 Work/Attempt/Company/Source/Profile 接受 fence；
- listing 和 detail 控制面现已把上述轻量执行输入作为带 kind discriminator 的 immutable offer 随 Attempt 持久化；相同 offer 命令在配置后续变化或进程重启后仍返回原始快照，避免 Executor 在执行中读取漂移配置。详情以 Job refresh generation 而非 Listing Checkpoint 作为数据代际，列表完成推进水位不会误杀已经领取的详情。offer 已携带与 Attempt 原子取得的版本化 BudgetPermit 及到期时间；进程级 Executor 自动运行正常路径已接通，崩溃恢复矩阵尚未闭合，因此当前仍不能宣称完整生产执行链；
- Executor 已增加只依赖 Atoll 公开 `Actor Resource` face 的 Recipe/Artifact 适配边界，没有修改 core。Recipe 正文从与 opaque content ref 同名的 KV Resource 读取，限制 256 KiB，严格拒绝未知字段和尾随 JSON，并同时复核规范内容 SHA-256、ABI、kind、transport 与 capability；外部 HTTP URL 不能充当 Recipe Resource；
- Artifact 通过 Atoll File Resource 的 `CreateFile`/`FileAccess` 流式写入，写入期间计算 SHA-256，超出调用方预算会 Abort，成功 Commit 后才形成只含 ResourceID/hash/access scope/retention 的领域元数据。元数据在创建文件前验证，Resource 拒绝或写入失败不会提交业务结果；Actor Message、Actor State 和 MySQL 不承载原始网站正文；
- HTTP Driver 已有正式的 Atoll ArtifactSink adapter：使用公开 `FormatFileAddress` 和 Resource directory/file capability，把确定性 Artifact ID 映射为配置设备与 Channel 下的文件；sink 绑定一个 Work/Attempt，并为每个 Attempt 创建同层独立证据目录，避免并发证据混放，也不要求不同设备驱动把“目录已存在”统一映射为同一错误。它拒绝跨 Attempt 写入、不安全路径片段、声明 hash 与字节不符、未知 artifact kind 和超过 20 MiB 的配置。针对“文件已 Commit、结果尚未提交即崩溃”的窗口，同 ResourceID 且逐字节 hash/长度一致视为幂等重放，不同内容明确冲突且绝不覆盖原文件；
- 公共 execution offer 已从 MySQL `store` 提取到招聘扩展内部的 `executioncontract` 包，控制面和 Executor 共享同一个 JSON 合同；`store` 只保留兼容别名，Executor 不依赖 Repository/DAO。控制面的响应版本也引用同一合同常量，避免两端字符串漂移；
- Executor ingress 已把 listing/detail offer 映射为 Recipe ABI RunInput，并在 Resource 解析及网络 I/O 前重检 Work/Attempt acceptance、Company/Source/Assignment/Recipe/Profile fence、BudgetPermit 与到期时间、origin、Recipe hash/capability/transport，以及 discriminator 与 Occurrence/Checkpoint/Detail payload 的互斥关系。Listing 输入保留版本化 Source Endpoint 和旧 Checkpoint，Detail 输入固定 Job URL/version 与 profile opaque ref；过期 permit、跨 origin、Recipe 串版和混合 payload 的测试均 fail closed；
- `execution.offer/accept/started/failed` 的消息类型、请求和成功响应也已收进同一共享合同。Executor 协议客户端只调用 Atoll 公开 `Sys.Call`/`Pending.Wait`，检查 response kind/type、core terminal status、合同版本、correlation、requested_by、Attempt/Executor/incarnation 绑定；远端失败保留结构化 error code，等待失败会主动 Cancel 当前 call，completed 响应的未知字段 fail closed。transition command receipt 与状态事务原子提交，并绑定 authenticated sender；并发重复和回包丢失可稳定重放。Executor 只由持久 dispatch 显式唤醒，每次最多领取一份 Work，不产生空轮询；
- `listing_page/listing_completion/detail` 结果 payload 也已归入共享 execution contract。Listing 成功转换会严格核对页面顺序、Artifact 顺序、末页标记、游标和唯一条目总数；把相对详情 URL 按实际页面 URL 解析并规范化，支持无精度损失的整数岗位 ID，为每条 Observation 和 listing fingerprint 生成稳定 SHA-256，并额外保存绑定 Work/Attempt 的 ListingDelta Artifact 后才形成 completion。原始 Artifact 的 `redacted` 不再被虚假固定为 true，而由实际 sink 策略声明；
- Driver 失败输出可转换为共享 FailureReport；控制面校验闭集 failure class、修复分类和绑定 Attempt/Work 的 Failure Artifact，并在 `execution.failed` 的同一招聘事务中保存 Artifact metadata、关闭 Attempt、释放 Permit、把 Work 置为可重试。这样 Resource 中的失败证据不会因随后状态提交而成为无元数据孤儿；
- 控制面不再把所有 Driver 失败立即放回可运行队列：带版本的可配置策略将瞬态错误写入未来 `not_before`，throttled 使用独立最短延迟，确定性修复/不可重试/次数耗尽进入 `waiting_human`；相关审计字段和协作事件与失败事务一起提交。该策略先关闭单 Executor 自旋和多 Executor 重试风暴入口，后续仍需按 origin/error 覆盖及 RepairIncident 单飞；
- Executor 内部已有不含调度策略的单次 Offer 编排：在 accept 前校验 immutable offer、Permit/expiry 与 HTTP transport，随后按 accept→started→Recipe Resource resolve→HTTP listing/detail→page/completion/detail 或 classified failed 顺序调用控制面。单元合同覆盖详情成功、列表页先于 completion、Recipe 解析失败转 Failure Artifact；结果提交出现歧义时不会擅自把 Attempt 判失败，而留给幂等重放/恢复。该编排已挂到同一 Executor class 的显式 `recruiting.execution.wake` 入口，每次最多领取一份 Work，不启动轮询或隐式 drain；
- 生产 wake 默认关闭，启用时配置必须显式提供唯一允许唤醒的 Recruiting Actor、`http.fetch` capability、调用超时、Artifact 的设备/Channel/目录/访问/保留/是否脱敏/字节上限、条款版本与复核时间，以及 HTTP/robots 限流参数。每次 Actor incarnation 使用新身份；offer command 在同一 delivery 内稳定，在 redelivery 或 Actor 重启时变化，以便重新观察队列而不是重放已经完成的旧 Offer。非配置控制 Actor、缺失安全配置和 Browser capability 均在网络 I/O 前拒绝。控制面的 capability-aware fleet 持久派发及跨真实进程正常执行已接通；崩溃切点 e2e 尚未全部接通；
- HTTP Detail runner 已实现“先证据、后解析”：成功响应先保存 response Artifact，再由同一声明式 JSON/HTML Recipe 离线提取且必须恰好得到一条记录，规范 JSON 和 normalized SHA-256 随稳定 detail-version ID 提交；HTTP 失败保留 failure Artifact，解析失败同时保留原始 response 与独立分类 failure Artifact。JSON 字符串字段现在与 DOM 一样去除首尾空白，确保规范详情重放稳定；
- Executor 的 result 客户端已通过共享 `recruiting.execution.result` 类型提交，并严格要求控制面 acknowledgement 的 `page|completion|detail` 分支与所提交 result_kind 唯一匹配；
- result command 的请求哈希包含 word、原始 payload 与 authenticated Executor sender；Listing Page、Listing Completion、Detail 的稳定 response receipt 和各自业务事实处于同一 MySQL 事务。MySQL 8.4 合同已验证相同页面命令并发只接受一次、另一次稳定重放，以及相同 command ID 改请求明确冲突；这关闭了“Artifact 可重放但 command ID 可被另一结果复用”的缺口；
- 固定 Recipe 类型 `listing|detail|discovery` 和执行 transport `http_json|http_html|browser`，但 capability 仍为可扩展字符串，不制造多种 Worker class；Extension 是候选 Recipe 捕获入口，不是日常执行 transport；
- 声明式请求只能为 GET；timeout、响应大小和 redirect 次数有硬上限；Recipe 只能声明 Accept/Accept-Language，不能携带 Cookie、Authorization 等秘密；
- Listing Recipe 必须声明稳定 identity、`newest_activity_desc`、update-retop、`activity_time|frontier_keys` 边界、安全重叠页数和最大页数；identity/activity 必须引用实际提取字段；
- Listing Recipe 现已显式声明 `detail_url_field` 并把它纳入内容哈希，Executor 不再靠字段名猜测岗位详情 URL。Recipe 单页上限从与控制面不一致的 5,000 收敛到 500；Driver 为每个成功页面保留 sequence、页面 URL、resume cursor、terminal、Artifact 和该页首次出现的唯一 items，跨页重复不会导致 completion item_count 与已接受页面总数不一致；
- Checkpoint 只有在 identity 完整、分页稳定、倒序契约、旧 frontier 到达、同时间组完整消费和 overlap 完成全部证明都成立时才能推进；
- 成功输出必须包含 Artifact 证据和结构化 JSON；失败必须包含 Artifact 并使用有限分类，不能把原始错误文本当错误类别；
- Recipe 内容哈希覆盖完整 ABI，并由确定性 JSON 编码产生稳定 SHA-256；输入、请求边界、输出与 hash 均已有 race 测试。
- `recipeexec` 对已保存响应提供无网络的 JSON 与 HTML 执行：JSON 使用 RFC 6901 pointer，HTML 使用 CSS selector 及受限属性读取，不执行页面脚本；字段缺失、selector 错误、异常尾随输入和单页超量均 fail closed；
- JSON/DOM 输出统一规范为稳定字段 JSON，原始响应计算 SHA-256；测试证明相同 Artifact 重放完全一致、置顶项可排除、文本空白可规范化、分页引用可提取，并从真实观测计算身份完整与活动倒序证据；
- `ListingScan` 实现跨页增量闭合：`activity_time` 只在出现严格更旧时间后确认同时间组已经完整消费，`frontier_keys` 必须找到上次全部边界键；随后继续完整读取配置的 overlap 页，或到达真实输入末尾；
- 扫描按稳定岗位键去重，相同键跨页内容冲突会令 `pagination_stable=false`；边界消失、逆序、身份缺失或先碰到 `max_pages` 均不能生成 Checkpoint；安全完成时只生成 version+1 候选，最终 CAS 仍由 Recruiting Actor 控制；
- baseline 只有读到输入末尾才可产生首个 Checkpoint；置顶广告在提取必填岗位字段之前排除，因此不要求广告伪装成岗位；
- DOM selector 使用独立 `cascadia v1.3.3`，仅在招聘 Executor 扩展中依赖；原项目依赖清单未被 `go mod tidy` 的非任务机械删除污染。
- `httpdriver` 已实现只读 GET 效果边界：Recipe timeout/响应字节/redirect 上限、显式 User-Agent、Accept header 白名单、全局并发和每 origin 最小间隔；响应体在内存和返回 Artifact 字节两处都不超过上限；
- 生产 Dialer 在实际连接前解析全部地址并拒绝 loopback、private、link-local、multicast、CGNAT、benchmark 和文档网段，避免 DNS rebinding/SSRF；redirect 只允许相同 scheme+authority 且次数受 Recipe 限制；
- 每次 fetch 必须同时携带有效 robots 证据和条款审查版本/时间；robots 拒绝发生在网站请求之前；429、403、5xx、超时、过大响应、危险 Endpoint 和 redirect 分为稳定错误类别，连续 429/403/5xx 会打开本进程 origin circuit；
- `httptest` 验证 GET/UA、robots 先决条件、响应截断、超时、同源与跨源 redirect、429 熔断及生产 Dialer 的私网拒绝。测试专用私网开关只存在于未导出的构造函数，生产 `New` 无法开启。
- `RobotsTxtChecker` 使用同一生产级安全 Dialer 获取每个 origin 的 `/robots.txt`，限制 1 MiB/30s 上限，按实际 Recipe User-Agent 解析 allow/disallow 与 crawl-delay，并保存 policy URL、内容 SHA-256、检查时间；
- robots cache 按 origin 带 TTL，单 origin lock 合并并发首次加载；401/403/429、超量、解析或网络失败均 fail closed，跨 origin redirect 被拒绝；crawl-delay 会延长 HTTP Driver 后续请求的 origin 间隔。
- `RunListing` 已串联 HTTP→Artifact→离线 Recipe→`ListingScan`：每页原始响应必须先经 `ArtifactSink` 成功持久化，之后才允许解析；单页和整次扫描都有独立字节上限，next page 必须保持初始 scheme+authority 且不得携带凭据或 fragment；
- 成功结果包含所有去重观测、逐页 Artifact 和仅供 Actor CAS 的 Checkpoint candidate；HTTP/解析/分页/质量失败返回有限类别、原始页面及额外 failure Artifact，保留当时质量证明，且不产生 Checkpoint；Artifact sink 失败直接中止执行；
- 控制面已实现 ABI 对应的有界 `listing_page` 与 `listing_completion` 接受：只有完整 identity、pagination、ordering、frontier、同时间组和 overlap 证明可以推进 Checkpoint；Executor 的 candidate 只贡献 frontier 时间/键，其余 Checkpoint 字段不受消息控制；
- 控制面也已实现 `detail` offer/result：同一 Executor class 按 capability、origin 和全局 Work priority 领取列表或详情；detail result 有 1 MiB 结构化上限并原子提交 response Artifact、Job/DetailVersion、Attempt/Work 及 outbox，相同 Artifact 只有输入摘要完全一致时才能重放；
- 三页真实 `httptest` 流程验证：顶部新岗位→完整旧时间组→更旧边界→一页 overlap 后停止，保存三页 Artifact 并生成 checkpoint version+1；另覆盖畸形 JSON、跨源 next 和 Artifact 写失败。
- JSON 稳定岗位键支持字符串和整数两种无歧义标量；整数保持任意精度并规范成十进制字符串，浮点、指数、布尔、null、对象和数组仍 fail closed。该兼容性由真实 Greenhouse 数据暴露，并只修改招聘 Recipe 执行扩展。
- browserdriver 已固定受控 Browser Broker 边界：招聘 Executor 只发送 profile:// opaque ref，Cookie、密码、OTP 和 Authorization 不出现在 SessionRequest；秘密只允许由授权设备侧 Broker 解析；
- Browser Plan 仅支持 wait_selector、scroll_page 和 follow_link，没有任意 JavaScript、输入文本、表单提交、通用 click 或下载动作；selector、次数、导航数、DOM 字节和整次 timeout 均有硬上限，follow_link 必须由 Broker 解析 href 后按同源 GET 导航；
- Broker 必须返回网络效果 attestation：只允许 GET/HEAD，写请求、跨源文档导航、表单、下载和 popup 都必须为 0；同时证明解析地址为公网、robots allowed、条款版本吻合，以及使用 Profile 时 lease 已授权。任一项不符均分类为 effect policy violation；
- Browser DOM 同样先保存 Artifact，之后才检查 attestation、最终 URL 与离线 DOM Recipe；策略违规、跨源、超量和解析失败仍保留证据。当前实现是安全契约和可替换 Broker adapter，尚未宣称已经接入某个 Chromium 运行时。
- `extensioncapture` 把浏览器插件严格定位为人工发现/修复入口，而非新的长期 Worker：用户确认后只能提交绑定 Source/Endpoint version 的候选 Recipe、结构化 selector 轨迹和 `artifact://` 证据；候选内容生成稳定哈希，但没有 active、Assignment 或 Checkpoint 字段；
- 插件轨迹没有输入值、请求体、Cookie/storage 或凭据字段，候选仍受 Recipe GET/header/预算校验；带凭据 URL、疑似 secret query、signed object URL、任意交互以及“仍依赖 extension 执行”的候选均拒绝。Browser 候选必须转成受控 Browser Plan，HTTP 候选不能夹带 Browser action；发布、灰度和 Assignment 仍只能由 Recruiting Actor 后续审批命令完成。
- `recipe.propose` 已把权限受控的 Recipe Spec Resource 接入 Recruiting Actor：Source version/active Endpoint revision 是提案 fence，Actor 与 Executor 共享严格有界 decoder，自行计算 content/compatibility contract hash 和 Endpoint scope。客户端不能提交 active 状态、Assignment 或自报 contract hash；错误 hash/未知字段失败关闭，成功事务只产生 immutable draft、receipt/event。Extension Capture 的 trace/capture/evidence 完整 Proposal 尚未持久接入，不能把本入口描述为浏览器插件闭环已完成。
- Listing 候选 Recipe 已有真实执行式验证和独立审批：`recipe.validate` 原子冻结候选 version 与 ready Source 的生产 Endpoint/Company/Source fence，创建 `recipe_validation` ListingRun、Work 和 capability dispatch；拟议 Assignment 只用于 immutable offer，不写入 Source 当前 Assignment 或历史。Executor 复用 Listing validation driver，只提交 page/trace Artifact 与质量证明；结果不写 Job、Observation 或 Checkpoint。
- `recipe.approve` 重新核对 candidate content/contract/execution、指定 Work/run、唯一成功 Attempt、绑定该 Attempt 的未拒绝 Artifact 数量，以及 identity/ordering/pagination 三项证明；失败统一为 `quality_rejected` 且不发布。共享 execution result contract 与客户端 acknowledgement 已显式增加 `recipe_validation` 分支，避免执行完成后因客户端闭集遗漏而把 Attempt 留在 running。
- `recipe.reject` 不允许用 Recipe 状态切换隐式遗弃运行中的验证。它要求精确 validation Work 已为 completed/failed/canceled 且没有活动 Attempt，否则返回 `waiting_human`；成功时仅把候选返回 draft，保留全部 Work/Attempt/Artifact 证据并支持命令重放。普通用户 server E2E 已覆盖 open Work 拒绝失败、显式 cancel、拒绝成功及 server restart 后重放。

## 候选 Recipe 真实网站验证（负向证据通过）

2026-09-10 显式运行 `ATOLL_RECRUITING_LIVE_E2E=1 go test ./e2e -run '^TestRecruitingLiveRecipeValidationRejectsBadCandidateThroughAtoll$' -count=1 -v`。普通登录用户通过真实 Atoll server 创建控制 Actor 和 daemon 上的统一 HTTP Executor；测试只读访问 Discord 的公开 Greenhouse Job Board API。候选 Listing Recipe 能成功请求、解析并产生 page/trace Artifact，但该实时列表不满足候选声明的 `newest_activity_desc`，因此验证 Work/Attempt 作为“执行完成”成功收口，后续审批明确返回 `quality_rejected`。回读确认候选仍为 validating、当前 Assignment 引用数为零，且验证事务没有写岗位事实或 Checkpoint。

这条用例不是把第三方数据失败包装成通过；它验证的是审批 fail closed。单次实时列表不能证明 update-retop，且测试没有将该 Source 标为可生产增量。紧凑证据见 `docs/experiments/evidence/recruiting-live-recipe-validation-20260910.json`。

## 真实站点 Live Smoke（诊断通过，增量认证拒绝）

2026-09-08 运行 `make recruiting-live-smoke`，固定访问 Greenhouse 官方公开 Job Board API 的 MongoDB board。官方文档说明 Job Board 的 GET 数据公开且无需认证；工具同时读取实际 `robots.txt`，目标 API 路径判定为 allowed。工具不接受任意 URL，只允许一次只读 GET，响应上限 2 MiB、redirect 为 0、并发为 1。

实际响应 876,426 bytes、407 个岗位，身份完整；原始响应先保存为 Artifact，SHA-256 为 `29be60ed9e417ae7b36413daf1436f4610c4b6d00cda8938ca751755bcd4f7cc`。离线 Recipe 随后确认当前列表违反 `newest_activity_desc`，返回非重试 `quality_rejected`，没有生成或提交 Checkpoint。即使未来某次快照恰好倒序，单次观测仍不能证明历史岗位更新后置顶，因此该 Source 保持 `production_incremental_eligible=false`。

可机读的紧凑证据位于 `docs/experiments/evidence/recruiting-live-smoke-greenhouse-20260908.json`。第三方原始岗位 payload 不提交到 Git；本次运行对象保存在报告记录的本地 Artifact 路径，Git 只保存内容哈希和审计结论。

同日又以 `scripts/recruiting-live-e2e.sh` 完成一次真实进程纵向验收，而非命令行 Driver 探针：普通运营员在自己的 Home Channel 创建 server 侧 Recruiting Actor 与 daemon 侧 Executor；每日 durable timer 自动物化 occurrence、Work 和 capability dispatch。测试确认 pending dispatch 已提交后、首次投递前强杀 server，随后重启 server、重新登录并等待 daemon Executor 恢复 present；恢复后的 Recruiting Actor 从 MySQL 找回并投递同一 dispatch。Executor 从真实 Recipe KV 解析相同只读 Recipe，经生产 HTTP/robots/条款边界访问 Greenhouse，再把页面及 failure Artifact 写入 daemon File Resource，通过 Atoll Message 提交分类失败并确认 dispatch。2026-09-09 实测日常、独立 diagnostic 和独立 production 均因真实列表违反倒序契约而进入 Work `waiting_human`、Attempt `failed`、failure class `quality_rejected`；diagnostic 的岗位事实为零变化，production 的 Checkpoint 保持 `1→1`，质量拒绝没有被测试代码改写成成功。随后故障注入把已处理 dispatch 恢复成 completion acknowledgement 丢失时的 pending 状态；第二次 delivery 得到 idle 并确认 delivered，delivery attempts=2、Work Attempt 总数仍为 1，证明没有重新抓取。测试默认跳过第三方网络，只有显式 `ATOLL_RECRUITING_LIVE_E2E=1` 才运行。

- HTTP Executor 现识别独立 `ListingRun` Offer；diagnostic 成功时不构造 ListingObservation 或 checkpoint completion，而是把原始 page Artifact 与一份 trace Artifact 作为单个 `diagnostic` 结果提交。production 使用 ListingRun ID 作为本次列表执行证据 ID，生成与 daily 相同的 page→completion 结果序列；控制面据 Work 关联区分 SourceOccurrence 和独立 production ListingRun，不创建伪日报实体。单元测试覆盖冻结 Checkpoint 输入和两类结果转换；

## 当前验证

```text
go test -race ./drivers/tools/recruitingexecutor/...
go test -race ./cmd/recruiting-live-smoke
go vet ./drivers/tools/recruitingexecutor/...
./scripts/recruiting-boundary-check.sh a94d2b8d
make recruiting-live-smoke
./scripts/recruiting-live-e2e.sh
```

## 尚未完成

- Browser Broker 的 Chromium/CDP 实现、OS/container 级隔离，以及 Extension 候选 Proposal 接入 Recruiting Actor 的持久 Draft/人工审批链；
- 本地确定性站点的全部异常矩阵；
- 更多站型的 Nightly/Weekly Live 验证，以及由正式 Artifact 存储提供保留期，而不是验收机本地文件；
- Recipe KV 与 Artifact File 已在真实 Atoll server/daemon 的允许路径通过；权限拒绝和重启保持 e2e 仍待补齐；
- 已有 Repository 合同证明旧 listing Attempt 只保存 rejected Artifact、不能提交业务结果；仍缺真实 Executor Actor 经 Atoll Message 提交该迟到结果的进程级端到端证明。
- 页面进度已改为 Attempt 作用域，并通过 MySQL 8.4 的 crash/retry 合同：Attempt A 接受第一页后失败，Attempt B 可从第一页重新运行；A/B 页面证据同时保留，B 的 completion 只统计 B 的页面。Failure Artifact 的原子控制面合同及真实进程自动执行正常路径已闭合；启用前仍须补齐真实进程崩溃切点。
- accept/start/failed 以及 page/completion/detail 的 command receipt 已闭合，控制面也已有 capability-aware fleet 的持久单次 wake 和 authenticated completion acknowledgement；“控制面已提交 dispatch、首次投递前 server 退出”和“Executor 已处理、控制面未保存 completion acknowledgement”已有真实 Atoll 进程恢复证明，尚缺“Executor 处理中退出”和“业务结果已提交但响应丢失”等切点。

P4 仍为进行中；ABI 冻结不等于 Driver 与真实站点验收完成。
