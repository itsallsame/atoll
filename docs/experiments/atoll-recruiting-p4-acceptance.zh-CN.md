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

## 当前验证

```text
go test -race ./drivers/tools/recruitingexecutor/...
go vet ./drivers/tools/recruitingexecutor/...
./scripts/recruiting-boundary-check.sh a94d2b8d
```

## 尚未完成

- 声明式 JSON/DOM 离线执行器与相同输入确定性重放；
- HTTP Driver 的 DNS/IP 安全、redirect 同源策略、robots/条款证据、响应限额、origin 限流、429/403 熔断；
- Browser/Profile/Extension Driver 的隔离与秘密边界；
- 本地确定性站点的全部异常矩阵；
- 真实公开招聘站点的 Live Smoke，以及保存 URL、Recipe 版本、trace、Artifact 和抽样结论；
- 旧 Attempt 只保存 rejected Artifact、不能提交业务结果的端到端证明。

P4 仍为进行中；ABI 冻结不等于 Driver 与真实站点验收完成。
