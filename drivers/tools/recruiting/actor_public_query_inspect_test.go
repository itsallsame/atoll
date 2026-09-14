package recruiting

import (
	"strings"
	"testing"
)

func TestBoundedJSONPreviewPreservesShapeAndBoundsCollections(t *testing.T) {
	items := make([]any, 8)
	for index := range items {
		items[index] = map[string]any{"jobId": index, "name": strings.Repeat("x", 600)}
	}
	budget, truncated := 512, false
	preview := boundedJSONPreview(map[string]any{
		"data": map[string]any{"list": items, "pageNo": float64(1), "pageSize": float64(10)},
	}, 0, &budget, &truncated)
	root, ok := preview.(map[string]any)
	if !ok || !truncated {
		t.Fatalf("preview type=%T truncated=%v", preview, truncated)
	}
	data, ok := root["data"].(map[string]any)
	if !ok || data["pageNo"] != float64(1) || data["pageSize"] != float64(10) {
		t.Fatalf("pagination shape was not preserved: %#v", root)
	}
	list, ok := data["list"].([]any)
	if !ok || len(list) != 5 {
		t.Fatalf("array bound was not applied: %#v", data["list"])
	}
	first := list[0].(map[string]any)
	if first["jobId"] != 0 || len(first["name"].(string)) != 512 {
		t.Fatalf("sample identity or string bound was not preserved: %#v", first)
	}
}

func TestBoundedJSONPreviewStopsAtNodeBudget(t *testing.T) {
	budget, truncated := 2, false
	preview := boundedJSONPreview(map[string]any{"a": map[string]any{"b": "value"}}, 0, &budget, &truncated)
	if !truncated {
		t.Fatal("exhausted node budget was not reported")
	}
	root := preview.(map[string]any)
	inner := root["a"].(map[string]any)
	if inner["b"] != "<truncated>" {
		t.Fatalf("unexpected bounded preview: %#v", preview)
	}
}
