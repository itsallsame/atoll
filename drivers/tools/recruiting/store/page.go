package store

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"time"

	"github.com/wanpengxie/atoll/drivers/tools/recruiting/model"
)

const listingPageMaxItems = 500

type ListingPageCommit struct {
	Progress model.ListingPageProgress
	Items    []ListingIngest
}

type ListingPageCommitResult struct {
	Items    []ListingIngestResult
	Replayed bool
}

// ApplyListingPage makes page facts, derived detail-work intents, and the
// append-only resume point one atomic unit.
func (r *Repository) ApplyListingPage(ctx context.Context, page ListingPageCommit, businessAt time.Time) (ListingPageCommitResult, error) {
	if page.Progress.WorkID == "" || page.Progress.PageSequence == 0 || page.Progress.ArtifactID == "" ||
		page.Progress.ItemCount != len(page.Items) || len(page.Items) > listingPageMaxItems || businessAt.IsZero() {
		return ListingPageCommitResult{}, fmt.Errorf("listing page requires progress, matching items, and business time")
	}
	for index := range page.Items {
		input := page.Items[index]
		if input.Observation.ObservationID == "" || input.NewJobID == "" || input.DetailWorkID == "" ||
			input.Origin == "" || input.Capability == "" || input.ObservedAt.IsZero() || input.NotBefore.IsZero() ||
			input.Observation.ArtifactID != page.Progress.ArtifactID {
			return ListingPageCommitResult{}, fmt.Errorf("listing page item %d is incomplete or belongs to another page artifact", index)
		}
		validated, err := model.NewListingObservation(input.Observation)
		if err != nil {
			return ListingPageCommitResult{}, err
		}
		page.Items[index].Observation = validated
	}
	for attempt := 0; attempt < 3; attempt++ {
		result, err := r.applyListingPageOnce(ctx, page, businessAt)
		if !isRetryableTransactionError(err) {
			return result, err
		}
	}
	return ListingPageCommitResult{}, fmt.Errorf("listing page exhausted transaction retries")
}

func (r *Repository) applyListingPageOnce(ctx context.Context, page ListingPageCommit, businessAt time.Time) (ListingPageCommitResult, error) {
	tx, err := r.db.BeginTx(ctx, &sql.TxOptions{Isolation: sql.LevelReadCommitted})
	if err != nil {
		return ListingPageCommitResult{}, fmt.Errorf("begin listing page: %w", err)
	}
	defer tx.Rollback()

	var workState []byte
	if err := tx.QueryRowContext(ctx, "SELECT state_json FROM recruiting_works WHERE work_id = ? FOR UPDATE", page.Progress.WorkID).Scan(&workState); err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return ListingPageCommitResult{}, ErrNotFound
		}
		return ListingPageCommitResult{}, fmt.Errorf("lock listing work: %w", err)
	}
	var work model.Work
	if err := json.Unmarshal(workState, &work); err != nil {
		return ListingPageCommitResult{}, fmt.Errorf("decode listing work: %w", err)
	}
	if work.Status != model.WorkRunning || work.Version != page.Progress.WorkVersion || work.AcceptanceVersion != page.Progress.WorkAcceptanceVersion {
		return ListingPageCommitResult{}, ErrProgressConflict
	}

	latest, found, err := getLatestListingProgressTx(ctx, tx, page.Progress.WorkID, "")
	if err != nil {
		return ListingPageCommitResult{}, err
	}
	if found && latest.PageSequence == page.Progress.PageSequence {
		if latest == page.Progress {
			return ListingPageCommitResult{Replayed: true}, nil
		}
		return ListingPageCommitResult{}, ErrProgressConflict
	}
	if (!found && page.Progress.PageSequence != 1) || (found && (latest.EndOfInput || page.Progress.PageSequence != latest.PageSequence+1)) {
		return ListingPageCommitResult{}, ErrProgressConflict
	}

	results := make([]ListingIngestResult, 0, len(page.Items))
	for _, input := range page.Items {
		result, err := applyListingObservationTx(ctx, tx, input)
		if err != nil {
			return ListingPageCommitResult{}, err
		}
		results = append(results, result)
	}
	state, _ := json.Marshal(page.Progress)
	_, err = tx.ExecContext(ctx, "INSERT INTO recruiting_listing_page_progress("+
		"work_id, attempt_id, page_sequence, resume_cursor, artifact_id, item_count, end_of_input, "+
		"work_version, work_acceptance_version, state_json, committed_at"+
		") VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)", page.Progress.WorkID, nullableString(page.Progress.AttemptID), page.Progress.PageSequence,
		nullableString(page.Progress.ResumeCursor), page.Progress.ArtifactID, page.Progress.ItemCount, page.Progress.EndOfInput,
		page.Progress.WorkVersion, page.Progress.WorkAcceptanceVersion, state, businessAt.UTC())
	if err != nil {
		return ListingPageCommitResult{}, fmt.Errorf("append listing page progress: %w", err)
	}
	if err := tx.Commit(); err != nil {
		return ListingPageCommitResult{}, fmt.Errorf("commit listing page: %w", err)
	}
	return ListingPageCommitResult{Items: results}, nil
}

func (r *Repository) GetLatestListingProgress(ctx context.Context, workID string) (model.ListingPageProgress, error) {
	var state []byte
	err := r.db.QueryRowContext(ctx, "SELECT state_json FROM recruiting_listing_page_progress "+
		"WHERE work_id = ? ORDER BY committed_at DESC, progress_id DESC LIMIT 1", workID).Scan(&state)
	if errors.Is(err, sql.ErrNoRows) {
		return model.ListingPageProgress{}, ErrNotFound
	}
	if err != nil {
		return model.ListingPageProgress{}, fmt.Errorf("get latest listing progress: %w", err)
	}
	var progress model.ListingPageProgress
	if err := json.Unmarshal(state, &progress); err != nil {
		return model.ListingPageProgress{}, fmt.Errorf("decode latest listing progress: %w", err)
	}
	return progress, nil
}

func getLatestListingProgressTx(ctx context.Context, tx *sql.Tx, workID, attemptID string) (model.ListingPageProgress, bool, error) {
	var state []byte
	query := "SELECT state_json FROM recruiting_listing_page_progress WHERE work_id = ? AND attempt_id = ? ORDER BY page_sequence DESC LIMIT 1 FOR UPDATE"
	args := []any{workID, attemptID}
	if attemptID == "" {
		query = "SELECT state_json FROM recruiting_listing_page_progress WHERE work_id = ? AND attempt_id IS NULL ORDER BY page_sequence DESC LIMIT 1 FOR UPDATE"
		args = []any{workID}
	}
	err := tx.QueryRowContext(ctx, query, args...).Scan(&state)
	if errors.Is(err, sql.ErrNoRows) {
		return model.ListingPageProgress{}, false, nil
	}
	if err != nil {
		return model.ListingPageProgress{}, false, fmt.Errorf("lock latest listing progress: %w", err)
	}
	var progress model.ListingPageProgress
	if err := json.Unmarshal(state, &progress); err != nil {
		return model.ListingPageProgress{}, false, fmt.Errorf("decode latest listing progress: %w", err)
	}
	return progress, true, nil
}
