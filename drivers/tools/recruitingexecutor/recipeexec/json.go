// Package recipeexec runs validated declarative recipes over saved response
// bytes. Network I/O belongs to a Driver and is intentionally absent here, so
// an Artifact can be replayed offline with deterministic output.
package recipeexec

import (
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"math/big"
	"strconv"
	"strings"
	"time"

	"github.com/wanpengxie/atoll/drivers/tools/recruitingexecutor/recipeabi"
)

type DocumentResult struct {
	Items        []map[string]json.RawMessage `json:"items"`
	Next         json.RawMessage              `json:"next,omitempty"`
	ResponseHash string                       `json:"response_hash"`
	Quality      recipeabi.QualityProof       `json:"quality"`
}

func ExecuteJSON(spec recipeabi.Spec, document []byte) (DocumentResult, error) {
	if err := spec.Validate(); err != nil {
		return DocumentResult{}, err
	}
	if spec.Transport != recipeabi.TransportHTTPJSON {
		return DocumentResult{}, fmt.Errorf("JSON executor cannot run transport %q", spec.Transport)
	}
	decoder := json.NewDecoder(bytes.NewReader(document))
	decoder.UseNumber()
	var root any
	if err := decoder.Decode(&root); err != nil {
		return DocumentResult{}, fmt.Errorf("decode recipe document: %w", err)
	}
	var trailing any
	if err := decoder.Decode(&trailing); !errors.Is(err, io.EOF) {
		if err == nil {
			return DocumentResult{}, fmt.Errorf("decode recipe document: multiple JSON values")
		}
		return DocumentResult{}, fmt.Errorf("decode recipe document trailing bytes: %w", err)
	}

	rows := []any{root}
	if spec.Extraction.Collection != "" {
		collection, err := resolvePointer(root, spec.Extraction.Collection)
		if err != nil {
			return DocumentResult{}, fmt.Errorf("resolve collection: %w", err)
		}
		var ok bool
		rows, ok = collection.([]any)
		if !ok {
			return DocumentResult{}, fmt.Errorf("collection pointer did not resolve to an array")
		}
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

	result := DocumentResult{Items: make([]map[string]json.RawMessage, 0, len(rows))}
	sum := sha256.Sum256(document)
	result.ResponseHash = "sha256:" + hex.EncodeToString(sum[:])
	result.Quality.IdentityComplete = true
	result.Quality.OrderingContractHeld = true
	result.Quality.PaginationStable = true
	var previousActivity time.Time
	for index, row := range rows {
		if spec.Kind == recipeabi.KindListing && spec.Listing.ExcludePinnedField != "" {
			pointer := spec.Extraction.Fields[spec.Listing.ExcludePinnedField]
			if pinnedValue, err := resolvePointer(row, pointer); err == nil {
				pinnedRaw, _ := json.Marshal(pinnedValue)
				if pinned, ok := rawBool(pinnedRaw); ok && pinned {
					continue
				}
			}
		}
		item := make(map[string]json.RawMessage, len(spec.Extraction.Fields))
		for field, pointer := range spec.Extraction.Fields {
			value, err := resolvePointer(row, pointer)
			if err != nil {
				return DocumentResult{}, fmt.Errorf("row %d field %s: %w", index, field, err)
			}
			if text, ok := value.(string); ok {
				value = strings.TrimSpace(text)
			}
			raw, err := json.Marshal(value)
			if err != nil {
				return DocumentResult{}, fmt.Errorf("row %d field %s: %w", index, field, err)
			}
			item[field] = raw
		}
		if spec.Kind == recipeabi.KindListing {
			identity, ok := rawIdentity(item[spec.Listing.IdentityField])
			if !ok || strings.TrimSpace(identity) == "" {
				result.Quality.IdentityComplete = false
			}
			if spec.Listing.ActivityField != "" {
				activityRaw, ok := rawString(item[spec.Listing.ActivityField])
				activity, err := time.Parse(time.RFC3339, activityRaw)
				if !ok || err != nil {
					result.Quality.OrderingContractHeld = false
				} else if !previousActivity.IsZero() && activity.After(previousActivity) {
					result.Quality.OrderingContractHeld = false
				}
				if err == nil {
					previousActivity = activity
				}
			}
		}
		result.Items = append(result.Items, item)
	}
	result.Quality.ItemCount = len(result.Items)
	if spec.Extraction.Next != "" {
		next, err := resolvePointer(root, spec.Extraction.Next)
		if err != nil {
			return DocumentResult{}, fmt.Errorf("resolve next: %w", err)
		}
		result.Next, err = json.Marshal(next)
		if err != nil {
			return DocumentResult{}, err
		}
	}
	return result, nil
}

func resolvePointer(value any, pointer string) (any, error) {
	if pointer == "" {
		return value, nil
	}
	if !strings.HasPrefix(pointer, "/") {
		return nil, fmt.Errorf("invalid JSON pointer %q", pointer)
	}
	current := value
	for _, encoded := range strings.Split(pointer[1:], "/") {
		for index := 0; index < len(encoded); index++ {
			if encoded[index] == '~' && (index+1 >= len(encoded) || (encoded[index+1] != '0' && encoded[index+1] != '1')) {
				return nil, fmt.Errorf("JSON pointer token has invalid escape")
			}
		}
		token := strings.ReplaceAll(strings.ReplaceAll(encoded, "~1", "/"), "~0", "~")
		switch node := current.(type) {
		case map[string]any:
			var found bool
			current, found = node[token]
			if !found {
				return nil, fmt.Errorf("JSON pointer token %q not found", token)
			}
		case []any:
			index, err := strconv.Atoi(token)
			if err != nil || index < 0 || index >= len(node) {
				return nil, fmt.Errorf("JSON pointer array token %q is invalid", token)
			}
			current = node[index]
		default:
			return nil, fmt.Errorf("JSON pointer token %q traverses a scalar", token)
		}
	}
	return current, nil
}

func rawString(raw json.RawMessage) (string, bool) {
	var value string
	if err := json.Unmarshal(raw, &value); err != nil {
		return "", false
	}
	return value, true
}

// rawIdentity accepts the two lossless scalar forms commonly used by public
// job APIs: a JSON string or an integer. Integers are canonicalized so the
// scanner never treats alternative spellings as different identities. Floats,
// exponents, booleans, and null are deliberately rejected as ambiguous keys.
func rawIdentity(raw json.RawMessage) (string, bool) {
	if value, ok := rawString(raw); ok {
		return value, true
	}
	text := strings.TrimSpace(string(raw))
	if text == "" || strings.ContainsAny(text, ".eE") {
		return "", false
	}
	integer, ok := new(big.Int).SetString(text, 10)
	if !ok {
		return "", false
	}
	return integer.String(), true
}

func rawBool(raw json.RawMessage) (bool, bool) {
	var value bool
	if err := json.Unmarshal(raw, &value); err == nil {
		return value, true
	}
	var text string
	if err := json.Unmarshal(raw, &text); err != nil {
		return false, false
	}
	value, err := strconv.ParseBool(strings.TrimSpace(text))
	return value, err == nil
}
