# 《名册之外》演化叙事包

这里存放的不是章节大纲，而是一套能够产生虚构历史的初始条件。人物在有限信息、制度约束、资源压力和既有关系中行动；系统记录发生过的事实，作者再从中筛选、编排并改写成小说。

## 核心命题

> 战乱之后，人们争夺的不是唯一的真相，而是谁有权让哪一种过去在新的秩序中生效。

同治六年（1867），浙西虚构县份澜溪县开始重造战后残缺的鱼鳞图册。归鹭洲必须在三十日内完成第一轮土地申报和争议清册。旧官册残损，但私人契据、税票、族谱、界石、耕作痕迹和人的记忆保存得并不均等。新册不会神奇地创造真相，却会决定谁纳税、谁能过割土地、谁的争讼会被受理，以及谁暂时留在制度之外。

## 文件

- `constitution.yaml`：作者契约和不可被临场改写的世界宪法。
- `world.yaml`：时间、空间、制度、资源、信息传播与外部压力。
- `cast.yaml`：首轮六名核心人物及其非公开状态、关系和矛盾。
- `dynamics.yaml`：可执行动作、因果传播、导演边界、记忆和叙事筛选规则。
- `simulation.yaml`：跨日期复用的环境过程与人物行动策略，不包含某一日的预写剧情。
- `scenarios/day-01.yaml`：第一轮四小时演化实验的种子状态。

## 运行时分层

```text
作者层：主题、禁区、叙事引力点、视角与取舍
    ↓ 只投放压力和机会，不命令人物作选择
世界层：天气、空间、制度、物资、价格、时限、证据
    ↕
人物层：欲望、信念、记忆、关系、身体、行动
    ↓
事实账：客观事件及实际后果（只追加）
    ↓
叙事层：筛选事件、控制披露、组织场景、文学改写
```

必须同时维护三种互不覆盖的事实：

1. `world_fact`：世界中实际发生的事。
2. `character_belief`：某个人认为发生了什么。
3. `reader_disclosure`：作品在某一页之前允许读者知道什么。

角色不能读取全局事实账。角色只能依据亲历、感官、文书、他人转述以及自己的推断行动。

## 与 Atoll 的对应关系

Atoll 是承载演化过程的后台，不进入小说表层：

- actor 是人物、环境过程、制度过程或导演的技术载体；小说人物仍然是人。
- channel 是一个有共同可见范围的社会场所，例如册局、茶棚、渡口或某户内室。
- membership 表示谁在场、谁可能听见、谁能采取何种制度动作。
- append-only ledger 保存不可悄悄改写的客观事件。
- actor 私有状态保存人物的记忆、误解、秘密和意图。
- timer 推进汛情、期限、伤病、饥饿、作物和公务节律。

不要把一个人物永久绑定到一个频道；人物会移动，消息会失真，缺席本身也会产生后果。

## 第一阶段成功标准

首轮实验不是生成一章成稿，而是验证：

- 至少三次行动来自人物自身目标，而非导演命令。
- 至少一个微小环境变化经过两层以上传导，改变人物选择。
- 同一事件在三名人物记忆中形成不同版本。
- 至少一个决定产生不可轻易撤销的社会后果。
- 去掉系统解释后，筛选出的事件仍能组成一个有欲望、阻碍、选择和转折的场景。

## 运行第一日实验

在仓库根目录执行：

```bash
go run ./cmd/narrative-sim \
  --bundle narrative \
  --out narrative/runs/day-01
```

输出包括：

- `ledger.jsonl`：只追加语义的客观事实账，每条事件带原因、意图、实际后果和观察者。
- `memories.json`：沈砚秋、何阿生、林素云各自能记住的版本。
- `beliefs.json`：逐事件观察累积出的角色私有事实；未观察到的事实不会进入角色决策。
- `proposals.json`：人物行动前使用的信念快照、候选行动、效用与最终选择。
- `state-diff.json`：土地、税粮、债务、制度队列和人物身份的前后差异。
- `scene-material.md`：只使用已发生事实整理出的场景发动机、披露纪律和余波，不冒充小说成稿。
- `summary.json`：本轮是否达到终止条件以及种子事件、演化事件数量。

当前运行器是确定性的第一块内核：相同设定必定得到相同事实账，便于测试和回放。每个 `decision_point` 同时声明多个可行动选项，运行器根据角色在该时刻的效用来源选出一项，并把选中分数和落选分支一起记入事实账；改变人物权重就会改变历史，而不必修改程序中的剧情。

这个命令仍是离线参考模型：候选行动由场景设计者提供，用于确定性回归和重放。正式运行时由下文的独立 Atoll 人物 actor 根据各自私有信念提出行动，再由世界 actor 验证并结算；离线事实账格式继续作为对照格式保留。

## Atoll 原生演化（正式运行时）

`cmd/narrative-sim` 是确定性参考模型；正式演化从第二日状态迁入 Atoll。运行节点后执行：

```bash
make build-go narrative-atoll
bin/narrative-atoll --bundle narrative --through 5
```

命令使用节点的本地 bearer token，声明七类原生 actor，把世界、环境、规划、写作、审稿和投影 actor 放在 `c0`，并为每个人物创建独立的 `c0.narrative-<人物>` 私有子频道：

- `narrative-world` 是 server-placed 世界 actor，持有全局世界状态、验证人物提案、结算后果，并可用 durable timer 推进日期。
- `narrative-environment` 是独立的连续系统 actor，根据权威世界快照提出天气、生态、疾病、市场和制度等状态迁移，不直接写世界。
- `narrative-character` 是每名人物各自的 server-placed agent actor。每个实例只在自己的子频道和 actor state 中保存信念、来源和已经采取的行动，其他人物既不在该频道名册中，也不能读取其账本。
- `narrative-planner` 把带 `cause_event_ids` 的事实编译成 setup、pressure、escalation、turn、resolution 场景节拍；计划不完整时不进入写作。
- `narrative-writer` 以不可变场景计划整章写作，并把同一任务的草稿按版本保存在 actor state 中；模型输入只含作者契约、脱敏人物卡、公开事实、上一章连续性锚点、上一版草稿和审稿问题，不读取角色私有账本。正文生成后，单独的只读抽取阶段修复事实证据锚点，抽取错误不占用文学返修版本。
- `narrative-critic` 先以确定性规则检查事实覆盖、可验证正文落点、因果前件、篇幅和模板语言，再由独立模型检查 POV、人物一致性、空间连续性、对白和段落因果；两层都通过才算通过。
- `narrative-projector` 只负责协调已经完成的公开事实集：按最多六条、以人物行动收束、且存在一个角色能够合法感知全部事件的连续场景批次调用 planner、writer、critic；POV 按观察覆盖率和实际参与度选择。后续事实留在权威队列中，不会因单章失败无限膨胀。每个批次最多返修三版；只有审稿通过才发出 `narrative.chapter`，失败草稿和审稿记录仍可审计，但不会进入小说正文。
- `narrative.fact` 只写入 `c0` 世界账本，没有角色能够覆盖它；角色频道里不复制全局事实账。
- 世界 actor 以每条事实为父消息，穿过 Atoll channel membrane，逐个调用观察者频道暴露的 `narrative.observe`。因此“事实发生”和“某人得知事实”分属世界与人物两本账，但保持可审计的因果链。
- 每个 tick，环境与人物提交包含行动、目标、前提和预期效果的结构化意图；世界重新依据真实状态裁决，并把接受和拒绝都记录为 `narrative.decision`。私有 `known_facts` 不写入公开决策。

章节不是把事实逐条扩写。正式链路为：

```text
narrative.fact（事实及真实因果来源）
  → narrative.scene.plan（整章戏剧问题与场景节拍）
  → narrative.chapter.draft（同一任务的版本化草稿）
  → narrative.chapter.review（独立质量判定与返修意见）
  → narrative.chapter（仅发布通过版本）
```

任务 ID 由源事实 ID 确定生成，因此重试和节点重启后仍属于同一章；planner、writer、critic 的计划、草稿和审稿结果分别持久化。生成正文必须为每条源事实提供一段确实存在于正文的原句，同时禁止直接复制事实摘要；服务端会对不实摘录做最多两次只读重抽取，仍不可定位就失败关闭。这让 critic 能区分“事实写进元数据”和“事实真正被戏剧化”。当前两个已校准场景仍作为确定性写作基线；模型不可用或三次审稿仍失败时，素材保留在有界场景队列中，不以事件纪要冒充小说。

使用 Atoll 的持久 timer 推进下一日：

```bash
bin/narrative-atoll --bundle narrative --schedule 5s
```

查看 Atoll 中的持久世界状态而不推进：

```bash
bin/narrative-atoll --bundle narrative --status
```

启动一个有上限、可中止的 durable 自动演化循环：

```bash
bin/narrative-atoll --bundle narrative --auto-through 12 --interval 1m --max-empty-days 2
bin/narrative-atoll --bundle narrative --stop-auto
```

循环到目标日即停止；连续空转达到上限也会停止。运行位置、计时器和停止条件都在世界 actor state 中持久化，节点重启后不会丢失边界。

默认从第二日迁移，是因为前两日仍是作者校准场景；第三日起的世界事件、人物提案、定向观察、响应和完成事件都必须先写入 Atoll 的频道 SQLite 账本。离线 `runs/day-03` 以后内容只作为回归基准，不再是运行时真相。

### 通用内生机制

`simulation.yaml` 不限于小说情节变量；天气、生态、疾病、价格、库存、制度队列、空间位置、身体状态、关系与声誉都使用同一套状态/事件契约。规则默认只发生一次；连续系统放在 `recurring_processes`，可声明 `cooldown_days` 和 `max_firings`，每次是否继续由最新世界状态决定。

条件支持 `exists`、`not_exists`、`eq`、`neq`、`gt/gte`、`lt/lte`；效果支持 `set`、`add`、`multiply` 和 `delete`。人物选项的基础效用之外可以声明 `utility_modifiers`：只有角色自己已经观察到的资源、关系或制度事实满足条件时，修正值才进入选择。因此同一外部局面会因人物经历不同而产生不同提案；提案仍须由世界 actor 按真实状态复核，错误认知不能直接改写世界。

这使系统具备“状态 → 环境过程 → 局部观察 → 受经历影响的行动 → 新状态”的闭环。它仍是可配置的因果模型，不假装自动知道任意现实领域的规律；新背景需要提供相应状态、过程和可行动作，开放式模型生成可以作为后续的提案来源，但不能绕过世界裁决。

外部的人、传感器或其他 Atoll 组织通过同一个世界入口施加影响，而不是直接写数据库。准备一份刺激事件：

```json
{
  "source": "county-weather-station",
  "location": "上游",
  "action": "发布上游雨情",
  "summary": "未来半日不会再降雨。",
  "actual_effect": "复测当天的涨水风险降低。",
  "observers": ["shen_yanqiu", "zhou_jiren"],
  "effects": [
    {"key": "weather.upstream_forecast", "op": "set", "value": "stable_half_day"}
  ]
}
```

然后执行 `bin/narrative-atoll --bundle narrative --inject stimulus.json`。世界 actor 会校验状态操作，把事件标记为 `origin: external` 写入 `c0`，并只向列出的角色投递观察；未列出的角色不会凭空知道。后续内生规则可以把这条外部事实作为条件或效用修正来源。

## 局域网站点

先让 Atoll 原生运行时完成迁移和演化，再启动固定端口的网站：

```bash
bin/narrative-atoll --bundle narrative --through 5
make narrative-serve
```

服务监听 `0.0.0.0:8841`。本机使用 `http://127.0.0.1:8841/`；同一局域网中的访客使用 `http://<本机局域网IP>:8841/`。页面的 `/api/public-runs` 直接读取 Atoll `c0` 中的 `narrative.fact`、`narrative.decision`、通过审稿的 `narrative.chapter` 和安全的自动运行状态，并每 15 秒刷新；第一、二日明确标记为作者校准。第三日起，章节以完整叙事流水线的发布输出为准，事实仍是最终权威。完整小说视图会把历史短日稿按场景弧合章；逐日模式仍保留，便于核对每个事实来源。

已存在的账本可以在不重跑世界、不改变任何事实的情况下重新投影：

```bash
bin/narrative-atoll --bundle narrative --reproject-from 6 --through 7
```

Web 不再静态暴露 `runs/`。角色频道内的 beliefs、memories、proposals 和观察账本均不进入公网；浏览器只能得到客观事实、公开世界设定和候选章节。若 Atoll 使用非默认 home，可用 `NARRATIVE_ATOLL_HOME=/path/to/server make narrative-serve` 指定。

## 从最新状态自主续演

前两日是用于校准世界与人物的人工场景。此后可以不再新增 `day-03.yaml`，直接从事实账的最新状态继续：

```bash
go run ./cmd/narrative-sim --bundle narrative --continue 1 --out narrative/runs
```

运行器会逐个时间刻检查 `simulation.yaml` 中可复用的环境过程与人物策略。环境过程读取客观世界；人物 actor 只能读取自己从历史观察中累积的私有信念。满足信念条件时，人物从可用行动能力中形成提案，按自己的效用选择，随后才由世界结算实际后果。新事实只进入在场者的信念与记忆，同时写入全局因果账，最后才被叙事层改写成候选章节。
