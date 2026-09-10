package narrative

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

func TestResponsesProseGeneratorSendsRevisionAndDecodesContract(t *testing.T) {
	events := []fact{{EventID: "E1", Summary: "水退了", ActualEffect: "旧沟显露"}}
	longBody := "# 第一章 水线\n\n" + strings.Repeat("泥上的旧沟把两个人引到同一棵树下。他们沿着水线争执，又因县里的期限不得不共同封存木尺。\n\n", 35)
	realization := "泥上的旧沟把两个人引到同一棵树下"
	server := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
		if request.Header.Get("Authorization") != "Bearer secret" {
			t.Fatal("missing bearer token")
		}
		var body struct {
			Input string `json:"input"`
		}
		if err := json.NewDecoder(request.Body).Decode(&body); err != nil {
			t.Fatal(err)
		}
		if !strings.Contains(body.Input, "上一版全文") || !strings.Contains(body.Input, "上一章连续性锚点") || !strings.Contains(body.Input, "too short") || !strings.Contains(body.Input, "E1") {
			t.Fatalf("revision context missing from prompt: %s", body.Input)
		}
		output, _ := json.Marshal(generatedProse{Title: "水线", POV: "何阿生", Markdown: longBody, FactRealizations: map[string]string{"E1": realization}})
		_ = json.NewEncoder(writer).Encode(map[string]any{"output": []any{map[string]any{"content": []any{map[string]any{"type": "output_text", "text": string(output)}}}}})
	}))
	defer server.Close()

	generator := &responsesProseGenerator{Endpoint: server.URL, APIKey: "secret", Model: "test", AuthorContext: "守住事实", Client: server.Client()}
	prior := projectChapter(3, 3, events)
	previous := chapter{Markdown: "上一章最后，船离开了渡口。"}
	got, err := generator.Generate(context.Background(), writeRequest{
		Attempt: 2, Plan: chapterPlan{StartDay: 3, EndDay: 3}, Events: events, PreviousChapter: &previous, Prior: &prior, Issues: []string{"too short"},
	})
	if err != nil {
		t.Fatal(err)
	}
	if got.Title != "水线" || got.FactRealizations["E1"] != realization {
		t.Fatalf("generated prose = %+v", got)
	}
}

func TestModelChapterRequiresVerifiableFactRealizations(t *testing.T) {
	events := []fact{{EventID: "E1", Summary: "水退了", ActualEffect: "旧沟显露"}}
	chapter := chapter{
		Generation: "model", Status: "draft", Markdown: strings.Repeat("一段连续发生的正文。", 180),
		SourceEvents: []string{"E1"}, Claims: chapterClaims(events), Realizations: map[string]string{"E1": "正文中根本不存在的事实片段"},
	}
	if err := validateProjectedChapter(chapter, events); err == nil || !strings.Contains(err.Error(), "E1") {
		t.Fatalf("invalid realization was accepted: %v", err)
	}
	chapter.Realizations["E1"] = "一段连续发生的正文。一段连续发生的正文"
	if err := validateProjectedChapter(chapter, events); err != nil {
		t.Fatalf("valid realization rejected: %v", err)
	}
}

func TestEvidenceExcerptAllowsDialogueAndParagraphPunctuation(t *testing.T) {
	markdown := "何阿生说：\u201c欠你的一日粮，抵这一程。\u201d\n\n船户瞥向他渗血的草鞋。"
	if !containsEvidenceExcerpt(markdown, "欠你的一日粮，抵这一程。船户瞥向他渗血的草鞋") {
		t.Fatal("presentation-only punctuation made a continuous evidence anchor unverifiable")
	}
	if containsEvidenceExcerpt(markdown, "欠你的一日粮，船户答应免掉三年债务") {
		t.Fatal("invented or reordered evidence was accepted")
	}
}

func TestResponsesProseGeneratorRepairsEvidenceOutsideRevisionBudget(t *testing.T) {
	events := []fact{{EventID: "E1", Summary: "旧沟显露", ActualEffect: "两人同时看见木桩"}}
	markdown := "# 第一章 水线\n\n" + strings.Repeat("泥上的旧沟把两个人引到同一棵树下。他们沿水线找到半截木桩。\n\n", 45)
	calls := 0
	server := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
		calls++
		var output []byte
		if calls == 1 {
			output, _ = json.Marshal(generatedProse{Title: "水线", POV: "何阿生", Markdown: markdown, FactRealizations: map[string]string{"E1": "正文里没有这段证据而且长度足够"}})
		} else {
			output, _ = json.Marshal(map[string]any{"fact_realizations": map[string]string{"E1": "泥上的旧沟把两个人引到同一棵树下"}})
		}
		_ = json.NewEncoder(writer).Encode(map[string]any{"output": []any{map[string]any{"content": []any{map[string]any{"type": "output_text", "text": string(output)}}}}})
	}))
	defer server.Close()
	generator := &responsesProseGenerator{Endpoint: server.URL, APIKey: "secret", Model: "test", Client: server.Client()}
	got, err := generator.Generate(context.Background(), writeRequest{Plan: chapterPlan{StartDay: 1, EndDay: 1}, Events: events})
	if err != nil {
		t.Fatal(err)
	}
	if calls != 2 || got.FactRealizations["E1"] != "泥上的旧沟把两个人引到同一棵树下" {
		t.Fatalf("calls=%d realizations=%v", calls, got.FactRealizations)
	}
}
