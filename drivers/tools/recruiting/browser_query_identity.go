package recruiting

import (
	"bytes"
	"encoding/json"
	"fmt"
	"net/url"
	"sort"
	"strings"

	"github.com/wanpengxie/atoll/drivers/tools/recruiting/model"
	"github.com/wanpengxie/atoll/drivers/tools/recruiting/store"
	"github.com/wanpengxie/atoll/drivers/tools/recruitingexecutor/recipeabi"
)

// browserQueryFromProbe binds a listing Recipe to the request that the page
// actually issued. Only pagination fields demonstrated by a later click may
// vary. Other same-path requests are excluded by the immutable body identity.
func browserQueryFromProbe(selected recipeabi.PublicQueryObservation, probe model.DeepDiscoveryBrowserProbe,
	result store.DeepDiscoveryBrowserResult) (recipeabi.BrowserQuery, error) {
	endpoint, err := url.Parse(selected.EndpointURL)
	if err != nil {
		return recipeabi.BrowserQuery{}, err
	}
	query := recipeabi.BrowserQuery{Method: selected.Method, EndpointPath: endpoint.Path,
		JSONBody: append(json.RawMessage(nil), selected.JSONBody...)}
	if probe.ListingAdvance == nil || probe.ListingAdvance.Kind == string(recipeabi.ListingAdvanceNone) {
		return query, query.Validate()
	}
	var baseline map[string]any
	if err := decodeQueryBody(selected.JSONBody, &baseline); err != nil {
		return recipeabi.BrowserQuery{}, err
	}
	for _, captured := range result.PublicQueryResponses {
		if captured.ActionSequence == 0 || captured.Request.Method != selected.Method {
			continue
		}
		candidateEndpoint, err := url.Parse(captured.Request.EndpointURL)
		if err != nil || candidateEndpoint.Path != endpoint.Path {
			continue
		}
		var candidate map[string]any
		if err := decodeQueryBody(captured.Request.JSONBody, &candidate); err != nil {
			continue
		}
		var changed []string
		collectQueryDifferences(baseline, candidate, "", &changed)
		if len(changed) != 1 || !isPaginationPointer(changed[0]) ||
			!paginationValueAdvanced(baseline, candidate, changed[0]) {
			continue
		}
		query.MutableJSONPointers = changed
		if err := query.Validate(); err == nil && query.MatchesObservation(captured.Request) {
			return query, nil
		}
	}
	return recipeabi.BrowserQuery{}, fmt.Errorf("listing advancement Probe did not demonstrate a stable pagination request identity")
}

func paginationValueAdvanced(baseline, candidate map[string]any, pointer string) bool {
	segments := strings.Split(pointer[1:], "/")
	left, right := any(baseline), any(candidate)
	for _, segment := range segments {
		segment = strings.ReplaceAll(strings.ReplaceAll(segment, "~1", "/"), "~0", "~")
		leftObject, leftOK := left.(map[string]any)
		rightObject, rightOK := right.(map[string]any)
		if !leftOK || !rightOK {
			return false
		}
		left, right = leftObject[segment], rightObject[segment]
	}
	leftNumber, leftOK := left.(json.Number)
	rightNumber, rightOK := right.(json.Number)
	if !leftOK || !rightOK {
		return false
	}
	previous, leftErr := leftNumber.Int64()
	next, rightErr := rightNumber.Int64()
	return leftErr == nil && rightErr == nil && previous >= 0 && next > previous
}

func decodeQueryBody(raw json.RawMessage, target *map[string]any) error {
	decoder := json.NewDecoder(bytes.NewReader(raw))
	decoder.UseNumber()
	return decoder.Decode(target)
}

func collectQueryDifferences(left, right map[string]any, prefix string, differences *[]string) {
	keys := make(map[string]struct{}, len(left)+len(right))
	for key := range left {
		keys[key] = struct{}{}
	}
	for key := range right {
		keys[key] = struct{}{}
	}
	ordered := make([]string, 0, len(keys))
	for key := range keys {
		ordered = append(ordered, key)
	}
	sort.Strings(ordered)
	for _, key := range ordered {
		pointer := prefix + "/" + strings.ReplaceAll(strings.ReplaceAll(key, "~", "~0"), "/", "~1")
		leftValue, leftPresent := left[key]
		rightValue, rightPresent := right[key]
		if !leftPresent || !rightPresent {
			*differences = append(*differences, pointer)
			continue
		}
		leftObject, leftIsObject := leftValue.(map[string]any)
		rightObject, rightIsObject := rightValue.(map[string]any)
		if leftIsObject && rightIsObject {
			collectQueryDifferences(leftObject, rightObject, pointer, differences)
		} else if !queryValuesEqual(leftValue, rightValue) {
			*differences = append(*differences, pointer)
		}
	}
}

func queryValuesEqual(left, right any) bool {
	leftJSON, _ := json.Marshal(left)
	rightJSON, _ := json.Marshal(right)
	return bytes.Equal(leftJSON, rightJSON)
}

func isPaginationPointer(pointer string) bool {
	segments := strings.Split(pointer, "/")
	field := strings.ToLower(segments[len(segments)-1])
	switch field {
	case "offset", "page", "page_no", "page_num", "page_index", "page_number", "current", "current_page":
		return true
	default:
		return false
	}
}

func equalStringSlices(left, right []string) bool {
	if len(left) != len(right) {
		return false
	}
	for index := range left {
		if left[index] != right[index] {
			return false
		}
	}
	return true
}
