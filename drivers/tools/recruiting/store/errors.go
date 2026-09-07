package store

import "errors"

var (
	ErrNotFound          = errors.New("recruiting entity not found")
	ErrBusinessKeyExists = errors.New("recruiting business key already exists")
	ErrCommandConflict   = errors.New("command ID reused with different request")
	ErrAttemptConflict   = errors.New("attempt state changed")
)
