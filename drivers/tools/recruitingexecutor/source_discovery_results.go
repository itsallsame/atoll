package recruitingexecutor

import (
	"encoding/json"
	"errors"
	"fmt"
	"net/url"
	"strings"

	"github.com/wanpengxie/atoll/drivers/tools/recruiting/executioncontract"
	"github.com/wanpengxie/atoll/drivers/tools/recruiting/model"
	"github.com/wanpengxie/atoll/drivers/tools/recruitingexecutor/httpdriver"
)

func prepareSourceDiscoverySubmission(offer executioncontract.Offer, run httpdriver.DiscoveryRunResult,
	sink *atollArtifactSink) (executioncontract.SourceDiscoveryResult, error) {
	if sink == nil || offer.Kind != "source_discovery" || offer.Discovery == nil || offer.Recipe == nil ||
		run.Output.Failure != nil || run.Output.AttemptID != offer.Attempt.AttemptID || len(run.Output.Artifacts) != 1 ||
		run.Output.Artifacts[0] != run.ResponseArtifact || len(run.Items) > 500 {
		return executioncontract.SourceDiscoveryResult{}, errors.New("successful bounded source discovery run and response Artifact are required")
	}
	metadata, err := sink.metadata(run.ResponseArtifact, model.ArtifactResponse)
	if err != nil {
		return executioncontract.SourceDiscoveryResult{}, err
	}
	base, err := url.Parse(offer.Discovery.SeedURL)
	if err != nil {
		return executioncontract.SourceDiscoveryResult{}, err
	}
	candidates := make([]model.SourceDiscoveryCandidate, 0, len(run.Items))
	seen := make(map[string]struct{}, len(run.Items))
	for index, item := range run.Items {
		endpoint, err := listingString(item["endpoint"])
		if err != nil {
			return executioncontract.SourceDiscoveryResult{}, fmt.Errorf("candidate %d endpoint: %w", index, err)
		}
		reference, err := url.Parse(endpoint)
		if err != nil {
			return executioncontract.SourceDiscoveryResult{}, fmt.Errorf("candidate %d endpoint: %w", index, err)
		}
		resolved := base.ResolveReference(reference)
		if resolved.User != nil || resolved.Fragment != "" || (resolved.Scheme != "http" && resolved.Scheme != "https") || resolved.Host == "" {
			return executioncontract.SourceDiscoveryResult{}, fmt.Errorf("candidate %d endpoint is not an absolute safe HTTP URL", index)
		}
		confidence, err := listingString(item["confidence_basis"])
		if err != nil {
			return executioncontract.SourceDiscoveryResult{}, fmt.Errorf("candidate %d confidence_basis: %w", index, err)
		}
		category := ""
		if raw, exists := item["category"]; exists && len(raw) != 0 {
			if err := json.Unmarshal(raw, &category); err != nil {
				return executioncontract.SourceDiscoveryResult{}, fmt.Errorf("candidate %d category must be a string", index)
			}
		}
		candidate, err := model.NewSourceDiscoveryCandidate(resolved.String(), strings.TrimSpace(category), resolved.String(),
			confidence, metadata.ArtifactID)
		if err != nil {
			return executioncontract.SourceDiscoveryResult{}, fmt.Errorf("candidate %d: %w", index, err)
		}
		if _, duplicate := seen[candidate.CandidateID]; duplicate {
			continue
		}
		seen[candidate.CandidateID] = struct{}{}
		candidates = append(candidates, candidate)
	}
	return executioncontract.SourceDiscoveryResult{CommandID: "source-discovery-result-" + offer.Attempt.AttemptID,
		ResultKind: "source_discovery", AttemptID: offer.Attempt.AttemptID,
		ExecutorIncarnation: offer.Attempt.ExecutorIncarnation, Artifact: metadata, Candidates: candidates}, nil
}
