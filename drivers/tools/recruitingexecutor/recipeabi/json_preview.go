package recipeabi

import (
	"encoding/json"
	"sort"
)

// BuildJSONPreview keeps enough public response shape for an Agent to derive
// field paths while bounding what crosses the Executor/control-plane boundary.
func BuildJSONPreview(raw []byte) (json.RawMessage, bool, error) {
	var decoded any
	if err := json.Unmarshal(raw, &decoded); err != nil {
		return nil, false, err
	}
	budget, truncated := 512, false
	preview := boundedJSONPreview(decoded, 0, &budget, &truncated)
	encoded, err := json.Marshal(preview)
	if err != nil {
		return nil, false, err
	}
	return encoded, truncated, nil
}

func boundedJSONPreview(value any, depth int, budget *int, truncated *bool) any {
	if *budget <= 0 || depth > 10 {
		*truncated = true
		return "<truncated>"
	}
	*budget = *budget - 1
	switch typed := value.(type) {
	case map[string]any:
		keys := make([]string, 0, len(typed))
		for key := range typed {
			keys = append(keys, key)
		}
		sort.Strings(keys)
		if len(keys) > 64 {
			keys = keys[:64]
			*truncated = true
		}
		result := make(map[string]any, len(keys))
		for _, key := range keys {
			result[key] = boundedJSONPreview(typed[key], depth+1, budget, truncated)
		}
		return result
	case []any:
		limit := len(typed)
		if limit > 5 {
			limit = 5
			*truncated = true
		}
		result := make([]any, limit)
		for index := 0; index < limit; index++ {
			result[index] = boundedJSONPreview(typed[index], depth+1, budget, truncated)
		}
		return result
	case string:
		if len(typed) > 512 {
			*truncated = true
			return typed[:512]
		}
		return typed
	default:
		return typed
	}
}
