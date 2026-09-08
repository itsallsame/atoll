package recruitingexecutor

import (
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/url"
	"strings"

	"github.com/wanpengxie/atoll/drivers/tools/recruiting/model"
	"github.com/wanpengxie/atoll/drivers/tools/recruitingexecutor/recipeabi"
	"github.com/wanpengxie/atoll/protocol/resource"
	"github.com/wanpengxie/atoll/runtime/accessdoor"
)

const maxRecipeBytes = 256 << 10

// resourceRecipeReader is the public Atoll Resource face narrowed to the one
// operation the recruiting executor needs. Recipe bodies remain application
// data; the Atoll access plane only authorizes and returns opaque bytes.
type resourceRecipeReader interface {
	Read(resource.ResourceID) (accessdoor.Outcome, error)
}

// resourceArtifactCreator is the public Atoll file Resource face narrowed to
// creation. Large website evidence is streamed through FileAccess and never
// placed in an actor message or actor state.
type resourceArtifactCreator interface {
	CreateFile(resource.ResourceID, bool) (accessdoor.FileAccess, accessdoor.Outcome, error)
}

type recipeExpectation struct {
	ContentHash string
	Kind        recipeabi.Kind
	Capability  string
	Transport   recipeabi.Transport
}

func resolveRecipe(resources resourceRecipeReader, contentRef string, expected recipeExpectation) (recipeabi.Spec, error) {
	if resources == nil {
		return recipeabi.Spec{}, errors.New("recipe resource reader is required")
	}
	if err := validateRecipeReference(contentRef); err != nil {
		return recipeabi.Spec{}, err
	}
	outcome, err := resources.Read(resource.ResourceID(contentRef))
	if err != nil {
		return recipeabi.Spec{}, fmt.Errorf("read recipe resource: %w", err)
	}
	if !outcome.Accepted() {
		return recipeabi.Spec{}, fmt.Errorf("read recipe resource rejected: %s", outcome.RejectReason)
	}
	if !outcome.Found {
		return recipeabi.Spec{}, errors.New("recipe resource has no content")
	}
	if len(outcome.Value) == 0 || len(outcome.Value) > maxRecipeBytes {
		return recipeabi.Spec{}, fmt.Errorf("recipe resource size must be in [1,%d] bytes", maxRecipeBytes)
	}

	var spec recipeabi.Spec
	decoder := json.NewDecoder(bytes.NewReader(outcome.Value))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(&spec); err != nil {
		return recipeabi.Spec{}, fmt.Errorf("decode recipe resource: %w", err)
	}
	var trailing any
	if err := decoder.Decode(&trailing); !errors.Is(err, io.EOF) {
		if err == nil {
			return recipeabi.Spec{}, errors.New("decode recipe resource: multiple JSON values")
		}
		return recipeabi.Spec{}, fmt.Errorf("decode recipe resource: %w", err)
	}
	if err := spec.Validate(); err != nil {
		return recipeabi.Spec{}, fmt.Errorf("validate recipe resource: %w", err)
	}
	actualHash, err := spec.ContentHash()
	if err != nil {
		return recipeabi.Spec{}, fmt.Errorf("hash recipe resource: %w", err)
	}
	if !validSHA256(expected.ContentHash) || actualHash != expected.ContentHash {
		return recipeabi.Spec{}, fmt.Errorf("recipe content hash mismatch: got %s", actualHash)
	}
	if spec.Kind != expected.Kind || spec.RequiredCapability != strings.TrimSpace(expected.Capability) || spec.Transport != expected.Transport {
		return recipeabi.Spec{}, errors.New("recipe execution contract does not match the immutable offer")
	}
	return spec, nil
}

func validateRecipeReference(value string) error {
	if strings.TrimSpace(value) != value || len(value) == 0 || len(value) > 1024 {
		return errors.New("recipe content reference is invalid")
	}
	reference, err := url.Parse(value)
	if err != nil || (reference.Scheme != "recipe" && reference.Scheme != "artifact") || reference.Host == "" ||
		reference.User != nil || reference.RawQuery != "" || reference.Fragment != "" {
		return errors.New("recipe content reference must be an opaque recipe:// or artifact:// resource id")
	}
	return nil
}

type artifactWrite struct {
	Address     resource.ResourceID
	ArtifactID  string
	Kind        model.ArtifactKind
	WorkID      string
	AttemptID   string
	AccessScope string
	Retention   string
	Redacted    bool
	Content     io.Reader
	MaxBytes    int64
}

func storeArtifact(resources resourceArtifactCreator, input artifactWrite) (model.ArtifactMetadata, error) {
	if resources == nil || input.Address == "" || strings.TrimSpace(input.ArtifactID) == "" || input.Content == nil || input.MaxBytes < 1 {
		return model.ArtifactMetadata{}, errors.New("artifact resource, address, identity, content, and positive byte limit are required")
	}
	metadata, err := model.NewArtifactMetadata(input.ArtifactID, input.Kind, "sha256:"+strings.Repeat("0", sha256.Size*2), input.Address.String(),
		input.WorkID, input.AttemptID, input.AccessScope, input.Retention, input.Redacted)
	if err != nil {
		return model.ArtifactMetadata{}, fmt.Errorf("validate artifact metadata: %w", err)
	}
	file, outcome, err := resources.CreateFile(input.Address, true)
	if err != nil {
		return model.ArtifactMetadata{}, fmt.Errorf("create artifact resource: %w", err)
	}
	if !outcome.Accepted() {
		return model.ArtifactMetadata{}, fmt.Errorf("create artifact resource rejected: %s", outcome.RejectReason)
	}
	writer, ok := file.Writer()
	if !ok {
		return model.ArtifactMetadata{}, errors.New("artifact resource has no write capability")
	}

	hash := sha256.New()
	written, copyErr := io.Copy(io.MultiWriter(writer, hash), io.LimitReader(input.Content, input.MaxBytes+1))
	if copyErr != nil {
		_ = writer.Abort()
		return model.ArtifactMetadata{}, fmt.Errorf("write artifact resource: %w", copyErr)
	}
	if written > input.MaxBytes {
		_ = writer.Abort()
		return model.ArtifactMetadata{}, fmt.Errorf("artifact exceeds %d byte limit", input.MaxBytes)
	}
	if err := writer.Commit(); err != nil {
		return model.ArtifactMetadata{}, fmt.Errorf("commit artifact resource: %w", err)
	}
	metadata.ContentHash = "sha256:" + hex.EncodeToString(hash.Sum(nil))
	return metadata, nil
}

func validSHA256(value string) bool {
	if !strings.HasPrefix(value, "sha256:") || len(value) != len("sha256:")+sha256.Size*2 {
		return false
	}
	_, err := hex.DecodeString(strings.TrimPrefix(value, "sha256:"))
	return err == nil
}
