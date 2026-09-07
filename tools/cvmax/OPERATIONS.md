# CvMax 安装、升级与诊断

CvMax 作为 Atoll 应用安装到用户已有的私有频道。它复用 Atoll 的身份、频道、Hosted Agent、MCP 成员和生命周期，不启动第二套调度或身份。CvMax 公共经验服务是应用后端，只负责发布无事实值配方和接收匿名核验反馈。Web 入口留待后续，本期入口是外部 AI 中的唯一 CvMax Skill 和配对的 Chrome 插件。

## 前置条件

- Node.js 22 或更高版本；
- 一个正在运行且可访问的 Atoll 节点、用户私有频道和该用户的访问凭据；
- Chrome 已通过开发者模式加载 `extension/`，并完成一次本机服务配对；
- 服务配置中的 `allowedOrigins` 只包含用户允许操作的招聘站点；
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

Registry 默认只监听 `127.0.0.1`。生产部署应放在 HTTPS 网关后，并使用独立安装凭据替换当前开发写入 token。Service 私有配置中的 `experienceRegistry` 包含 Registry URL、安装 ID、写入凭据、可信公钥映射、缓存文件和 TTL；插件与 Skill 不持有数据库账号或签名私钥。Registry 暂时不可达时，候选和反馈写入最多 500 条的本地持久队列，恢复后按原回执 ID 重放；页面填写不等待后台写入恢复。

仓库开发入口：

```text
node /absolute/repo/tools/cvmax/src/cli.mjs install --config /absolute/private/client.json
```

`install` 是幂等操作。它通过 Atoll 的公开声明和频道 overlay 安装或更新 `cvmax-agent` 与 `cvmax-service`；配置完全相同时返回空的 `restarted` 列表，不重启成员。已有任务保存在应用状态文件中；升级重启后，确定未派发或已经验证的工作进入安全接续，已发送但结果不明的动作进入只读核对。

只有 Chrome 扩展代码发生变化时才更新 `extension/manifest.json` 的版本，并让用户在 `chrome://extensions` 手动重新加载。Agent、Client 或 Service 更新不要求重载插件。

## 使用

所有命令从标准输入读取一个 JSON 对象：

```text
printf '%s' '<resume-json>' | node /absolute/repo/tools/cvmax/src/cli.mjs resume-save --config /absolute/private/client.json
printf '%s' '{}' | node /absolute/repo/tools/cvmax/src/cli.mjs resume-list --config /absolute/private/client.json
printf '%s' '<request-json>' | node /absolute/repo/tools/cvmax/src/cli.mjs submit --config /absolute/private/client.json
printf '%s' '{"taskId":"<id>"}' | node /absolute/repo/tools/cvmax/src/cli.mjs status --config /absolute/private/client.json
```

`resume-save` 接收 `{saveRequestId, name, projectionFacts, resumePath, resumeId?, expectedRevision?}`。首次保存返回 `resumeId` 和 revision；修改已有简历生成新 revision。重复执行使用相同 `saveRequestId`。

`submit` 必须且只能选择一种入口：`links` 或 `currentPage: true`。正常请求指定 `{resumeId, resumeRevision, taskAnswers?}`；Client 先调用 `request_save` 暂存授权范围，再只把 `clientRequestId` 委托给 Hosted Agent。Agent 用 `request_get` 读取不含事实原值的范围，`create` 从服务内暂存记录冻结资料。服务冻结该简历版本，不能从其他简历补值。多链接会先去重再冻结顺序，Hosted Agent 串行处理。超时后使用相同 `clientRequestId` 查询或重试，不能另建任务。`continue` 接续排队、待检查或待只读核对的任务；`resume` 只用于用户明确暂停的任务。

## 诊断

```text
node /absolute/repo/tools/cvmax/src/cli.mjs doctor --config /absolute/private/client.json
```

正常结果应同时显示 Atoll 已连接、频道已附着、Service 和 Agent 可用、Browser 已连接，并始终显示 `finalSubmitAllowed: false`。

故障处理按当前状态逐步进行：

1. `service_unavailable` 或 `agent_unavailable`：重新执行一次幂等 `install`，再运行 `doctor`。
2. `browser: disconnected`：确认 Chrome 扩展已加载且配对服务地址正确；只有扩展版本变化时才重新加载。
3. `needs_reconcile`：执行 `continue`，由 Hosted Agent 先做只读观察；不得重复未知动作。
4. `waiting_user`：只完成当前提示的一个页面动作或一小组相关事实，再用 `answer` 接续同一 taskId。
5. `cancelled` 或 `finished`：属于终态；若有新需求，使用新的 `clientRequestId` 创建新任务。

## 当前边界

- 不点击最终投递，不代替用户接受隐私政策或法律声明；
- 证件号等敏感信息由用户直接在招聘页面输入，不通过对话传递；
- 网站原生简历解析可用时优先使用，随后仍由 CvMax 回读、纠错和补填；
- 未知网站先分析并形成候选；公共服务依据多个独立安装实例的核验、成功率、近期性和结构一致性决定发布；
- 经验只保存页面家族、字段语义、动作类型、事实 ID 和核验计数，不保存简历事实值；
- 跨用户公共经验属于一期；商业账号体系、支付、运营后台和 Web 业务入口属于后续服务工程；
- 最终发布与签名在独立发布工程完成。
