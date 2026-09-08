package store

import (
	"encoding/base64"
	"errors"
	"testing"
)

func TestOpaqueCursorDecoderRejectsShapeChangesAndTrailingValues(t *testing.T) {
	valid := encodeCursor(jobCursor{SourceID: "source-1", UpdatedAt: "2026-09-08T00:00:00Z", JobID: "job-1"})
	if _, err := decodeCursor[jobCursor](valid); err != nil {
		t.Fatal(err)
	}
	for _, value := range []string{
		"not-base64",
		encodeCursor(map[string]any{"source_id": "source-1", "updated_at": "2026-09-08T00:00:00Z", "job_id": "job-1", "unexpected": true}),
		base64.RawURLEncoding.EncodeToString([]byte(`{"source_id":"source-1"} {}`)),
	} {
		if _, err := decodeCursor[jobCursor](value); !errors.Is(err, ErrInvalidCursor) {
			t.Fatalf("unsafe cursor %q = %v", value, err)
		}
	}
}
