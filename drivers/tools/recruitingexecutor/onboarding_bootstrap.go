package recruitingexecutor

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/url"
	"strings"
	"time"

	"github.com/wanpengxie/atoll/lib/actorbase"
	"github.com/wanpengxie/atoll/protocol/actor"
	"golang.org/x/net/publicsuffix"
)

const (
	wikidataAPIBase       = "https://www.wikidata.org"
	websiteLookupMaxBytes = 2 << 20
)

type websiteLookupPayload struct {
	CompanyName string `json:"company_name"`
	Language    string `json:"language,omitempty"`
}

type websiteCandidate struct {
	EntityID        string `json:"entity_id"`
	Label           string `json:"label"`
	Description     string `json:"description,omitempty"`
	Website         string `json:"website"`
	ClaimedWebsite  string `json:"claimed_website,omitempty"`
	ConfidenceBasis string `json:"confidence_basis"`
	EvidenceURL     string `json:"evidence_url"`
}

type wikidataSearchResponse struct {
	Search []struct {
		ID          string `json:"id"`
		Label       string `json:"label"`
		Description string `json:"description"`
	} `json:"search"`
}

type wikidataEntityResponse struct {
	Entities map[string]struct {
		Claims map[string][]struct {
			MainSnak struct {
				DataValue struct {
					Value json.RawMessage `json:"value"`
				} `json:"datavalue"`
			} `json:"mainsnak"`
		} `json:"claims"`
	} `json:"entities"`
}

func handleOnboardingBootstrap(sys actorbase.Sys, cfg Config, msg actorbase.Msg) {
	if msg.Sender.Kind != actor.KindHuman && msg.Sender.Kind != actor.KindAgent {
		_, _ = sys.Fail(msg, "permission_denied", "only a human or agent may start onboarding bootstrap")
		return
	}
	if !cfg.ExecutionEnabled {
		_, _ = sys.Fail(msg, "dependency_missing", "onboarding bootstrap requires an enabled executor")
		return
	}
	switch msg.Type {
	case TypeCompanyWebsiteLookup:
		if cfg.Capability != "http.fetch" {
			_, _ = sys.Fail(msg, "type_unsupported", "official website lookup requires the http.fetch executor")
			return
		}
		var payload websiteLookupPayload
		if !decode(sys, msg, &payload) {
			return
		}
		payload.CompanyName, payload.Language = strings.TrimSpace(payload.CompanyName), strings.TrimSpace(payload.Language)
		if payload.Language == "" {
			payload.Language = "zh"
		}
		if payload.CompanyName == "" || len(payload.CompanyName) > 200 || (payload.Language != "zh" && payload.Language != "en") {
			_, _ = sys.Fail(msg, "payload_invalid", "company_name and language zh or en are required")
			return
		}
		client := &http.Client{Timeout: 15 * time.Second, CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }}
		normalize := func(ctx context.Context, website string) string {
			resolved, resolveErr := resolveWebsiteRedirect(ctx, client, website)
			if resolveErr != nil {
				return website
			}
			return resolved
		}
		candidates, err := lookupOfficialWebsites(msg.Ctx(), client, wikidataAPIBase, payload.CompanyName, payload.Language, normalize)
		if err != nil {
			_, _ = sys.Fail(msg, "provider_failed", err.Error())
			return
		}
		_, _ = sys.Reply(msg, map[string]any{"status": "completed", "company_name": payload.CompanyName,
			"candidates": candidates, "candidate_count": len(candidates), "next_action": "confirm_website_candidate"})
	}
}

func lookupOfficialWebsites(ctx context.Context, client *http.Client, baseURL, companyName, language string,
	normalize func(context.Context, string) string) ([]websiteCandidate, error) {
	if client == nil {
		return nil, errors.New("HTTP client is required")
	}
	base, err := url.Parse(baseURL)
	if err != nil || base.Scheme == "" || base.Host == "" {
		return nil, errors.New("Wikidata base URL is invalid")
	}
	query := base.ResolveReference(&url.URL{Path: "/w/api.php"})
	values := query.Query()
	values.Set("action", "wbsearchentities")
	values.Set("search", companyName)
	values.Set("language", language)
	values.Set("uselang", language)
	values.Set("type", "item")
	values.Set("limit", "5")
	values.Set("format", "json")
	query.RawQuery = values.Encode()
	var search wikidataSearchResponse
	if err := getBoundedJSON(ctx, client, query.String(), &search); err != nil {
		return nil, fmt.Errorf("search Wikidata entities: %w", err)
	}
	result := make([]websiteCandidate, 0, len(search.Search))
	seen := map[string]struct{}{}
	for _, hit := range search.Search {
		if len(result) >= 5 {
			break
		}
		if !safeEntityID(hit.ID) {
			continue
		}
		evidence := base.ResolveReference(&url.URL{Path: "/wiki/Special:EntityData/" + hit.ID + ".json"})
		var entities wikidataEntityResponse
		if err := getBoundedJSON(ctx, client, evidence.String(), &entities); err != nil {
			continue
		}
		entity, found := entities.Entities[hit.ID]
		if !found {
			continue
		}
		for _, claim := range entity.Claims["P856"] {
			var website string
			if err := json.Unmarshal(claim.MainSnak.DataValue.Value, &website); err != nil || !safeWebsiteCandidate(website) {
				continue
			}
			claimedWebsite := strings.TrimRight(website, "/")
			website = claimedWebsite
			if normalize != nil {
				website = strings.TrimRight(normalize(ctx, website), "/")
			}
			if _, duplicate := seen[website]; duplicate {
				continue
			}
			seen[website] = struct{}{}
			result = append(result, websiteCandidate{EntityID: hit.ID, Label: hit.Label, Description: hit.Description,
				Website: website, ClaimedWebsite: claimedWebsite, ConfidenceBasis: "Wikidata official website property P856 plus same-registrable-domain redirect verification", EvidenceURL: evidence.String()})
			if len(result) >= 5 {
				break
			}
		}
	}
	return result, nil
}

func resolveWebsiteRedirect(ctx context.Context, client *http.Client, website string) (string, error) {
	current, err := url.Parse(website)
	if err != nil || !safeWebsiteCandidate(website) {
		return "", errors.New("website candidate is invalid")
	}
	registrable, err := publicsuffix.EffectiveTLDPlusOne(strings.ToLower(current.Hostname()))
	if err != nil {
		return "", errors.New("website candidate has no registrable domain")
	}
	for redirects := 0; redirects <= 3; redirects++ {
		if err := requirePublicHost(ctx, current.Hostname()); err != nil {
			return "", err
		}
		request, err := http.NewRequestWithContext(ctx, http.MethodHead, current.String(), nil)
		if err != nil {
			return "", err
		}
		request.Header.Set("User-Agent", "Atoll-Recruiting-Website-Lookup/1")
		response, err := client.Do(request)
		if err != nil {
			return "", err
		}
		_ = response.Body.Close()
		if response.StatusCode < 300 || response.StatusCode >= 400 {
			return current.String(), nil
		}
		location := response.Header.Get("Location")
		if location == "" || redirects == 3 {
			return "", errors.New("website redirect chain is incomplete or too long")
		}
		next, err := current.Parse(location)
		if err != nil || !safeWebsiteCandidate(next.String()) {
			return "", errors.New("website redirect target is invalid")
		}
		nextRegistrable, err := publicsuffix.EffectiveTLDPlusOne(strings.ToLower(next.Hostname()))
		if err != nil || nextRegistrable != registrable {
			return "", errors.New("website redirect crossed its registrable domain")
		}
		current = next
	}
	return "", errors.New("website redirect resolution exhausted")
}

func requirePublicHost(ctx context.Context, hostname string) error {
	addresses, err := net.DefaultResolver.LookupIPAddr(ctx, hostname)
	if err != nil || len(addresses) == 0 {
		return errors.New("website hostname did not resolve")
	}
	for _, address := range addresses {
		if address.IP.IsPrivate() || address.IP.IsLoopback() || address.IP.IsUnspecified() || address.IP.IsLinkLocalUnicast() || address.IP.IsLinkLocalMulticast() {
			return errors.New("website hostname resolved outside the public network")
		}
	}
	return nil
}

func getBoundedJSON(ctx context.Context, client *http.Client, target string, output any) error {
	request, err := http.NewRequestWithContext(ctx, http.MethodGet, target, nil)
	if err != nil {
		return err
	}
	request.Header.Set("Accept", "application/json")
	request.Header.Set("User-Agent", "Atoll-Recruiting-Website-Lookup/1")
	response, err := client.Do(request)
	if err != nil {
		return err
	}
	defer response.Body.Close()
	if response.StatusCode != http.StatusOK {
		return fmt.Errorf("unexpected HTTP status %d", response.StatusCode)
	}
	decoder := json.NewDecoder(io.LimitReader(response.Body, websiteLookupMaxBytes+1))
	if err := decoder.Decode(output); err != nil {
		return err
	}
	var trailing any
	if err := decoder.Decode(&trailing); err != io.EOF {
		return errors.New("JSON response exceeds one document")
	}
	return nil
}

func safeEntityID(value string) bool {
	if len(value) < 2 || value[0] != 'Q' {
		return false
	}
	for _, character := range value[1:] {
		if character < '0' || character > '9' {
			return false
		}
	}
	return true
}

func safeWebsiteCandidate(value string) bool {
	parsed, err := url.Parse(strings.TrimSpace(value))
	if err != nil || (parsed.Scheme != "http" && parsed.Scheme != "https") || parsed.Hostname() == "" || parsed.User != nil || parsed.Fragment != "" {
		return false
	}
	host := strings.ToLower(parsed.Hostname())
	if host == "localhost" || strings.HasSuffix(host, ".localhost") {
		return false
	}
	if address := net.ParseIP(host); address != nil && (address.IsPrivate() || address.IsLoopback() || address.IsUnspecified() || address.IsLinkLocalUnicast()) {
		return false
	}
	return true
}
