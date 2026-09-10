package narrative

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"os"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/goccy/go-yaml"
)

const (
	defaultWriterEndpoint = "http://127.0.0.1:8000/v1/responses"
	maxWriterResponse     = 2 << 20
)

type proseGenerator interface {
	Generate(context.Context, writeRequest) (generatedProse, error)
}

type proseCritic interface {
	Critique(context.Context, chapterPlan, []fact, chapter) ([]string, error)
}

type generatedProse struct {
	Title            string            `json:"title"`
	POV              string            `json:"pov"`
	Markdown         string            `json:"markdown"`
	FactRealizations map[string]string `json:"fact_realizations"`
}

type responsesProseGenerator struct {
	Endpoint      string
	APIKey        string
	Model         string
	AuthorContext string
	Client        *http.Client
}

func newProseGeneratorFromEnvironment(bundle string) (proseGenerator, error) {
	key := strings.TrimSpace(os.Getenv("NARRATIVE_WRITER_API_KEY"))
	if key == "" {
		key = strings.TrimSpace(os.Getenv("PROXY_API_KEY"))
	}
	if key == "" {
		return nil, nil
	}
	endpoint := strings.TrimSpace(os.Getenv("NARRATIVE_WRITER_URL"))
	if endpoint == "" {
		endpoint = defaultWriterEndpoint
	}
	model := strings.TrimSpace(os.Getenv("NARRATIVE_WRITER_MODEL"))
	if model == "" {
		model = strings.TrimSpace(os.Getenv("CODEX_DEFAULT_MODEL"))
	}
	context, err := loadWriterContext(bundle)
	if err != nil {
		return nil, err
	}
	return &responsesProseGenerator{
		Endpoint: endpoint, APIKey: key, Model: model, AuthorContext: context,
		Client: &http.Client{Timeout: 5 * time.Minute},
	}, nil
}

func (g *responsesProseGenerator) Generate(ctx context.Context, request writeRequest) (generatedProse, error) {
	prompt, err := buildWriterPrompt(g.AuthorContext, request)
	if err != nil {
		return generatedProse{}, err
	}
	output, err := g.respond(ctx, "你是中文长篇小说的章节写作者。只输出严格 JSON，不使用代码围栏，不解释写作过程。", prompt)
	if err != nil {
		return generatedProse{}, err
	}
	var prose generatedProse
	if err := json.Unmarshal([]byte(stripJSONFence(output)), &prose); err != nil {
		return generatedProse{}, fmt.Errorf("decode prose JSON: %w", err)
	}
	if strings.TrimSpace(prose.Title) == "" || strings.TrimSpace(prose.POV) == "" || utf8.RuneCountInString(prose.Markdown) < 1200 {
		return generatedProse{}, errors.New("prose JSON omitted title, POV, or a chapter-length markdown body")
	}
	if err := g.repairFactRealizations(ctx, request.Events, &prose); err != nil {
		return generatedProse{}, err
	}
	return prose, nil
}

func (g *responsesProseGenerator) repairFactRealizations(ctx context.Context, events []fact, prose *generatedProse) error {
	for attempt := 0; attempt < 2; attempt++ {
		missing := unverifiableRealizations(events, prose.Markdown, prose.FactRealizations)
		if len(missing) == 0 {
			return nil
		}
		eventsRaw, _ := json.MarshalIndent(missing, "", "  ")
		prompt := fmt.Sprintf(`从下面已经完成的小说正文中，为指定事实重新抽取证据锚点。不得改写正文，不得概括或拼接。

输出严格 JSON：{"fact_realizations":{"event_id":"正文中原样连续复制的12–80字"}}。
每个值必须是正文中真实存在的一段连续文字；引号、标点、换段也尽量原样复制。只能包含指定 event_id。

指定事实：
%s

正文：
%s`, eventsRaw, prose.Markdown)
		output, err := g.respond(ctx, "你是小说事实证据抽取器，只复制原文，不创作。只输出严格 JSON。", prompt)
		if err != nil {
			return fmt.Errorf("repair fact realizations: %w", err)
		}
		var repaired struct {
			FactRealizations map[string]string `json:"fact_realizations"`
		}
		if err := json.Unmarshal([]byte(stripJSONFence(output)), &repaired); err != nil {
			return fmt.Errorf("decode repaired fact realizations: %w", err)
		}
		if prose.FactRealizations == nil {
			prose.FactRealizations = map[string]string{}
		}
		for _, event := range missing {
			if excerpt := repaired.FactRealizations[event.EventID]; containsEvidenceExcerpt(prose.Markdown, excerpt) {
				prose.FactRealizations[event.EventID] = excerpt
			}
		}
	}
	missing := unverifiableRealizations(events, prose.Markdown, prose.FactRealizations)
	if len(missing) > 0 {
		return fmt.Errorf("fact evidence extraction failed for %s", missing[0].EventID)
	}
	return nil
}

func unverifiableRealizations(events []fact, markdown string, realizations map[string]string) []fact {
	missing := make([]fact, 0)
	for _, event := range events {
		excerpt := strings.TrimSpace(realizations[event.EventID])
		length := utf8.RuneCountInString(excerpt)
		if length < 12 || length > 80 || !containsEvidenceExcerpt(markdown, excerpt) {
			missing = append(missing, event)
		}
	}
	return missing
}

func (g *responsesProseGenerator) Critique(ctx context.Context, plan chapterPlan, events []fact, draft chapter) ([]string, error) {
	planRaw, _ := json.MarshalIndent(plan, "", "  ")
	eventsRaw, _ := json.MarshalIndent(events, "", "  ")
	prompt := fmt.Sprintf(`独立审阅下面这一章。不要改写正文，只判断它是否达到可公开发表的小说章节标准。

判定为不通过的情况包括：段落像独立事件卡而非连续场景；动作之间缺少可见因果；POV 越界；人物性别、称谓、空间位置或前后状态矛盾；对白像作者说明、人物自指异常或不符合其说话方式；直接照抄事实摘要；为连接情节擅自新增关键事实；结尾没有状态变化；语言重复、机械或莫名其妙。

输出严格 JSON：{"pass":true|false,"issues":["具体且可执行的整章返修意见"]}。只有不存在实质问题时才能 pass=true。issues 最多 6 条。

作者契约与公开人物卡：
%s

场景计划：
%s

权威事实：
%s

待审正文：
%s`, g.AuthorContext, planRaw, eventsRaw, draft.Markdown)
	output, err := g.respond(ctx, "你是独立的中文小说责任编辑，不参与写作。只输出严格 JSON。", prompt)
	if err != nil {
		return nil, err
	}
	var result struct {
		Pass   bool     `json:"pass"`
		Issues []string `json:"issues"`
	}
	if err := json.Unmarshal([]byte(stripJSONFence(output)), &result); err != nil {
		return nil, fmt.Errorf("decode critique JSON: %w", err)
	}
	if result.Pass {
		return nil, nil
	}
	if len(result.Issues) == 0 {
		return []string{"independent prose critic rejected the draft without details"}, nil
	}
	if len(result.Issues) > 6 {
		result.Issues = result.Issues[:6]
	}
	return result.Issues, nil
}

func (g *responsesProseGenerator) respond(ctx context.Context, instructions, input string) (string, error) {
	body := map[string]any{"model": g.Model, "instructions": instructions, "input": input}
	raw, err := json.Marshal(body)
	if err != nil {
		return "", err
	}
	httpRequest, err := http.NewRequestWithContext(ctx, http.MethodPost, g.Endpoint, bytes.NewReader(raw))
	if err != nil {
		return "", err
	}
	httpRequest.Header.Set("Authorization", "Bearer "+g.APIKey)
	httpRequest.Header.Set("Content-Type", "application/json")
	response, err := g.Client.Do(httpRequest)
	if err != nil {
		return "", fmt.Errorf("call prose model: %w", err)
	}
	defer response.Body.Close()
	responseRaw, err := io.ReadAll(io.LimitReader(response.Body, maxWriterResponse+1))
	if err != nil {
		return "", fmt.Errorf("read prose model: %w", err)
	}
	if len(responseRaw) > maxWriterResponse {
		return "", errors.New("prose model response exceeds 2 MiB")
	}
	if response.StatusCode < 200 || response.StatusCode >= 300 {
		return "", fmt.Errorf("prose model status %d: %s", response.StatusCode, boundedText(responseRaw, 500))
	}
	var envelope struct {
		Output []struct {
			Content []struct {
				Type string `json:"type"`
				Text string `json:"text"`
			} `json:"content"`
		} `json:"output"`
	}
	if err := json.Unmarshal(responseRaw, &envelope); err != nil {
		return "", fmt.Errorf("decode prose model envelope: %w", err)
	}
	var output string
	for _, item := range envelope.Output {
		for _, content := range item.Content {
			if content.Type == "output_text" {
				output += content.Text
			}
		}
	}
	if strings.TrimSpace(output) == "" {
		return "", errors.New("prose model returned no output text")
	}
	return output, nil
}

func buildWriterPrompt(constitution string, request writeRequest) (string, error) {
	planRaw, err := json.MarshalIndent(request.Plan, "", "  ")
	if err != nil {
		return "", err
	}
	eventsRaw, err := json.MarshalIndent(request.Events, "", "  ")
	if err != nil {
		return "", err
	}
	var revision strings.Builder
	var continuity strings.Builder
	if request.PreviousChapter != nil {
		continuity.WriteString("\n上一章连续性锚点（承接状态与语气，不复述）：\n")
		continuity.WriteString(chapterTail(request.PreviousChapter.Markdown, 1200))
		continuity.WriteString("\n")
	}
	if request.Prior != nil {
		revision.WriteString("\n上一版全文（必须整章重写，不要局部打补丁）：\n")
		revision.WriteString(request.Prior.Markdown)
		revision.WriteString("\n审稿问题：\n")
		issues, _ := json.Marshal(request.Issues)
		revision.Write(issues)
	}
	return fmt.Sprintf(`写《名册之外》第 %d–%d 日形成的一章，目标为 1800–3000 个汉字。

硬约束：
1. 只写一个连续场景弧，不按事件逐条写纪要；相邻段落必须由动作、反应、阻碍或后果连接。
2. 严格服从 source_events 中已经发生的事实和实际后果，不新增能改变产权、身份、伤亡、证据真伪或制度裁决的事实。
3. 以 scene_plan 的 POV 限制感知；人物不能知道其未观察到的私密信息。心理活动只属于 POV 人物，其他人的动机通过行为、语言和可见细节表现。严格遵守人物性别、称谓和说话方式。
4. 必须依次完成 setup、pressure/escalation、turn、resolution；结尾的 resolution 可以是局势被重新定义或新的不可逆约束，不要求解决争议。
5. 具体物件、身体、空间和行动先于抽象解释；对白服务于争夺，不讲系统原理。
6. 不出现 Agent、actor、prompt、模拟器、event_id 等技术词，不照抄事实摘要，不使用“这一步推动了局面”等解释性模板句。
7. 输出 JSON 对象，且只能包含 title、pov、markdown、fact_realizations。markdown 从“# 第X章 标题”开始。fact_realizations 必须为对象：每个 source event_id 恰好一个键，值是从 markdown 原样复制的 12–80 字连续片段，指出该事实如何在正文中被戏剧化。不得用正文之外的说明充数。

作者契约：
%s

scene_plan：
%s

source_events：
%s
%s%s`, request.Plan.StartDay, request.Plan.EndDay, constitution, planRaw, eventsRaw, continuity.String(), revision.String()), nil
}

func chapterTail(markdown string, limit int) string {
	runes := []rune(strings.TrimSpace(markdown))
	if len(runes) <= limit {
		return string(runes)
	}
	return string(runes[len(runes)-limit:])
}

func loadWriterContext(bundle string) (string, error) {
	constitution, err := os.ReadFile(bundle + "/constitution.yaml")
	if err != nil {
		return "", fmt.Errorf("read author contract: %w", err)
	}
	castRaw, err := os.ReadFile(bundle + "/cast.yaml")
	if err != nil {
		return "", fmt.Errorf("read public cast: %w", err)
	}
	var cast struct {
		Characters []struct {
			ID             string         `yaml:"id" json:"id"`
			Name           string         `yaml:"name" json:"name"`
			Age            int            `yaml:"age" json:"age"`
			PublicIdentity string         `yaml:"public_identity" json:"public_identity"`
			PublicGoal     string         `yaml:"public_goal" json:"public_goal"`
			EmbodiedState  map[string]any `yaml:"embodied_state" json:"embodied_state"`
			Voice          string         `yaml:"voice" json:"voice"`
		} `yaml:"characters" json:"characters"`
	}
	if err := yaml.Unmarshal(castRaw, &cast); err != nil {
		return "", fmt.Errorf("decode public cast: %w", err)
	}
	publicCast, err := json.MarshalIndent(cast, "", "  ")
	if err != nil {
		return "", err
	}
	return string(constitution) + "\n\n公开人物卡（不包含秘密与私有目标）：\n" + string(publicCast), nil
}

func stripJSONFence(value string) string {
	value = strings.TrimSpace(value)
	if strings.HasPrefix(value, "```") {
		value = strings.TrimPrefix(value, "```json")
		value = strings.TrimPrefix(value, "```")
		value = strings.TrimSuffix(strings.TrimSpace(value), "```")
	}
	return strings.TrimSpace(value)
}

func boundedText(value []byte, limit int) string {
	if len(value) <= limit {
		return string(value)
	}
	return string(value[:limit]) + "…"
}
