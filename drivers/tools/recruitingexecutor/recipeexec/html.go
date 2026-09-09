package recipeexec

import (
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"strings"
	"time"

	"github.com/andybalholm/cascadia"
	"golang.org/x/net/html"

	"github.com/wanpengxie/atoll/drivers/tools/recruitingexecutor/recipeabi"
)

func ExecuteHTML(spec recipeabi.Spec, document []byte) (DocumentResult, error) {
	if err := spec.Validate(); err != nil {
		return DocumentResult{}, err
	}
	if spec.Transport != recipeabi.TransportHTTPHTML {
		return DocumentResult{}, fmt.Errorf("HTML executor cannot run transport %q", spec.Transport)
	}
	root, err := html.Parse(bytes.NewReader(document))
	if err != nil {
		return DocumentResult{}, fmt.Errorf("parse recipe HTML: %w", err)
	}
	rows := []*html.Node{root}
	if spec.Extraction.Collection != "" {
		selector, err := cascadia.Compile(spec.Extraction.Collection)
		if err != nil {
			return DocumentResult{}, fmt.Errorf("compile collection selector: %w", err)
		}
		rows = cascadia.QueryAll(root, selector)
	}
	if spec.Kind == recipeabi.KindListing && len(rows) > spec.Listing.MaxItemsPerPage {
		return DocumentResult{}, fmt.Errorf("listing page contains %d items, limit is %d", len(rows), spec.Listing.MaxItemsPerPage)
	}
	if spec.Kind == recipeabi.KindDiscovery && len(rows) > 500 {
		return DocumentResult{}, fmt.Errorf("discovery page contains %d candidates, limit is 500", len(rows))
	}
	if spec.Kind == recipeabi.KindDiscovery && len(rows) > 500 {
		return DocumentResult{}, fmt.Errorf("discovery page contains %d candidates, limit is 500", len(rows))
	}
	fieldSelectors := make(map[string]cascadia.Selector, len(spec.Extraction.Fields))
	for field, expression := range spec.Extraction.Fields {
		selector, err := cascadia.Compile(expression)
		if err != nil {
			return DocumentResult{}, fmt.Errorf("compile field %s selector: %w", field, err)
		}
		fieldSelectors[field] = selector
	}

	result := DocumentResult{Items: make([]map[string]json.RawMessage, 0, len(rows))}
	sum := sha256.Sum256(document)
	result.ResponseHash = "sha256:" + hex.EncodeToString(sum[:])
	result.Quality.IdentityComplete = true
	result.Quality.OrderingContractHeld = true
	result.Quality.PaginationStable = true
	var previousActivity time.Time
	for index, row := range rows {
		if spec.Kind == recipeabi.KindListing && spec.Listing.ExcludePinnedField != "" {
			field := spec.Listing.ExcludePinnedField
			if node := cascadia.Query(row, fieldSelectors[field]); node != nil {
				if value, valueErr := htmlNodeValue(node, spec.Extraction.Attributes[field]); valueErr == nil {
					pinnedRaw, _ := json.Marshal(value)
					if pinned, ok := rawBool(pinnedRaw); ok && pinned {
						continue
					}
				}
			}
		}
		item := make(map[string]json.RawMessage, len(fieldSelectors))
		for field, selector := range fieldSelectors {
			node := cascadia.Query(row, selector)
			if node == nil {
				return DocumentResult{}, fmt.Errorf("row %d field %s selector matched nothing", index, field)
			}
			value, err := htmlNodeValue(node, spec.Extraction.Attributes[field])
			if err != nil {
				return DocumentResult{}, fmt.Errorf("row %d field %s: %w", index, field, err)
			}
			item[field], _ = json.Marshal(value)
		}
		if spec.Kind == recipeabi.KindListing {
			identity, ok := rawString(item[spec.Listing.IdentityField])
			if !ok || strings.TrimSpace(identity) == "" {
				result.Quality.IdentityComplete = false
			}
			if spec.Listing.ActivityField != "" {
				activityRaw, ok := rawString(item[spec.Listing.ActivityField])
				activity, parseErr := time.Parse(time.RFC3339, activityRaw)
				if !ok || parseErr != nil {
					result.Quality.OrderingContractHeld = false
				} else if !previousActivity.IsZero() && activity.After(previousActivity) {
					result.Quality.OrderingContractHeld = false
				}
				if parseErr == nil {
					previousActivity = activity
				}
			}
		}
		result.Items = append(result.Items, item)
	}
	result.Quality.ItemCount = len(result.Items)
	if spec.Extraction.Next != "" {
		selector, err := cascadia.Compile(spec.Extraction.Next)
		if err != nil {
			return DocumentResult{}, fmt.Errorf("compile next selector: %w", err)
		}
		if node := cascadia.Query(root, selector); node != nil {
			value, err := htmlNodeValue(node, spec.Extraction.NextAttribute)
			if err != nil {
				return DocumentResult{}, fmt.Errorf("extract next: %w", err)
			}
			result.Next, _ = json.Marshal(value)
		}
	}
	return result, nil
}

func htmlNodeValue(node *html.Node, attribute string) (string, error) {
	if attribute != "" {
		for _, candidate := range node.Attr {
			if strings.EqualFold(candidate.Key, attribute) {
				return strings.TrimSpace(candidate.Val), nil
			}
		}
		return "", fmt.Errorf("attribute %s is missing", attribute)
	}
	var parts []string
	var walk func(*html.Node)
	walk = func(current *html.Node) {
		if current.Type == html.TextNode {
			if text := strings.TrimSpace(current.Data); text != "" {
				parts = append(parts, text)
			}
		}
		for child := current.FirstChild; child != nil; child = child.NextSibling {
			walk(child)
		}
	}
	walk(node)
	return strings.Join(strings.Fields(strings.Join(parts, " ")), " "), nil
}
