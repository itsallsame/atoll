package store

import "errors"

var (
	ErrNotFound          = errors.New("recruiting entity not found")
	ErrBusinessKeyExists = errors.New("recruiting business key already exists")
)
