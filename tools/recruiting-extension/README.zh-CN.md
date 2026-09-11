# Atoll Recruiting Capture（开发预览）

这是招聘数据产品的可选浏览器采集入口。它把用户在真实岗位列表页或已入库样本岗位详情页确认过的 DOM 结构保存为 Candidate Recipe、结构化 Trace 和证据 Artifact，再通过 Recruiting Actor 的公开 `recruiting.recipe.propose` 命令创建 Draft。它不是每日 Worker，不执行调度，也不能直接发布 Recipe、修改 Assignment 或推进 Checkpoint。

## 组成

- `extension/`：可加载的 Chrome Manifest V3 扩展；只在用户点击后取得当前页 `activeTab` 权限，不申请全部招聘网站访问权限；
- `bridge/`：严格验证浏览器 Draft、从 Atoll 读取 Source/Endpoint 和真实消息发送者、生成三个不可变 Resource，并调用公开 Recipe 提案命令；
- `cmd/recruiting-extension-bridge`：仅监听显式 loopback IP、使用随机插件配对令牌的本机 Bridge 进程；可选启用使用独立 Executor 令牌的 Profile 修复入口。

扩展不能直接连接 Atoll `/ws`。Atoll 的 WebSocket 使用同源 Cookie 和 Origin 检查防止 CSWSH；为扩展放宽该边界会破坏核心安全模型。Bridge 使用普通用户登录会话连接现有协议，扩展只连接 `127.0.0.1`/`::1`。

## 本地运行

先构建：

```bash
make build-go
```

把普通用户密码放进仅当前用户可读的临时文件（Bridge 也接受 `0400`）：

```bash
install -m 0600 /dev/null /tmp/atoll-recruiting-password
```

写入密码后启动 Bridge：

```bash
bin/atoll-recruiting-extension-bridge \
  --atoll http://127.0.0.1:8080 \
  --email operator@example.com \
  --password-file /tmp/atoll-recruiting-password \
  --channel '<包含 Recruiting Actor 的 Channel ID>' \
  --control-actor '<Recruiting Actor ID>'
```

Bridge 在标准输出打印一次 JSON，其中包含随机 `endpoint` 和 `token`。在 Chrome 的扩展管理页选择“加载已解压的扩展程序”，目录指向 `tools/recruiting-extension/extension`，把这两个值填入插件并连接。

需要执行设备绑定的 Profile 修复时，另建一个只允许当前用户读取、内容至少 32 字符的 Executor 令牌文件：

```bash
install -m 0600 /dev/null /tmp/atoll-recruiting-executor-token
```

写入随机令牌后，先为每个受管浏览器目录生成独立的 Profile 绑定令牌，并只把 SHA-256 放进 `0600` registry。`user_data_dir` 必须是绝对、规范、非符号链接且仅当前用户可访问的目录；明文绑定令牌只保存在该受管 Chrome Profile 的插件本地存储中：

```json
{
  "version": "recruiting.browser-profile-registry.v1",
  "profiles": [{
    "profile_id": "profile-company-a",
    "profile_version": 1,
    "security_domain": "jobs.example.com",
    "user_data_dir": "/srv/atoll/browser-profiles/company-a",
    "profile_directory": "Default",
    "binding_token_hash": "sha256:<64 lowercase hex>"
  }]
}
```

给 Bridge 增加 `--executor-token-file /tmp/atoll-recruiting-executor-token --profile-registry /absolute/path/profiles.json`。Bridge 输出会额外包含 `browser_broker_url`，但不会打印 Executor 令牌或 Profile 绑定令牌。必须从 registry 指定的 `user_data_dir/profile_directory` 启动并安装插件，在插件连接区同时填写 `profile_id` 和明文 Profile 绑定令牌；普通 Recipe 捕获连接可把两项都留空。

绑定到该设备、capability 为 `browser.profile.repair` 的同一 `recruiting-executor` class 使用以下配置：

```json
{
  "execution_enabled": true,
  "capability": "browser.profile.repair",
  "browser_broker_url": "http://127.0.0.1:<port>",
  "browser_broker_token_file": "/tmp/atoll-recruiting-executor-token",
  "browser_broker_timeout_ms": 900000,
  "artifact_redaction": "redacted"
}
```

同一设备上执行日常登录态抓取的 `browser.recipe` capability 使用相同 registry：

```json
{
  "execution_enabled": true,
  "capability": "browser.recipe",
  "browser_chrome_path": "/absolute/path/to/chrome",
  "browser_profile_registry": "/absolute/path/profiles.json",
  "artifact_redaction": "redacted"
}
```

其余 control actor 与 Artifact Resource 配置仍按 Recruiting Executor 的既有严格配置提供。插件配对令牌与 Executor 令牌必须不同；两个入口都只接受 loopback 请求。

操作顺序：

1. Listing 模式打开已登记 Source 的确切 active Endpoint；Detail 模式打开一个已入库样本 Job 的确切 `detail_url`（均必须是 HTTPS）；
2. 输入 Source ID、Recipe ID 和新版本；Detail 模式还必须填写样本 Job ID，然后点击“开始捕获”；
3. Listing 依次标注岗位卡片、稳定岗位 ID、标题和详情链接；Detail 至少标注标题和正文，可再标注地点、雇佣类型、发布时间；
4. Listing 明确确认“最新活动时间倒序、更新重新置顶”等 Source 契约；Detail 明确确认当前页与样本 Job 及字段语义；
5. 提交 Draft。Bridge 会重新读取 Source/version/Endpoint 和消息信封身份，再上传 Evidence、Recipe、Capture Resource 并提案；
6. 后续仍必须运行 `recipe.validate`，验证通过后由用户独立审批，插件本身没有发布权限。

Profile 修复流程：运营员通过 `recruiting.profile.repair.begin` 创建一次性会话；控制面只向 Profile 绑定设备派发 `browser.profile.repair` Work；Bridge 只把任务交给持有该 Profile 独立绑定令牌的插件连接。插件提示用户在该安全域完成登录，确认后导航到冻结的 HTTPS canary 地址并执行冻结的 Browser Recipe，只返回 Recipe 身份、记录数和是否通过。成功后 Bridge 以版本围栏原子替换 registry，并提交不含凭证的 opaque `secret://local-browser-profile/...` 引用；独立 verification Work 再次运行 canary并预发布下一 ready 版本，最后仍需既有 RepairIncident validation/resolve 才能恢复调度。插件连接和每日 Browser Runner 共用跨进程目录租约，修复浏览器未退出时日常任务无法并发打开同一凭证目录。

## 安全与限制

- Bridge 拒绝非 loopback 监听地址、非 `chrome-extension://` Origin、错误配对令牌、远程明文 Atoll URL和权限过宽的密码文件；
- 浏览器 Draft 无法声明 `captured_by`、Source version、Endpoint revision、Job version、Resource hash、Recipe status、Assignment 或 Checkpoint；这些事实由 Bridge/Actor 生成或验证。Detail 捕获的页面 URL 必须同时匹配经公开 `job.get` 读取、并在提案事务中再次锁定的同 Source Job；
- 页面证据只保留最多三张岗位卡片的清理后 HTML，删除表单、输入控件、脚本、事件属性、secret-shaped 属性和 URL 查询；
- 新捕获使用 `recruiting.extension-capture.v2` 双重页面围栏；严格解码仍接受原始 v1 Listing 形状，并保持其旧哈希算法，已有不可变 Resource 可安全重放；
- 捕获 UI 生成声明式 HTTP/HTML Listing 或 Detail Recipe；Profile 修复 UI 只能运行控制面冻结、与安全域和精确 canary URL 绑定的 Browser Recipe，不能把一次普通页面加载当作认证证明；
- Profile 修复 Bridge/Executor/插件协议、设备 registry、绑定令牌、版本轮换与日常 Browser Driver provider 已接通，并有纯逻辑、Bridge、Executor、真实 Chrome 本地登录态和非 root MySQL 合同测试；尚未在真实授权登录站点完成整包 Chrome 验收，因此 S12 仍未完成；
- 当前环境的官方 Google Chrome 144 不接受命令行加载 unpacked extension，因此自动门分别验证扩展静态/协议、真实 Chrome DOM 捕获内核和普通用户 Bridge→Atoll 纵向链路。整包自动加载门需在 Chrome for Testing 或 Chromium 执行，不能用放宽 Atoll Origin 检查替代。

## 验证

```bash
make recruiting-extension-test
RECRUITING_CHROME_BIN=/absolute/path/to/chrome-for-testing make recruiting-extension-bundle-test
make recruiting-extension-live-test
./scripts/recruiting-boundary-check.sh a94d2b8d
```

整包门要求 Chrome for Testing 或允许命令行加载 unpacked extension 的 Chromium：测试从真实 Manifest V3 启动 Service Worker，打开真实 popup，填写 Bridge 与受管 Profile 绑定并点击连接，随后同时检查 UI “已连接”、Bridge 的 Profile 定向连接和每日 Runner 的跨进程互斥。真实站点测试访问 Discord 的公开 Greenhouse 岗位列表页及其第一条真实详情页，使用同一选择器/Recipe 生成内核确认 `tr.job-post` 能提取多条岗位、稳定详情 URL 和标题，并从详情页提取标题与正文；它只证明捕获机制与真实 DOM，不代替 Source 活动倒序契约或候选 Recipe 发布验证。
