package recipeabi

import (
	"encoding/json"
	"strings"
	"testing"
)

func TestBuildJSONPreviewPreservesShapeAndBoundsCollections(t *testing.T) {
	items := make([]any, 8)
	for index := range items {
		items[index] = map[string]any{"jobId": index, "name": strings.Repeat("x", 600)}
	}
	raw, _ := json.Marshal(map[string]any{"data": map[string]any{
		"list": items, "pageNo": 1, "pageSize": 10,
	}})
	preview, truncated, err := BuildJSONPreview(raw)
	if err != nil || !truncated {
		t.Fatalf("preview err=%v truncated=%v", err, truncated)
	}
	var root map[string]any
	if err := json.Unmarshal(preview, &root); err != nil {
		t.Fatal(err)
	}
	data := root["data"].(map[string]any)
	list := data["list"].([]any)
	if len(list) != 5 || data["pageNo"] != float64(1) || data["pageSize"] != float64(10) {
		t.Fatalf("response shape was not preserved: %#v", data)
	}
	first := list[0].(map[string]any)
	if first["jobId"] != float64(0) || len(first["name"].(string)) != 512 {
		t.Fatalf("sample identity or string bound was not preserved: %#v", first)
	}
}

func TestBuildJSONPreviewRejectsInvalidJSON(t *testing.T) {
	if _, _, err := BuildJSONPreview([]byte(`{"data":`)); err == nil {
		t.Fatal("invalid JSON was accepted")
	}
}
