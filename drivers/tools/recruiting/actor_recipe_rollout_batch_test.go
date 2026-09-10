package recruiting

import (
	"reflect"
	"testing"

	"github.com/wanpengxie/atoll/drivers/tools/recruiting/model"
)

func TestRecipeRolloutSourceDocumentIsStrictBoundedAndDeterministic(t *testing.T) {
	first, err := parseRecipeRolloutSourceDocument([]byte(`{"schema_version":"recipe-rollout-sources.v1","source_ids":["source-c","source-a","source-b"]}`),
		"batch-parser", recipeRolloutSourceSchema)
	if err != nil {
		t.Fatal(err)
	}
	second, err := parseRecipeRolloutSourceDocument([]byte(`{"schema_version":"recipe-rollout-sources.v1","source_ids":["source-b","source-c","source-a"]}`),
		"batch-parser", recipeRolloutSourceSchema)
	if err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(first, second) {
		t.Fatalf("deterministic Source order changed: %v != %v", first, second)
	}
	for index := 1; index < len(first); index++ {
		if model.RecipeRolloutOrderKey("batch-parser", first[index-1]) >=
			model.RecipeRolloutOrderKey("batch-parser", first[index]) {
			t.Fatalf("Source order is not canonical: %v", first)
		}
	}
	invalid := []string{
		`{"schema_version":"recipe-rollout-sources.v1"}`,
		`{"schema_version":"recipe-rollout-sources.v1","source_ids":[]}`,
		`{"schema_version":"recipe-rollout-sources.v1","source_ids":[" source-a"]}`,
		`{"schema_version":"recipe-rollout-sources.v1","source_ids":["source-a","source-a"]}`,
		`{"schema_version":"recipe-rollout-sources.v1","source_ids":["source-a"],"extra":true}`,
		`{"schema_version":"recipe-rollout-sources.v1","source_ids":["source-a"]} {}`,
	}
	for _, content := range invalid {
		if _, err := parseRecipeRolloutSourceDocument([]byte(content), "batch-parser", recipeRolloutSourceSchema); err == nil {
			t.Fatalf("invalid Source document was accepted: %q", content)
		}
	}
	if _, err := parseRecipeRolloutSourceDocument([]byte(`{"schema_version":"recipe-rollout-sources.v1","source_ids":["source-a"]}`), "batch-parser", "v2"); err == nil {
		t.Fatal("unknown rollout source schema was accepted")
	}
}
