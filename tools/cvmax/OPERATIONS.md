# CvMax 安装、升级与诊断

CvMax 作为 Atoll 应用安装到用户已有的私有频道。它复用 Atoll 的身份、频道、Hosted Agent、MCP 成员和生命周期，不启动第二套调度或身份。CvMax 公共经验服务是应用后端，只负责发布无事实值配方和接收匿名核验反馈。Web 入口留待后续，本期入口是外部 AI 中的唯一 CvMax Skill 和配对的 Chromium 插件。Microsoft Edge 是正式验收主浏览器，Chrome 保留兼容。

## 前置条件

- Node.js 22 或更高版本；
- 一个正在运行且可访问的 Atoll 节点、用户私有频道和该用户的访问凭据；
- Microsoft Edge 已通过 `edge://extensions` 的开发者模式加载 `extension/`，并完成一次本机服务配对；Chrome 可用同一目录兼容验证；
- 同机同时安装 Edge 与 Chrome 插件时，在私有 Service 配置的 `browser.preferredFamily` 写入 `"edge"`。插件会在握手时上报浏览器类型，Service 只向指定浏览器派发命令；`doctor` 的 `browserSelection.selectedFamily` 必须为 `edge` 后才能开始正式用例。
- 用户提供的岗位链接或明确选择的当前页构成当前 Application 的任务级授权；服务配置不维护招聘域名名单；
- 简历仅在用户明确授权保存或用于任务时读取；保存后的每份简历保持独立。
- 公共经验后端使用独立 `cvmax` MySQL 与 `cvmax_app` 运行时账号；真实连接写入权限为 0600 且已被 git 忽略的 `.env.local`，不复制到用户插件或 Skill。

公共经验库迁移位于 `db/migrations/`，只能由数据库管理员显式执行。运行时账号没有 DDL 权限。2026-09-06 的开发验证经用户明确授权，暂时沿用 Light Cone 当前无 TLS 的连接方式；生产环境必须改用数据库私网地址、TLS 或受控隧道，当前配置不得直接发布。

私有 Client 配置示例：

```json
{
  "base": "http://127.0.0.1:18932",
  "channel": "c0",
  "authorization": "Bearer <atoll-token>",
  "serviceConfig": "/absolute/private/path/service.json"
}
```

配置文件应只有当前用户可读。不要把 Cookie、Bearer token、简历路径或暂存附件提交到仓库。

## 安装与升级

公共 Registry 开发启动命令：

```text
node /absolute/repo/tools/cvmax/src/registry-server.mjs /absolute/private/registry.env
```

Registry 默认只监听 `127.0.0.1`。生产部署放在 HTTPS 网关后，使用服务端登记的独立安装凭据、独立管理员 token、写入限流和审计；全局写入 token 只保留给本机开发兼容。Service 私有配置中的 `recipeRegistry` 包含 Registry URL、安装 ID、对应的 `credential`、可信根公钥、缓存文件和 TTL；插件与 Skill 不持有数据库账号或签名私钥。签名密钥轮换通过上一把可信密钥签署转换声明，客户端可在线扩展信任链，无需升级插件或重启 Service。Registry 暂时不可达时，候选、反馈和匿名指标写入最多 500 条的本地持久队列，恢复后按原回执 ID 重放；页面填写不等待后台写入恢复。容器、迁移、nginx、凭据、回滚与密钥轮换步骤见 [Registry 部署说明](deploy/registry/README.md)。

仓库开发入口：

```text
node /absolute/repo/tools/cvmax/src/cli.mjs install --config /absolute/private/client.json
```

`install` 是幂等操作。它通过 Atoll 的公开声明和频道 overlay 安装或更新 `cvmax-agent` 与 `cvmax-service`；全部应用层运行模块及私有配置内容完全相同时返回空的 `restarted` 列表，不重启成员。新增岗位、招聘域名或公共经验不属于软件升级，不要求重新安装或重启。已有任务保存在应用状态文件中；升级重启后，确定未派发或已经验证的工作进入安全接续，已发送但结果不明的动作进入只读核对。

旧的自适应 Agent 重置和逐控件学习链已移除。当前未知页面返回 `learning_unavailable`，不会重置或唤醒 Agent；待新的整页学习流程接通后再补充操作说明。

只有浏览器扩展代码发生变化时才更新 `extension/manifest.json` 的版本，并让用户在 `edge://extensions`（Chrome 兼容测试时为 `chrome://extensions`）手动重新加载。Agent、Client 或 Service 更新不要求重载插件。详细步骤见 [`extension/README.md`](extension/README.md)。

招聘公司适配属于公共 Page Model + Recipe 发布，不属于软件发布。Registry 的新签名 release 会在目标页面下一次观察时被现有 Service 拉取；未知状态返回 404 是正常未命中，不触发降级、重启或重载。只有网络失败、签名失败或 Registry 服务异常才显示 `publicRecipe: degraded`。本地开发者加载的 unpacked 扩展不会自动更新通用执行器代码，因此开发阶段修改插件本身时仍需手动重载一次；正式商店版本由浏览器升级机制处理。

## 使用

所有命令从标准输入读取一个 JSON 对象：

```text
printf '%s' '<resume-json>' | node /absolute/repo/tools/cvmax/src/cli.mjs resume-save --config /absolute/private/client.json
printf '%s' '{}' | node /absolute/repo/tools/cvmax/src/cli.mjs resume-list --config /absolute/private/client.json
printf '%s' '<request-json>' | node /absolute/repo/tools/cvmax/src/cli.mjs submit --config /absolute/private/client.json
printf '%s' '{"taskId":"<id>"}' | node /absolute/repo/tools/cvmax/src/cli.mjs status --config /absolute/private/client.json
printf '%s' '{"taskId":"<id>"}' | node /absolute/repo/tools/cvmax/src/cli.mjs task-delete --config /absolute/private/client.json
printf '%s' '{"resumeId":"<id>"}' | node /absolute/repo/tools/cvmax/src/cli.mjs resume-delete --config /absolute/private/client.json
printf '%s' '{}' | node /absolute/repo/tools/cvmax/src/cli.mjs maintenance --config /absolute/private/client.json
```

`resume-save` 接收 `{saveRequestId, name, projectionFacts, resumePath, resumeId?, expectedRevision?}`。首次保存返回 `resumeId` 和 revision；修改已有简历生成新 revision。重复执行使用相同 `saveRequestId`。

`submit` 必须且只能选择一种入口：`links` 或 `currentPage: true`。正常请求指定 `{resume: {id, revision}, taskAnswers?}`；旧的顶层 `{resumeId, resumeRevision}` 仅作兼容别名，不能与规范形式混用。Client 严格拒绝未知参数，并先调用 `request_save` 校验简历存在、版本一致以及解析后的事实数和附件状态。服务冻结该简历版本，不能从其他简历补值。多链接会先去重并冻结顺序，只在当前 Application 开始时打开对应页面；后续岗位不会预先生成标签页。超时后使用相同 `clientRequestId` 查询或重试，不能另建任务。`continue` 接续排队、待检查或待只读核对的任务；`resume` 只用于用户明确暂停的任务。

`task-delete` 只删除已经结束或取消的任务；`resume-delete` 在没有活跃任务引用时删除简历记录和保存的源文件。`maintenance` 执行过期清理并返回计数。默认保留已结束任务 90 天、暂存请求和文件 24 小时，任务、简历和暂存记录均有容量上限；策略可在私有 Service 配置的 `localPolicy` 中调整。状态存储通过应用接口注入，当前 JSON 实现不是产品契约，也不要求引入 SQLite。

## 诊断

```text
node /absolute/repo/tools/cvmax/src/cli.mjs doctor --config /absolute/private/client.json
```

正常结果应同时显示 Atoll 已连接、频道已附着、Service 和 Agent 可用、Browser 已连接，并始终显示 `finalSubmitAllowed: false`。

故障处理按当前状态逐步进行：

1. `service_unavailable` 或 `agent_unavailable`：重新执行一次幂等 `install`，再运行 `doctor`。
2. `browser: disconnected`：确认 Edge/Chrome 扩展已加载且配对服务地址正确；只有扩展版本变化时才重新加载。
3. `needs_reconcile`：执行 `continue`，由 Hosted Agent 先做只读观察；不得重复未知动作。
4. `waiting_user`：只完成当前提示的一个页面动作或一小组相关事实，再用 `answer` 接续同一 taskId。
5. `cancelled` 或 `finished`：属于终态；若有新需求，使用新的 `clientRequestId` 创建新任务。

## 当前边界

- 不点击最终投递，不代替用户接受隐私政策或法律声明；
- 证件号等敏感信息由用户直接在招聘页面输入，不通过对话传递；
- 网站原生简历上传/解析可用时优先使用；关键简历事实回读正确即结束，只有关键冲突才纠正，确定无解析能力或无效果时才自主填写；
- 未知网站先分析并形成候选；公共服务依据多个独立安装实例的核验、成功率、近期性和结构一致性决定发布；
- Recipe 只保存招聘系统身份、粗粒度业务里程碑、当前动作的局部精确守卫、事实 ID、局部后置条件和核验计数，不保存简历事实值或全页标准模型；
- 跨用户公共经验属于一期；商业账号体系、支付、运营后台和 Web 业务入口属于后续服务工程；
- 最终发布与签名在独立发布工程完成。
