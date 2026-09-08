package recruitingexecutor

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"math/big"
	"net/url"
	"strconv"
	"strings"

	"github.com/wanpengxie/atoll/drivers/tools/recruiting/executioncontract"
	"github.com/wanpengxie/atoll/drivers/tools/recruiting/model"
	"github.com/wanpengxie/atoll/drivers/tools/recruitingexecutor/httpdriver"
	"github.com/wanpengxie/atoll/drivers/tools/recruitingexecutor/recipeabi"
)

type listingSubmissions struct {
	Pages      []executioncontract.ListingPageResult
	Completion executioncontract.ListingCompletionResult
}

func prepareListingSubmissions(ctx context.Context, offer executioncontract.Offer, spec recipeabi.Spec,
	run httpdriver.ListingRunResult, sink *atollArtifactSink) (listingSubmissions, error) {
	if ctx == nil || sink == nil || offer.Kind != "listing" || offer.Occurrence == nil || spec.Kind != recipeabi.KindListing ||
		run.Output.Failure != nil || run.CheckpointCandidate == nil || run.Output.AttemptID != offer.Attempt.AttemptID || len(run.Pages) == 0 {
		return listingSubmissions{}, errors.New("successful listing offer, recipe, run, and artifact sink are required")
	}
	if run.Output.Quality.ItemCount < 0 || len(run.Pages) != len(run.Output.Artifacts) {
		return listingSubmissions{}, errors.New("listing run pages, artifacts, or item count are inconsistent")
	}

	submissions := listingSubmissions{Pages: make([]executioncontract.ListingPageResult, 0, len(run.Pages))}
	totalItems := 0
	for index, page := range run.Pages {
		sequence := uint64(index + 1)
		if page.Sequence != sequence || page.Artifact != run.Output.Artifacts[index] || page.Terminal != (index == len(run.Pages)-1) ||
			(page.Terminal && page.ResumeCursor != "") || (!page.Terminal && strings.TrimSpace(page.ResumeCursor) == "") || len(page.Items) > 500 {
			return listingSubmissions{}, fmt.Errorf("listing page %d sequence, artifact, terminal, cursor, or size is inconsistent", sequence)
		}
		metadata, err := sink.metadata(page.Artifact, model.ArtifactPage)
		if err != nil {
			return listingSubmissions{}, err
		}
		observations := make([]model.ListingObservation, 0, len(page.Items))
		for itemIndex, item := range page.Items {
			observation, err := listingObservation(offer, spec, page, metadata.ArtifactID, item, itemIndex)
			if err != nil {
				return listingSubmissions{}, fmt.Errorf("listing page %d item %d: %w", sequence, itemIndex, err)
			}
			observations = append(observations, observation)
		}
		totalItems += len(observations)
		submissions.Pages = append(submissions.Pages, executioncontract.ListingPageResult{
			CommandID: "listing-page-" + offer.Attempt.AttemptID + "-" + strconv.FormatUint(sequence, 10), ResultKind: "listing_page",
			AttemptID: offer.Attempt.AttemptID, ExecutorIncarnation: offer.Attempt.ExecutorIncarnation,
			PageSequence: sequence, ResumeCursor: page.ResumeCursor, Terminal: page.Terminal, Artifact: metadata, Observations: observations,
		})
	}
	if totalItems != run.Output.Quality.ItemCount {
		return listingSubmissions{}, fmt.Errorf("listing run quality count %d does not match %d unique page observations", run.Output.Quality.ItemCount, totalItems)
	}
	deltaRef, err := sink.Put(ctx, httpdriver.ArtifactWrite{Kind: "listing_delta", AttemptID: offer.Attempt.AttemptID,
		PageSequence: len(run.Pages) + 1, URL: offer.Occurrence.ListingExecution.Endpoint.URL,
		ContentType: "application/json", Body: run.Output.Result})
	if err != nil {
		return listingSubmissions{}, fmt.Errorf("save listing delta artifact: %w", err)
	}
	deltaMetadata, err := sink.metadata(deltaRef, model.ArtifactListingDelta)
	if err != nil {
		return listingSubmissions{}, err
	}
	quality := run.Output.Quality
	submissions.Completion = executioncontract.ListingCompletionResult{
		CommandID: "listing-completion-" + offer.Attempt.AttemptID, ResultKind: "listing_completion",
		AttemptID: offer.Attempt.AttemptID, ExecutorIncarnation: offer.Attempt.ExecutorIncarnation, Artifact: deltaMetadata,
		Quality: executioncontract.ListingQuality{IdentityComplete: quality.IdentityComplete, OrderingContractHeld: quality.OrderingContractHeld,
			PaginationStable: quality.PaginationStable, PreviousFrontierReached: quality.PreviousFrontierReached,
			OverlapCompleted: quality.OverlapCompleted, ItemCount: quality.ItemCount},
		Checkpoint: executioncontract.ListingCheckpointCandidate{FrontierActivityAt: run.CheckpointCandidate.LastActivityAt,
			FrontierJobKeys: append([]string(nil), run.CheckpointCandidate.FrontierKeys...)},
	}
	return submissions, nil
}

func (s *atollArtifactSink) metadata(ref recipeabi.ArtifactRef, kind model.ArtifactKind) (model.ArtifactMetadata, error) {
	if err := ref.Validate(); err != nil {
		return model.ArtifactMetadata{}, err
	}
	metadata, err := model.NewArtifactMetadata(ref.ArtifactID, kind, ref.ContentHash, ref.ObjectRef, s.config.WorkID,
		s.config.AttemptID, s.config.AccessScope, s.config.Retention, s.config.Redacted)
	if err != nil {
		return model.ArtifactMetadata{}, err
	}
	return metadata, nil
}

func listingObservation(offer executioncontract.Offer, spec recipeabi.Spec, page httpdriver.ListingPage,
	artifactID string, item map[string]json.RawMessage, itemIndex int) (model.ListingObservation, error) {
	jobKey, err := listingIdentity(item[spec.Listing.IdentityField])
	if err != nil {
		return model.ListingObservation{}, err
	}
	detailText, err := listingString(item[spec.Listing.DetailURLField])
	if err != nil {
		return model.ListingObservation{}, fmt.Errorf("detail URL: %w", err)
	}
	detailURL, err := resolveListingDetailURL(page.URL, detailText)
	if err != nil {
		return model.ListingObservation{}, err
	}
	activity := ""
	if spec.Listing.ActivityField != "" {
		activity, err = listingString(item[spec.Listing.ActivityField])
		if err != nil {
			return model.ListingObservation{}, fmt.Errorf("activity: %w", err)
		}
	}
	canonicalItem, err := json.Marshal(item)
	if err != nil {
		return model.ListingObservation{}, err
	}
	fingerprintSum := sha256.Sum256(canonicalItem)
	identitySum := sha256.Sum256([]byte("recruiting.observation.v1\n" + offer.Attempt.AttemptID + "\n" +
		strconv.FormatUint(page.Sequence, 10) + "\n" + jobKey + "\n" + strconv.Itoa(itemIndex)))
	return model.NewListingObservation(model.ListingObservation{
		ObservationID: "observation-" + hex.EncodeToString(identitySum[:16]), OccurrenceID: offer.Occurrence.OccurrenceID,
		SourceID: offer.Occurrence.SourceID, SourceJobKey: jobKey, DetailURL: detailURL, ActivityAt: activity,
		ListingFingerprint: "sha256:" + hex.EncodeToString(fingerprintSum[:]), RecipeID: offer.Attempt.RecipeID,
		RecipeVersion: offer.Attempt.RecipeVersion, ArtifactID: artifactID,
	})
}

func listingIdentity(raw json.RawMessage) (string, error) {
	decoder := json.NewDecoder(bytes.NewReader(raw))
	decoder.UseNumber()
	var value any
	if err := decoder.Decode(&value); err != nil {
		return "", errors.New("identity must be a string or integer")
	}
	var trailing any
	if err := decoder.Decode(&trailing); !errors.Is(err, io.EOF) {
		return "", errors.New("identity contains trailing JSON")
	}
	switch typed := value.(type) {
	case string:
		if strings.TrimSpace(typed) == "" {
			return "", errors.New("identity is empty")
		}
		return typed, nil
	case json.Number:
		integer := new(big.Int)
		if _, ok := integer.SetString(string(typed), 10); !ok {
			return "", errors.New("identity number must be an integer")
		}
		return integer.String(), nil
	default:
		return "", errors.New("identity must be a string or integer")
	}
}

func listingString(raw json.RawMessage) (string, error) {
	var value string
	if len(raw) == 0 || json.Unmarshal(raw, &value) != nil || strings.TrimSpace(value) == "" {
		return "", errors.New("value must be a non-empty JSON string")
	}
	return strings.TrimSpace(value), nil
}

func resolveListingDetailURL(pageURL, reference string) (string, error) {
	base, err := url.Parse(pageURL)
	if err != nil || (base.Scheme != "http" && base.Scheme != "https") || base.Host == "" || base.User != nil {
		return "", errors.New("listing page URL is invalid")
	}
	relative, err := url.Parse(reference)
	if err != nil || relative.User != nil || relative.Fragment != "" {
		return "", errors.New("listing detail URL is invalid")
	}
	resolved := base.ResolveReference(relative)
	canonical, err := model.CanonicalHTTPURL(resolved.String())
	if err != nil {
		return "", fmt.Errorf("canonicalize listing detail URL: %w", err)
	}
	return canonical, nil
}
