# Atoll Recruiting P4 验收记录

状态：进行中，尚未通过退出门

日期：2026-09-08

## 已完成：Recipe ABI v1

- ABI 位于 `drivers/tools/recruitingexecutor/recipeabi`，是招聘扩展内部协议，不向 Atoll core 增加字段或消息；
- 固定执行输入：Target、版本化 Endpoint、Recipe Assignment/contract hash、可选 Checkpoint、不可解析的 `profile://` 引用、预算许可，以及 Work/Attempt/Company/Source/Profile 接受 fence；
- 固定 Recipe 类型 `listing|detail|discovery` 和执行 transport `http_json|http_html|browser|extension`，但 capability 仍为可扩展字符串，不制造多种 Worker class；
- 声明式请求只能为 GET；timeout、响应大小和 redirect 次数有硬上限；Recipe 只能声明 Accept/Accept-Language，不能携带 Cookie、Authorization 等秘密；
- Listing Recipe 必须声明稳定 identity、`newest_activity_desc`、update-retop、`activity_time|frontier_keys` 边界、安全重叠页数和最大页数；identity/activity 必须引用实际提取字段；
- Checkpoint 只有在 identity、倒序契约、旧 frontier 到达和 overlap 完成四项证明都成立时才能推进；
- 成功输出必须包含 Artifact 证据和结构化 JSON；失败必须包含 Artifact 并使用有限分类，不能把原始错误文本当错误类别；
- Recipe 内容哈希覆盖完整 ABI，并由确定性 JSON 编码产生稳定 SHA-256；输入、请求边界、输出与 hash 均已有 race 测试。
- `recipeexec` 对已保存响应提供无网络的 JSON 与 HTML 执行：JSON 使用 RFC 6901 pointer，HTML 使用 CSS selector 及受限属性读取，不执行页面脚本；字段缺失、selector 错误、异常尾随输入和单页超量均 fail closed；
- JSON/DOM 输出统一规范为稳定字段 JSON，原始响应计算 SHA-256；测试证明相同 Artifact 重放完全一致、置顶项可排除、文本空白可规范化、分页引用可提取，并从真实观测计算身份完整与活动倒序证据；
- `ListingScan` 实现跨页增量闭合：`activity_time` 只在出现严格更旧时间后确认同时间组已经完整消费，`frontier_keys` 必须找到上次全部边界键；随后继续完整读取配置的 overlap 页，或到达真实输入末尾；
- 扫描按稳定岗位键去重，相同键跨页内容冲突会令 `pagination_stable=false`；边界消失、逆序、身份缺失或先碰到 `max_pages` 均不能生成 Checkpoint；安全完成时只生成 version+1 候选，最终 CAS 仍由 Recruiting Actor 控制；
- baseline 只有读到输入末尾才可产生首个 Checkpoint；置顶广告在提取必填岗位字段之前排除，因此不要求广告伪装成岗位；
- DOM selector 使用独立 `cascadia v1.3.3`，仅在招聘 Executor 扩展中依赖；原项目依赖清单未被 `go mod tidy` 的非任务机械删除污染。

## 当前验证

```text
go test -race ./drivers/tools/recruitingexecutor/...
go vet ./drivers/tools/recruitingexecutor/...
./scripts/recruiting-boundary-check.sh a94d2b8d
```

## 尚未完成

- HTTP Driver 与跨页 fetch loop，把每页原始响应先保存为 Artifact，再交给离线执行器和 `ListingScan`；
- HTTP Driver 的 DNS/IP 安全、redirect 同源策略、robots/条款证据、响应限额、origin 限流、429/403 熔断；
- Browser/Profile/Extension Driver 的隔离与秘密边界；
- 本地确定性站点的全部异常矩阵；
- 真实公开招聘站点的 Live Smoke，以及保存 URL、Recipe 版本、trace、Artifact 和抽样结论；
- 旧 Attempt 只保存 rejected Artifact、不能提交业务结果的端到端证明。

P4 仍为进行中；ABI 冻结不等于 Driver 与真实站点验收完成。
