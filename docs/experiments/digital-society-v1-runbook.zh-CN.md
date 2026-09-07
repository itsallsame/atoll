# 数字社会第一期：运行手册

状态：第一期可运行交付  
模型版本：`digital-society-v1.1`

## 一条命令启动实验室

在仓库根目录运行：

```bash
go run ./cmd/atoll up \
  --dir /tmp/atoll-society-lab \
  --addr 127.0.0.1:8832 \
  --root-password root
```

打开 `http://127.0.0.1:8832/society/` 即可进入公共世界，不需要注册、登录或开放节点注册。所有浏览器观察同一个社会，公众页只提供观看、个体查阅和轻度投票，不再提供创建私有世界、运行控制或直接干预。该命令使用临时目录示例；不要把真实节点目录替换成 `/tmp` 后当作长期数据存储。

## 公民议会玩法闭环

每期议事持续 30 个模拟日，当前演示中每 30 秒推进一个模拟日。参与者先选择方案，再把 10 张预算票分配给“公共 AI 基础设施”“职业转型基金”“全民 AI 分红”；10 票全部分完才可提交。选票进入节点唯一的公共计票池，同一浏览器每期只能提交一次。

到期会自动封票。胜出方案立即变成下一阶段的确定性政策干预，并写入公共世界的持久快照与模型事件：

- 公共 AI 基础设施：透明度增加 15 个百分点，上限 100%；
- 职业转型基金：税率增加 3 个百分点，每日福利增加 250 分；
- 全民 AI 分红：税率增加 6 个百分点，每日福利增加 500 分。

平票按“公共 AI → 转型基金 → 全民分红”的固定顺序裁决，以保证复现一致；如果一票都没有，政策保持不变。封票后页面立即显示上期胜出方案、政策已生效提示，并开启下一期，因此可连续观察“公众选择 → 制度变化 → 个体与宏观后果 → 再次选择”的循环。

浏览器只保存由服务器签发的匿名 `HttpOnly` 参与标识，不保存账户、邮箱或密码。该标识用于限制普通重复投票，不是强身份认证；公开部署还应在入口层增加 IP 限频和异常票检测。

## 命令行批量实验

单个世界：

```bash
go run ./cmd/society run --variant A --days 3650 --seed 20260906 --out /tmp/society-a
```

四组同种子对照：

```bash
go run ./cmd/society compare --days 3650 --seed 20260906 --out /tmp/society-comparison
```

十个种子的敏感性检查；`--peak-pressure-bps` 可做单变量冲击强度扫描：

```bash
go run ./cmd/society sweep --days 3650 --seed-start 20260906 --seeds 10 --peak-pressure-bps 6000 --out /tmp/society-shock-6000
go run ./cmd/society sweep --days 3650 --seed-start 20260906 --seeds 10 --peak-pressure-bps 10000 --out /tmp/society-shock-10000
```

`run` 导出 `snapshot.json`、`summary.json`、`metrics.csv`、`events.csv` 和 `transactions.csv`；`compare` 另含总表；`sweep` 生成逐运行与按制度组汇总的 CSV/JSON。快照包含居民当日收入、债务和当前策略，指标包含每日税收与福利流量。金额单位是分，比例字段统一为 `[0,1]`。

## Atoll 控制词

`society` tool actor 提供：

- 状态与历史：`status`、`snapshot`、`history`、`recent`、`trace`、`export`；
- 运行控制：`start`、`pause`、`resume`、`step`、`speed`、`stop`、`reset`；
- 实验干预：`intervene`。

完整词形均为 `society.experiment.<word>`。改变状态的请求使用唯一 `command_id`；重复提交不会重复生效。每个逻辑日生成 `society.audit.day`，持久 outbox 允许崩溃后按稳定 bundle ID 重投。

上述 actor 控制词属于研究入口。公众页面只调用匿名 `POST /api/society`，可读取公共快照、历史、关系与个体，并提交当期选票；它不能暂停、重置、提前封票或直接施加干预。

## 验证

```bash
go test ./drivers/tools/society/...
go test ./web/societyconsole ./drivers/gateway/portal
go test ./e2e -run '^TestSocietyExperimentHumanJourney$' -count=1 -v
```

端到端测试会占用本机临时回环端口，并启动两个真实 Atoll 进程来验证重启恢复。

## 排障

- 页面 404：确认运行的是包含当前源码的 `cmd/atoll`，路径必须为 `/society/`。
- 看不到实验：先用 A/B/C/D 按钮创建；已有同组实验时控制台会复用。
- 历史图空白：世界至少推进一个逻辑日。
- 停止后不能继续：`stop` 是终态，请执行重置。
- CSV 很大：逐居民交易是完整历史；浏览器导出为最多 1000 个指标点和最近 500 条审计证据的便携包。
- 结果不能复现：核对 `model_version`、`build_version`、完整配置、种子和干预列表。

## 研究边界

这是机制研究原型，不是现实政策预测器。住房、公共服务、信贷、出生、死亡和迁移均为显式但高度简化的规则；默认参数尚未按现实数据估计。
