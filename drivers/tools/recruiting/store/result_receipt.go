package store

import (
	"context"
	"database/sql"
	"encoding/json"
	"fmt"
	"strings"
	"time"

	"github.com/wanpengxie/atoll/drivers/tools/recruiting/model"
)

func validateOptionalResultCommand(commandID, requestHash string) error {
	if (strings.TrimSpace(commandID) == "") != (strings.TrimSpace(requestHash) == "") {
		return fmt.Errorf("result command ID and request hash must be supplied together")
	}
	if commandID != strings.TrimSpace(commandID) || requestHash != strings.TrimSpace(requestHash) {
		return fmt.Errorf("result command identity and hash must be normalized")
	}
	return nil
}

func readResultReceipt[T any](ctx context.Context, tx *sql.Tx, commandID, requestHash string) (T, bool, error) {
	var zero T
	if commandID == "" {
		return zero, false, nil
	}
	raw, found, err := readCommandReceipt(ctx, tx, commandID, requestHash)
	if err != nil || !found {
		return zero, false, err
	}
	var outcome T
	if err := json.Unmarshal(raw, &outcome); err != nil {
		return zero, false, fmt.Errorf("decode result command receipt: %w", err)
	}
	return outcome, true, nil
}

func reserveResultReceipt(ctx context.Context, tx *sql.Tx, commandID, word, requestHash string, outcome any, businessAt time.Time) error {
	if commandID == "" {
		return nil
	}
	raw, err := json.Marshal(outcome)
	if err != nil {
		return fmt.Errorf("encode result command receipt: %w", err)
	}
	receipt, err := model.NewCommandReceipt(commandID, word, requestHash, raw)
	if err != nil {
		return err
	}
	return reserveCommandReceipt(ctx, tx, receipt, businessAt)
}
