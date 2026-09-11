package recruitingexecutor

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"path"
	"strconv"
	"strings"

	"github.com/wanpengxie/atoll/drivers/tools/recruiting/model"
	"github.com/wanpengxie/atoll/drivers/tools/recruitingexecutor/httpdriver"
	"github.com/wanpengxie/atoll/drivers/tools/recruitingexecutor/recipeabi"
	"github.com/wanpengxie/atoll/protocol/access"
	"github.com/wanpengxie/atoll/runtime/accessdoor"
)

type artifactSinkConfig struct {
	DeviceName  string
	ChannelName string
	Directory   string
	WorkID      string
	AttemptID   string
	AccessScope string
	Retention   string
	Redacted    bool
	MaxBytes    int64
}

type atollArtifactSink struct {
	resources resourceArtifactStore
	config    artifactSinkConfig
}

func newAtollArtifactSink(resources resourceArtifactStore, config artifactSinkConfig) (*atollArtifactSink, error) {
	config.DeviceName, config.ChannelName = strings.TrimSpace(config.DeviceName), strings.TrimSpace(config.ChannelName)
	config.Directory, config.AccessScope, config.Retention = strings.Trim(strings.TrimSpace(config.Directory), "/"),
		strings.TrimSpace(config.AccessScope), strings.TrimSpace(config.Retention)
	if resources == nil || config.DeviceName == "" || config.ChannelName == "" || config.Directory == "" ||
		strings.TrimSpace(config.WorkID) == "" || strings.TrimSpace(config.AttemptID) == "" ||
		config.AccessScope == "" || config.Retention == "" || config.MaxBytes < 1 || config.MaxBytes > 20<<20 {
		return nil, errors.New("artifact sink requires Resource access, location, policy metadata, and a byte limit in [1,20MiB]")
	}
	// A directory is an execution-owned evidence boundary, not a shared
	// mutable bucket. Apart from avoiding cross-attempt name collisions, this
	// means every execution creates a fresh directory: remote Resource drivers
	// are not required to normalize an existing-directory error to
	// access.AlreadyExists consistently across operating systems.
	if config.AttemptID == "." || config.AttemptID == ".." || path.Base(config.AttemptID) != config.AttemptID ||
		strings.Contains(config.AttemptID, `\`) {
		return nil, errors.New("artifact sink attempt ID must be a safe path segment")
	}
	config.Directory = path.Join(path.Dir(config.Directory), path.Base(config.Directory)+"--"+config.AttemptID)
	directory, err := accessdoor.FormatFileAddress(config.DeviceName, config.ChannelName, config.Directory)
	if err != nil {
		return nil, fmt.Errorf("format artifact directory: %w", err)
	}
	outcome, err := resources.CreateDirectory(directory)
	if err != nil {
		return nil, fmt.Errorf("create artifact directory: %w", err)
	}
	if !outcome.Accepted() && outcome.RejectReason != access.AlreadyExists {
		return nil, fmt.Errorf("create artifact directory rejected: %s", outcome.RejectReason)
	}
	return &atollArtifactSink{resources: resources, config: config}, nil
}

func (s *atollArtifactSink) Put(ctx context.Context, write httpdriver.ArtifactWrite) (recipeabi.ArtifactRef, error) {
	if ctx == nil {
		return recipeabi.ArtifactRef{}, errors.New("artifact write context is required")
	}
	if err := ctx.Err(); err != nil {
		return recipeabi.ArtifactRef{}, err
	}
	kind, err := artifactKind(write.Kind)
	if err != nil {
		return recipeabi.ArtifactRef{}, err
	}
	if strings.TrimSpace(write.AttemptID) == "" || write.AttemptID != s.config.AttemptID || write.PageSequence < 1 || len(write.Body) > int(s.config.MaxBytes) {
		return recipeabi.ArtifactRef{}, errors.New("artifact write requires attempt, positive sequence, and bounded body")
	}
	contentSum := sha256.Sum256(write.Body)
	contentHash := "sha256:" + hex.EncodeToString(contentSum[:])
	if write.ContentHash != "" && write.ContentHash != contentHash {
		return recipeabi.ArtifactRef{}, errors.New("artifact write content hash does not match its bytes")
	}
	identitySum := sha256.Sum256([]byte("recruiting.artifact.v1\n" + write.AttemptID + "\n" + write.Kind + "\n" +
		strconv.Itoa(write.PageSequence) + "\n" + write.URL + "\n" + contentHash))
	artifactID := "artifact-" + hex.EncodeToString(identitySum[:16])
	address, err := accessdoor.FormatFileAddress(s.config.DeviceName, s.config.ChannelName,
		path.Join(s.config.Directory, artifactID+".bin"))
	if err != nil {
		return recipeabi.ArtifactRef{}, fmt.Errorf("format artifact address: %w", err)
	}
	metadata, err := storeArtifact(s.resources, artifactWrite{Address: address, ArtifactID: artifactID, Kind: kind,
		WorkID: s.config.WorkID, AttemptID: s.config.AttemptID, AccessScope: s.config.AccessScope, Retention: s.config.Retention,
		Redacted: s.config.Redacted, Content: bytes.NewReader(write.Body), MaxBytes: s.config.MaxBytes})
	if err != nil {
		return recipeabi.ArtifactRef{}, err
	}
	return recipeabi.ArtifactRef{ArtifactID: metadata.ArtifactID, ContentHash: metadata.ContentHash,
		ObjectRef: metadata.ObjectRef, Kind: string(metadata.Kind)}, nil
}

func artifactKind(value string) (model.ArtifactKind, error) {
	switch value {
	case "page":
		return model.ArtifactPage, nil
	case "response":
		return model.ArtifactResponse, nil
	case "failure":
		return model.ArtifactFailure, nil
	case "listing_delta":
		return model.ArtifactListingDelta, nil
	case "trace":
		return model.ArtifactTrace, nil
	case "validation":
		return model.ArtifactValidation, nil
	case "derived":
		return model.ArtifactDerived, nil
	default:
		return "", fmt.Errorf("unsupported executor artifact kind %q", value)
	}
}
