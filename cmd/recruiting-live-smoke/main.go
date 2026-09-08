// Command recruiting-live-smoke performs one low-frequency, read-only check
// against a documented public job-board API. It is an acceptance tool, not a
// scheduler and not part of the Atoll core binary set.
package main

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"net/url"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"time"

	"github.com/wanpengxie/atoll/drivers/tools/recruitingexecutor/httpdriver"
	"github.com/wanpengxie/atoll/drivers/tools/recruitingexecutor/recipeabi"
)

const (
	sourceURL = "https://boards-api.greenhouse.io/v1/boards/mongodb/jobs"
	docsURL   = "https://docs.greenhouse.io/job-board.html"
)

type artifactRecord struct {
	Ref         recipeabi.ArtifactRef          `json:"ref"`
	Kind        string                         `json:"kind"`
	URL         string                         `json:"url"`
	StatusCode  int                            `json:"status_code,omitempty"`
	ContentType string                         `json:"content_type,omitempty"`
	Robots      *httpdriver.RobotsEvidence     `json:"robots,omitempty"`
	Compliance  *httpdriver.ComplianceEvidence `json:"compliance,omitempty"`
}

type fileSink struct {
	dir     string
	records []artifactRecord
}

func (s *fileSink) Put(ctx context.Context, write httpdriver.ArtifactWrite) (recipeabi.ArtifactRef, error) {
	if err := ctx.Err(); err != nil {
		return recipeabi.ArtifactRef{}, err
	}
	if err := os.MkdirAll(s.dir, 0o700); err != nil {
		return recipeabi.ArtifactRef{}, err
	}
	kind := safeToken(write.Kind)
	if kind == "" {
		return recipeabi.ArtifactRef{}, fmt.Errorf("artifact kind is empty")
	}
	hashToken := strings.TrimPrefix(write.ContentHash, "sha256:")
	if len(hashToken) < 12 {
		return recipeabi.ArtifactRef{}, fmt.Errorf("artifact content hash is malformed")
	}
	name := fmt.Sprintf("%03d-%s-%s.bin", write.PageSequence, kind, hashToken[:12])
	path := filepath.Join(s.dir, name)
	file, err := os.OpenFile(path, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0o600)
	if err != nil {
		return recipeabi.ArtifactRef{}, err
	}
	_, writeErr := file.Write(write.Body)
	closeErr := file.Close()
	if writeErr != nil {
		return recipeabi.ArtifactRef{}, writeErr
	}
	if closeErr != nil {
		return recipeabi.ArtifactRef{}, closeErr
	}
	absolute, err := filepath.Abs(path)
	if err != nil {
		return recipeabi.ArtifactRef{}, err
	}
	objectRef := (&url.URL{Scheme: "file", Path: absolute}).String()
	ref := recipeabi.ArtifactRef{ArtifactID: strings.TrimSuffix(name, ".bin"), ContentHash: write.ContentHash, ObjectRef: objectRef}
	record := artifactRecord{Ref: ref, Kind: write.Kind, URL: write.URL, StatusCode: write.StatusCode, ContentType: write.ContentType}
	if write.Robots.ContentHash != "" {
		robots := write.Robots
		record.Robots = &robots
	}
	if write.Compliance.TermsPolicyVersion != 0 {
		compliance := write.Compliance
		record.Compliance = &compliance
	}
	s.records = append(s.records, record)
	return ref, nil
}

type report struct {
	SchemaVersion                 string                 `json:"schema_version"`
	Status                        string                 `json:"status"`
	StartedAt                     string                 `json:"started_at"`
	FinishedAt                    string                 `json:"finished_at"`
	SourceURL                     string                 `json:"source_url"`
	PublicAPIDocumentation        string                 `json:"public_api_documentation"`
	RecipeVersion                 uint64                 `json:"recipe_version"`
	RecipeHash                    string                 `json:"recipe_hash"`
	ObservedItems                 int                    `json:"observed_items"`
	Quality                       recipeabi.QualityProof `json:"quality"`
	Failure                       *recipeabi.Failure     `json:"failure,omitempty"`
	CheckpointCandidateGenerated  bool                   `json:"checkpoint_candidate_generated"`
	CheckpointCommitted           bool                   `json:"checkpoint_committed"`
	ProductionIncrementalEligible bool                   `json:"production_incremental_eligible"`
	EligibilityReasons            []string               `json:"eligibility_reasons"`
	Artifacts                     []artifactRecord       `json:"artifacts"`
}

func candidateSpec() recipeabi.Spec {
	return recipeabi.Spec{
		ABIVersion: recipeabi.Version, Kind: recipeabi.KindListing, RequiredCapability: "http.fetch", Transport: recipeabi.TransportHTTPJSON,
		Request: recipeabi.ReadRequest{Method: "GET", Headers: map[string]string{"Accept": "application/json"}, TimeoutMS: 20_000,
			MaxResponseBytes: 2 << 20, MaxRedirects: 0, UserAgent: "Atoll-Recruiting-Live-Smoke/1 (+read-only acceptance test)"},
		Extraction: recipeabi.Extraction{Collection: "/jobs", Fields: map[string]string{
			"job_key": "/id", "title": "/title", "activity_at": "/updated_at", "detail_url": "/absolute_url",
		}},
		Listing: &recipeabi.ListingContract{IdentityField: "job_key", ActivityField: "activity_at", BoundaryMode: "activity_time",
			Ordering: "newest_activity_desc", UpdateRetop: true, OverlapPages: 1, MaxPages: 1, MaxItemsPerPage: 1_000,
			MaxTotalBytes: 2 << 20, FrontierWidth: 20},
	}
}

func run(ctx context.Context, artifactDir string, now time.Time) (report, error) {
	started := now.UTC()
	spec := candidateSpec()
	recipeHash, err := spec.ContentHash()
	if err != nil {
		return report{}, err
	}
	contractRaw, _ := json.Marshal(spec.Listing)
	contractSum := sha256.Sum256(contractRaw)
	contractHash := "sha256:" + hex.EncodeToString(contractSum[:])
	attemptID := "greenhouse-mongodb-" + started.Format("20060102T150405Z")
	input := recipeabi.RunInput{ABIVersion: recipeabi.Version, Target: recipeabi.TargetRef{Kind: "source", ID: "live-greenhouse-mongodb"},
		Endpoint:   recipeabi.EndpointRef{URL: sourceURL, Version: 1},
		Assignment: recipeabi.AssignmentRef{RecipeID: "greenhouse-public-list-v1", RecipeVersion: 1, AssignmentVersion: 1, ContractHash: contractHash},
		Budget:     recipeabi.BudgetRef{PermitID: "live-smoke-one-request", PolicyVersion: 1},
		Attempt:    recipeabi.AttemptFence{WorkID: attemptID, AttemptID: attemptID, AcceptanceVersion: 1, CompanyVersion: 1, SourceVersion: 1}}
	robots, err := httpdriver.NewRobotsTxtChecker(httpdriver.RobotsPolicy{Timeout: 10 * time.Second, MaxBytes: 64 << 10, CacheTTL: time.Hour})
	if err != nil {
		return report{}, err
	}
	driver, err := httpdriver.New(httpdriver.Policy{MaxConcurrency: 1, MinOriginInterval: 2 * time.Second, CircuitThreshold: 2, CircuitCooldown: time.Minute}, robots)
	if err != nil {
		return report{}, err
	}
	sink := &fileSink{dir: artifactDir}
	compliance := httpdriver.ComplianceEvidence{TermsPolicyVersion: 1, TermsReviewedAt: "2026-09-08T00:00:00Z"}
	result, runErr := driver.RunListing(ctx, spec, input, compliance, sink)
	finished := time.Now().UTC()
	rep := report{SchemaVersion: "recruiting.live-smoke.v1", StartedAt: started.Format(time.RFC3339), FinishedAt: finished.Format(time.RFC3339),
		SourceURL: sourceURL, PublicAPIDocumentation: docsURL, RecipeVersion: 1, RecipeHash: recipeHash,
		ObservedItems: result.Output.Quality.ItemCount, Quality: result.Output.Quality, Failure: result.Output.Failure,
		CheckpointCandidateGenerated: result.CheckpointCandidate != nil, CheckpointCommitted: false,
		ProductionIncrementalEligible: false, Artifacts: sink.records,
		EligibilityReasons: []string{"update-retop behavior has no verified historical-update evidence"}}
	if runErr != nil {
		rep.Status = "diagnostic_failed"
		return rep, runErr
	}
	if result.Output.Failure != nil {
		if result.Output.Failure.Class != "quality_rejected" && result.Output.Failure.Class != "contract_violated" {
			rep.Status = "diagnostic_failed"
			return rep, fmt.Errorf("live recipe failed with %s", result.Output.Failure.Class)
		}
		rep.EligibilityReasons = append(rep.EligibilityReasons, "observed listing did not satisfy the declared ordering contract")
	}
	if result.CheckpointCandidate != nil {
		// A single snapshot can observe order but cannot prove update-retop. The
		// smoke tool deliberately discards any candidate produced by the runner.
		rep.EligibilityReasons = append(rep.EligibilityReasons, "candidate checkpoint discarded because source contract is not certified")
	}
	if len(sink.records) == 0 || result.Output.Quality.ItemCount == 0 {
		rep.Status = "diagnostic_failed"
		return rep, errors.New("live source returned no auditable artifact or job observations")
	}
	rep.Status = "diagnostic_pass"
	return rep, nil
}

var unsafeToken = regexp.MustCompile(`[^a-zA-Z0-9_-]+`)

func safeToken(value string) string {
	return strings.Trim(unsafeToken.ReplaceAllString(value, "-"), "-")
}

func main() {
	artifactDir := flag.String("artifact-dir", "", "required directory for raw artifacts")
	reportPath := flag.String("report", "", "optional path for the compact JSON report")
	flag.Parse()
	if strings.TrimSpace(*artifactDir) == "" {
		fmt.Fprintln(os.Stderr, "-artifact-dir is required")
		os.Exit(2)
	}
	rep, err := run(context.Background(), *artifactDir, time.Now())
	raw, marshalErr := json.MarshalIndent(rep, "", "  ")
	if marshalErr == nil {
		raw = append(raw, '\n')
		_, _ = os.Stdout.Write(raw)
		if *reportPath != "" {
			if mkdirErr := os.MkdirAll(filepath.Dir(*reportPath), 0o700); mkdirErr == nil {
				marshalErr = os.WriteFile(*reportPath, raw, 0o600)
			} else {
				marshalErr = mkdirErr
			}
		}
	}
	if err != nil || marshalErr != nil {
		fmt.Fprintln(os.Stderr, errors.Join(err, marshalErr))
		os.Exit(1)
	}
}
