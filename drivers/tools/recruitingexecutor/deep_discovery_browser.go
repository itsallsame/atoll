package recruitingexecutor

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/url"
	"sort"
	"strings"
	"unicode/utf8"

	"github.com/wanpengxie/atoll/drivers/tools/recruiting/executioncontract"
	"github.com/wanpengxie/atoll/drivers/tools/recruiting/model"
	"github.com/wanpengxie/atoll/drivers/tools/recruitingexecutor/browserdriver"
	"github.com/wanpengxie/atoll/drivers/tools/recruitingexecutor/httpdriver"
	"github.com/wanpengxie/atoll/drivers/tools/recruitingexecutor/recipeabi"
	"golang.org/x/net/html"
)

func executeDeepDiscoveryBrowser(ctx context.Context, control executionControl, resources executionResourceAccess,
	explorer browserdriver.Broker, offer executioncontract.Offer, options executeOfferOptions) error {
	if ctx == nil || control == nil || resources == nil || explorer == nil || options.Now == nil ||
		offer.Kind != "deep_discovery_browser" || offer.DeepDiscoveryBrowser == nil ||
		offer.Work.Purpose != "deep_discovery_browser" || offer.Work.TargetID != offer.DeepDiscoveryBrowser.ProbeID ||
		offer.DeepDiscoveryBrowser.WorkID != offer.Work.WorkID || offer.RequestedCapability != "browser.public" {
		return errors.New("Deep Discovery browser execution requires one immutable browser.public offer")
	}
	probe := *offer.DeepDiscoveryBrowser
	if err := options.Compliance.Validate(); err != nil {
		return fmt.Errorf("validate Deep Discovery browser compliance evidence: %w", err)
	}
	actions := []recipeabi.BrowserAction{}
	if probe.WaitSelector != "" {
		actions = append(actions, recipeabi.BrowserAction{Kind: recipeabi.BrowserActionWaitSelector, Selector: probe.WaitSelector, TimeoutMS: 10_000})
	}
	if probe.ScrollRepeats > 0 {
		actions = append(actions, recipeabi.BrowserAction{Kind: recipeabi.BrowserActionScrollPage, MaxRepeats: probe.ScrollRepeats})
	}
	// Recruitment SPAs may perform a bounded same-origin document transition
	// while resolving locale or application state. Reserve two such transitions
	// in addition to the requested entry navigation; cross-origin documents and
	// all browser-side writes remain blocked by the Broker.
	maxNavigations := 3
	if probe.FollowLinkSelector != "" {
		actions = append(actions, recipeabi.BrowserAction{Kind: recipeabi.BrowserActionFollowLink, Selector: probe.FollowLinkSelector})
		maxNavigations = 4
	}
	plan := recipeabi.BrowserPlan{Version: recipeabi.BrowserPlanVersion, Actions: actions, MaxNavigations: maxNavigations, MaxDOMBytes: options.Artifact.MaxBytes}
	if err := plan.Validate(); err != nil {
		return fmt.Errorf("validate Deep Discovery browser plan: %w", err)
	}
	planHash, _ := plan.ContentHash()
	if len(offer.PublicQueryStubs) != len(probe.StubVerificationIDs) {
		return errors.New("Deep Discovery browser Stub references do not match the immutable Probe")
	}
	stubs := make([]browserdriver.PublicQueryStub, 0, len(offer.PublicQueryStubs))
	for index, ref := range offer.PublicQueryStubs {
		if ref.VerificationID != probe.StubVerificationIDs[index] || ref.Request.Validate() != nil ||
			ref.Artifact.Kind != model.ArtifactResponse || ref.Artifact.WorkID == "" {
			return errors.New("Deep Discovery browser Stub lineage is inconsistent")
		}
		body, readErr := readBackfillArtifact(ctx, resources, ref.Artifact, browserdriver.MaxPublicQueryStubBytes)
		if readErr != nil {
			return fmt.Errorf("read verified public query Stub %q: %w", ref.VerificationID, readErr)
		}
		stub := browserdriver.PublicQueryStub{Request: ref.Request, StatusCode: ref.StatusCode,
			ContentType: ref.ContentType, Body: body, ContentHash: ref.Artifact.ContentHash}
		if err := stub.Validate(); err != nil {
			return fmt.Errorf("validate verified public query Stub %q: %w", ref.VerificationID, err)
		}
		stubs = append(stubs, stub)
	}
	sinkConfig := options.Artifact
	sinkConfig.WorkID, sinkConfig.AttemptID = offer.Work.WorkID, offer.Attempt.AttemptID
	sink, err := newAtollArtifactSink(resources, sinkConfig)
	if err != nil {
		return fmt.Errorf("prepare Deep Discovery Artifact sink: %w", err)
	}
	if err := control.Accept(ctx, offer); err != nil {
		return fmt.Errorf("accept Deep Discovery browser offer: %w", err)
	}
	if err := control.Started(ctx, offer); err != nil {
		return fmt.Errorf("start Deep Discovery browser offer: %w", err)
	}
	request := browserdriver.SessionRequest{EndpointURL: probe.URL, UserAgent: "Atoll-Recruiting-Deep-Discovery/1",
		AcceptLanguage: "zh-CN,zh;q=0.9,en;q=0.8", Plan: plan, PlanHash: planHash,
		AttemptID: offer.Attempt.AttemptID, TimeoutMS: 60_000,
		Policy:         browserdriver.PolicyEvidence{TermsPolicyVersion: options.Compliance.TermsPolicyVersion, TermsReviewedAt: options.Compliance.TermsReviewedAt},
		AllowedMethods: []string{http.MethodGet, http.MethodHead, http.MethodOptions}, SameOriginDocs: true, BlockDownloads: true,
		BlockPopups: true, PublicQueryStubs: stubs}
	result, err := explorer.Run(ctx, request)
	if err != nil {
		class, retryable := deepDiscoveryBrowserFailure(err)
		return failLocalExecutionWithRetry(ctx, control, sink, offer, class, "deep_discovery_browser", retryable, err)
	}
	if err := result.Attestation.Validate(request); err != nil {
		return failLocalExecution(ctx, control, sink, offer, "contract_violated", "deep_discovery_effect_policy", err)
	}
	finalURL, err := model.CanonicalHTTPURL(result.FinalURL)
	if err != nil {
		return failLocalExecution(ctx, control, sink, offer, "contract_violated", "deep_discovery_final_url", err)
	}
	responseRef, err := sink.Put(ctx, httpdriver.ArtifactWrite{Kind: "response", AttemptID: offer.Attempt.AttemptID,
		PageSequence: 1, URL: finalURL, ContentType: result.ContentType, Body: result.DOM})
	if err != nil {
		return fmt.Errorf("save Deep Discovery DOM: %w", err)
	}
	traceJSON, _ := json.Marshal(map[string]any{"attestation": result.Attestation,
		"public_query_evidence": result.PublicQueryEvidence})
	traceRef, err := sink.Put(ctx, httpdriver.ArtifactWrite{Kind: "trace", AttemptID: offer.Attempt.AttemptID,
		PageSequence: 2, URL: finalURL, ContentType: "application/json", Body: traceJSON})
	if err != nil {
		return fmt.Errorf("save Deep Discovery effect trace: %w", err)
	}
	primary, supporting, err := successfulResultArtifacts([]recipeabi.ArtifactRef{responseRef, traceRef}, responseRef, sink)
	if err != nil {
		return err
	}
	bodySum := sha256.Sum256(result.DOM)
	links := extractDeepDiscoveryLinks(finalURL, result.DOM, 200)
	wireLinks := make([]executioncontract.DeepDiscoveryLink, len(links))
	for index := range links {
		wireLinks[index] = executioncontract.DeepDiscoveryLink(links[index])
	}
	submission := executioncontract.DeepDiscoveryBrowserResult{CommandID: "deep-discovery-browser-result-" + offer.Attempt.AttemptID,
		ResultKind: "deep_discovery_browser", AttemptID: offer.Attempt.AttemptID,
		ExecutorIncarnation: offer.Attempt.ExecutorIncarnation, Artifact: primary, SupportingArtifacts: supporting,
		FinalURL: finalURL, ContentHash: "sha256:" + hex.EncodeToString(bodySum[:]), Links: wireLinks,
		PublicQueryEvidence: result.PublicQueryEvidence,
		Attestation: executioncontract.DeepDiscoveryEffectAttestation{DocumentNavigations: result.Attestation.DocumentNavigations,
			ObservedMethods: result.Attestation.ObservedMethods, BlockedMethods: result.Attestation.BlockedMethods,
			AllowedWriteRequests: result.Attestation.AllowedWriteRequests, BlockedWriteRequests: result.Attestation.BlockedWriteRequests,
			CrossOriginDocumentNavigations: result.Attestation.CrossOriginDocumentNavigations, FormSubmissions: result.Attestation.FormSubmissions,
			Downloads: result.Attestation.Downloads, Popups: result.Attestation.Popups, PublicEndpoint: result.Attestation.PublicEndpoint,
			RobotsAllowed: result.Attestation.RobotsAllowed, TermsPolicyVersion: result.Attestation.TermsPolicyVersion}}
	submission.Attestation.FulfilledPublicQueries = result.Attestation.FulfilledPublicQueries
	submission.Attestation.FulfilledPublicQueryHashes = result.Attestation.FulfilledPublicQueryHashes
	if err := control.Submit(ctx, submission.ResultKind, submission); err != nil {
		return fmt.Errorf("submit Deep Discovery browser result: %w", err)
	}
	return nil
}

func deepDiscoveryBrowserFailure(err error) (string, bool) {
	var classified browserdriver.ClassifiedBrokerError
	if !errors.As(err, &classified) {
		// Chrome navigation and broker transport errors are commonly returned as
		// ordinary errors. Treating them as a terminal upstream status strands a
		// discovery Mission at the first transient browser failure.
		return "transport_timeout", true
	}
	switch classified.BrowserFailureClass() {
	case "endpoint_rejected", "robots_disallowed", "auth_expired", "captcha", "parse_error":
		return classified.BrowserFailureClass(), false
	case "effect_policy_violated":
		return "contract_violated", false
	default:
		return "transport_timeout", true
	}
}

type deepDiscoveryLink = executioncontract.DeepDiscoveryLink

func extractDeepDiscoveryLinks(baseURL string, document []byte, limit int) []deepDiscoveryLink {
	base, err := url.Parse(baseURL)
	if err != nil || limit < 1 {
		return nil
	}
	root, err := html.Parse(bytes.NewReader(document))
	if err != nil {
		return nil
	}
	seen := map[string]struct{}{}
	result := make([]deepDiscoveryLink, 0, limit)
	var walk func(*html.Node)
	walk = func(node *html.Node) {
		if len(result) >= limit {
			return
		}
		if node.Type == html.ElementNode && node.Data == "a" {
			href := ""
			for _, attribute := range node.Attr {
				if strings.EqualFold(attribute.Key, "href") {
					href = strings.TrimSpace(attribute.Val)
					break
				}
			}
			target, parseErr := base.Parse(href)
			if parseErr == nil && (target.Scheme == "http" || target.Scheme == "https") && target.Host != "" && target.User == nil {
				target.Fragment = ""
				stripSensitiveQuery(target)
				canonical, canonicalErr := model.CanonicalHTTPURL(target.String())
				if canonicalErr == nil {
					if _, duplicate := seen[canonical]; !duplicate {
						seen[canonical] = struct{}{}
						result = append(result, deepDiscoveryLink{URL: canonical, Text: boundedLinkText(node, 200)})
					}
				}
			}
		}
		for child := node.FirstChild; child != nil; child = child.NextSibling {
			walk(child)
		}
	}
	walk(root)
	sort.SliceStable(result, func(i, j int) bool { return result[i].URL < result[j].URL })
	return result
}

func stripSensitiveQuery(value *url.URL) {
	query := value.Query()
	for key := range query {
		lower := strings.ToLower(key)
		if strings.Contains(lower, "token") || strings.Contains(lower, "secret") || strings.Contains(lower, "password") || strings.Contains(lower, "session") || strings.Contains(lower, "authorization") || strings.Contains(lower, "signature") || strings.Contains(lower, "api_key") || strings.Contains(lower, "apikey") {
			query.Del(key)
		}
	}
	value.RawQuery = query.Encode()
}
func boundedLinkText(node *html.Node, limit int) string {
	var builder strings.Builder
	var walk func(*html.Node)
	walk = func(current *html.Node) {
		if builder.Len() >= limit {
			return
		}
		if current.Type == html.TextNode {
			value := strings.Join(strings.Fields(current.Data), " ")
			if value != "" {
				if builder.Len() > 0 {
					builder.WriteByte(' ')
				}
				builder.WriteString(value)
			}
		}
		for child := current.FirstChild; child != nil; child = child.NextSibling {
			walk(child)
		}
	}
	walk(node)
	value := builder.String()
	for len(value) > limit {
		_, size := utf8.DecodeLastRuneInString(value)
		if size < 1 {
			return ""
		}
		value = value[:len(value)-size]
	}
	return value
}
