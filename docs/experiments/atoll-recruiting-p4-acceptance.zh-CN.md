# Atoll Recruiting P4 验收记录

状态：进行中，尚未通过退出门

日期：2026-09-08

## 已完成：Recipe ABI v1

- ABI 位于 `drivers/tools/recruitingexecutor/recipeabi`，是招聘扩展内部协议，不向 Atoll core 增加字段或消息；
- 控制面的 Recipe 领域对象强制保存与 Executor 一致的 ABI version、opaque content ref、transport 和 required capability；跨包测试阻止 ABI 字符串漂移，Recipe Repository 阻止同一 version 静默改变执行方式；
- 固定执行输入：Target、版本化 Endpoint、Recipe Assignment/contract hash、可选 Checkpoint、不可解析的 `profile://` 引用、预算许可，以及 Work/Attempt/Company/Source/Profile 接受 fence；
- listing 和 detail 控制面现已把上述轻量执行输入作为带 kind discriminator 的 immutable offer 随 Attempt 持久化；相同 offer 命令在配置后续变化或进程重启后仍返回原始快照，避免 Executor 在执行中读取漂移配置。详情以 Job refresh generation 而非 Listing Checkpoint 作为数据代际，列表完成推进水位不会误杀已经领取的详情。offer 已携带与 Attempt 原子取得的版本化 BudgetPermit 及到期时间；进程级 Executor 自动运行仍未接通，因此当前仍不能宣称完整生产执行链；
- Executor 已增加只依赖 Atoll 公开 `Actor Resource` face 的 Recipe/Artifact 适配边界，没有修改 core。Recipe 正文从与 opaque content ref 同名的 KV Resource 读取，限制 256 KiB，严格拒绝未知字段和尾随 JSON，并同时复核规范内容 SHA-256、ABI、kind、transport 与 capability；外部 HTTP URL 不能充当 Recipe Resource；
- Artifact 通过 Atoll File Resource 的 `CreateFile`/`FileAccess` 流式写入，写入期间计算 SHA-256，超出调用方预算会 Abort，成功 Commit 后才形成只含 ResourceID/hash/access scope/retention 的领域元数据。元数据在创建文件前验证，Resource 拒绝或写入失败不会提交业务结果；Actor Message、Actor State 和 MySQL 不承载原始网站正文；
- HTTP Driver 已有正式的 Atoll ArtifactSink adapter：使用公开 `FormatFileAddress` 和 Resource directory/file capability，把确定性 Artifact ID 映射为配置设备与 Channel 下的文件；sink 绑定一个 Work/Attempt，拒绝跨 Attempt 写入、声明 hash 与字节不符、未知 artifact kind 和超过 20 MiB 的配置。针对“文件已 Commit、结果尚未提交即崩溃”的窗口，同 ResourceID 且逐字节 hash/长度一致视为幂等重放，不同内容明确冲突且绝不覆盖原文件；
- 公共 execution offer 已从 MySQL `store` 提取到招聘扩展内部的 `executioncontract` 包，控制面和 Executor 共享同一个 JSON 合同；`store` 只保留兼容别名，Executor 不依赖 Repository/DAO。控制面的响应版本也引用同一合同常量，避免两端字符串漂移；
- Executor ingress 已把 listing/detail offer 映射为 Recipe ABI RunInput，并在 Resource 解析及网络 I/O 前重检 Work/Attempt acceptance、Company/Source/Assignment/Recipe/Profile fence、BudgetPermit 与到期时间、origin、Recipe hash/capability/transport，以及 discriminator 与 Occurrence/Checkpoint/Detail payload 的互斥关系。Listing 输入保留版本化 Source Endpoint 和旧 Checkpoint，Detail 输入固定 Job URL/version 与 profile opaque ref；过期 permit、跨 origin、Recipe 串版和混合 payload 的测试均 fail closed；
- `execution.offer/accept/started/failed` 的消息类型、请求和成功响应也已收进同一共享合同。Executor 协议客户端只调用 Atoll 公开 `Sys.Call`/`Pending.Wait`，检查 response kind/type、core terminal status、合同版本、correlation、requested_by、Attempt/Executor/incarnation 绑定；远端失败保留结构化 error code，等待失败会主动 Cancel 当前 call，completed 响应的未知字段 fail closed。尚未加入唤醒/循环，因此不会产生空轮询；
- `listing_page/listing_completion/detail` 结果 payload 也已归入共享 execution contract。Listing 成功转换会严格核对页面顺序、Artifact 顺序、末页标记、游标和唯一条目总数；把相对详情 URL 按实际页面 URL 解析并规范化，支持无精度损失的整数岗位 ID，为每条 Observation 和 listing fingerprint 生成稳定 SHA-256，并额外保存绑定 Work/Attempt 的 ListingDelta Artifact 后才形成 completion。原始 Artifact 的 `redacted` 不再被虚假固定为 true，而由实际 sink 策略声明；
- HTTP Detail runner 已实现“先证据、后解析”：成功响应先保存 response Artifact，再由同一声明式 JSON/HTML Recipe 离线提取且必须恰好得到一条记录，规范 JSON 和 normalized SHA-256 随稳定 detail-version ID 提交；HTTP 失败保留 failure Artifact，解析失败同时保留原始 response 与独立分类 failure Artifact。JSON 字符串字段现在与 DOM 一样去除首尾空白，确保规范详情重放稳定；
- Executor 的 result 客户端已通过共享 `recruiting.execution.result` 类型提交，并严格要求控制面 acknowledgement 的 `page|completion|detail` 分支与所提交 result_kind 唯一匹配；
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

## 真实站点 Live Smoke（诊断通过，增量认证拒绝）

2026-09-08 运行 `make recruiting-live-smoke`，固定访问 Greenhouse 官方公开 Job Board API 的 MongoDB board。官方文档说明 Job Board 的 GET 数据公开且无需认证；工具同时读取实际 `robots.txt`，目标 API 路径判定为 allowed。工具不接受任意 URL，只允许一次只读 GET，响应上限 2 MiB、redirect 为 0、并发为 1。

实际响应 876,426 bytes、407 个岗位，身份完整；原始响应先保存为 Artifact，SHA-256 为 `29be60ed9e417ae7b36413daf1436f4610c4b6d00cda8938ca751755bcd4f7cc`。离线 Recipe 随后确认当前列表违反 `newest_activity_desc`，返回非重试 `quality_rejected`，没有生成或提交 Checkpoint。即使未来某次快照恰好倒序，单次观测仍不能证明历史岗位更新后置顶，因此该 Source 保持 `production_incremental_eligible=false`。

可机读的紧凑证据位于 `docs/experiments/evidence/recruiting-live-smoke-greenhouse-20260908.json`。第三方原始岗位 payload 不提交到 Git；本次运行对象保存在报告记录的本地 Artifact 路径，Git 只保存内容哈希和审计结论。

## 当前验证

```text
go test -race ./drivers/tools/recruitingexecutor/...
go test -race ./cmd/recruiting-live-smoke
go vet ./drivers/tools/recruitingexecutor/...
./scripts/recruiting-boundary-check.sh a94d2b8d
make recruiting-live-smoke
```

## 尚未完成

- Browser Broker 的 Chromium/CDP 实现、OS/container 级隔离，以及 Extension 候选 Proposal 接入 Recruiting Actor 的持久 Draft/人工审批链；
- 本地确定性站点的全部异常矩阵；
- 更多站型的 Nightly/Weekly Live 验证，以及由正式 Artifact 存储提供保留期，而不是验收机本地文件；
- Resource 适配器已经过内存 capability 合同测试，但仍缺真实 Atoll daemon 上 Recipe KV 与 Artifact File 的跨进程读写、权限拒绝和重启保持 e2e；
- 已有 Repository 合同证明旧 listing Attempt 只保存 rejected Artifact、不能提交业务结果；仍缺真实 Executor Actor 经 Atoll Message 提交该迟到结果的进程级端到端证明。
- 页面进度已改为 Attempt 作用域，并通过 MySQL 8.4 的 crash/retry 合同：Attempt A 接受第一页后失败，Attempt B 可从第一页重新运行；A/B 页面证据同时保留，B 的 completion 只统计 B 的页面。该恢复阻塞项已经关闭，但进程级自动执行仍须补齐 failure Artifact、消息唤醒与真实进程崩溃切点后才能启用。

P4 仍为进行中；ABI 冻结不等于 Driver 与真实站点验收完成。
