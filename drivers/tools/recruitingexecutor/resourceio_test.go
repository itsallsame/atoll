package recruitingexecutor

import (
	"bytes"
	"crypto/sha256"
	"encoding/json"
	"fmt"
	"testing"

	"github.com/wanpengxie/atoll/drivers/tools/recruiting/model"
	"github.com/wanpengxie/atoll/drivers/tools/recruitingexecutor/recipeabi"
	"github.com/wanpengxie/atoll/protocol/access"
	"github.com/wanpengxie/atoll/protocol/resource"
	"github.com/wanpengxie/atoll/runtime/accessdoor"
)

type recipeReaderStub struct{ outcome accessdoor.Outcome }

func (s recipeReaderStub) Read(resource.ResourceID) (accessdoor.Outcome, error) {
	return s.outcome, nil
}

func validResourceRecipe(t *testing.T) (recipeabi.Spec, []byte, string) {
	t.Helper()
	spec := recipeabi.Spec{
		ABIVersion: recipeabi.Version, Kind: recipeabi.KindDetail, RequiredCapability: "http.public", Transport: recipeabi.TransportHTTPJSON,
		Request:    recipeabi.ReadRequest{Method: "GET", TimeoutMS: 1000, MaxResponseBytes: 1024, MaxRedirects: 1, UserAgent: "atoll-test"},
		Extraction: recipeabi.Extraction{Fields: map[string]string{"title": "/title"}},
	}
	raw, err := jsonMarshal(spec)
	if err != nil {
		t.Fatal(err)
	}
	hash, err := spec.ContentHash()
	if err != nil {
		t.Fatal(err)
	}
	return spec, raw, hash
}

func TestResolveRecipeVerifiesImmutableExecutionContract(t *testing.T) {
	spec, raw, hash := validResourceRecipe(t)
	reader := recipeReaderStub{outcome: accessdoor.Outcome{Found: true, Value: raw}}
	got, err := resolveRecipe(reader, "recipe://detail/example/1", recipeExpectation{
		ContentHash: hash, Kind: recipeabi.KindDetail, Capability: "http.public", Transport: recipeabi.TransportHTTPJSON,
	})
	if err != nil || got.Kind != spec.Kind {
		t.Fatalf("resolve recipe: got=%+v err=%v", got, err)
	}
	if _, err := resolveRecipe(reader, "recipe://detail/example/1", recipeExpectation{
		ContentHash: hash, Kind: recipeabi.KindListing, Capability: "http.public", Transport: recipeabi.TransportHTTPJSON,
	}); err == nil {
		t.Fatal("expected offer/recipe contract mismatch")
	}
	if _, err := resolveRecipe(reader, "https://example.test/recipe", recipeExpectation{ContentHash: hash}); err == nil {
		t.Fatal("expected external recipe URL rejection")
	}
}

func TestResolveRecipeRejectsHashMismatchAndUnknownJSON(t *testing.T) {
	_, raw, hash := validResourceRecipe(t)
	reader := recipeReaderStub{outcome: accessdoor.Outcome{Found: true, Value: raw}}
	wrongHash := hash[:len(hash)-1] + "0"
	if wrongHash == hash {
		wrongHash = hash[:len(hash)-1] + "1"
	}
	if _, err := resolveRecipe(reader, "artifact://recipes/detail", recipeExpectation{
		ContentHash: wrongHash, Kind: recipeabi.KindDetail, Capability: "http.public", Transport: recipeabi.TransportHTTPJSON,
	}); err == nil {
		t.Fatal("expected content hash mismatch")
	}
	unknown := append(bytes.TrimSuffix(raw, []byte("}")), []byte(`,"secret":"must-not-pass"}`)...)
	reader.outcome.Value = unknown
	if _, err := resolveRecipe(reader, "recipe://detail/example/1", recipeExpectation{
		ContentHash: hash, Kind: recipeabi.KindDetail, Capability: "http.public", Transport: recipeabi.TransportHTTPJSON,
	}); err == nil {
		t.Fatal("expected unknown recipe field rejection")
	}
}

type artifactCreatorStub struct {
	writer   *writeHandleStub
	outcome  accessdoor.Outcome
	created  resource.ResourceID
	withBody bool
}

func (s *artifactCreatorStub) CreateFile(id resource.ResourceID, withContent bool) (accessdoor.FileAccess, accessdoor.Outcome, error) {
	s.created, s.withBody = id, withContent
	return accessdoor.FileAccess{Local: &accessdoor.LocalFile{Write: s.writer}}, s.outcome, nil
}

type writeHandleStub struct {
	bytes.Buffer
	committed bool
	aborted   bool
}

func (w *writeHandleStub) Commit() error { w.committed = true; return nil }
func (w *writeHandleStub) Abort() error  { w.aborted = true; return nil }

func TestStoreArtifactStreamsCommitsAndReturnsVerifiedMetadata(t *testing.T) {
	writer := &writeHandleStub{}
	creator := &artifactCreatorStub{writer: writer}
	content := []byte("website response")
	metadata, err := storeArtifact(creator, artifactWrite{
		Address: "daemon://worker/recruiting/artifacts/a1", ArtifactID: "a1", Kind: model.ArtifactResponse,
		WorkID: "work-1", AttemptID: "attempt-1", AccessScope: "operators", Retention: "30d", Redacted: true,
		Content: bytes.NewReader(content), MaxBytes: int64(len(content)),
	})
	if err != nil {
		t.Fatal(err)
	}
	if !writer.committed || writer.aborted || !creator.withBody || !bytes.Equal(writer.Bytes(), content) {
		t.Fatalf("unexpected resource write state: committed=%v aborted=%v content=%q", writer.committed, writer.aborted, writer.Bytes())
	}
	sum := sha256.Sum256(content)
	if metadata.ObjectRef != creator.created.String() || metadata.ContentHash != fmt.Sprintf("sha256:%x", sum) {
		t.Fatalf("unexpected artifact metadata: %+v", metadata)
	}
}

func TestStoreArtifactAbortsOversizedEvidence(t *testing.T) {
	writer := &writeHandleStub{}
	creator := &artifactCreatorStub{writer: writer}
	_, err := storeArtifact(creator, artifactWrite{
		Address: "daemon://worker/recruiting/artifacts/a2", ArtifactID: "a2", Kind: model.ArtifactPage,
		WorkID: "work-2", AttemptID: "attempt-2", AccessScope: "operators", Retention: "30d",
		Content: bytes.NewBufferString("too large"), MaxBytes: 3,
	})
	if err == nil || !writer.aborted || writer.committed {
		t.Fatalf("expected aborted oversized write, err=%v committed=%v aborted=%v", err, writer.committed, writer.aborted)
	}
}

func TestStoreArtifactRejectsDeniedResource(t *testing.T) {
	creator := &artifactCreatorStub{writer: &writeHandleStub{}, outcome: accessdoor.Outcome{RejectReason: access.AccessDenied}}
	_, err := storeArtifact(creator, artifactWrite{Address: "denied", ArtifactID: "a", Kind: model.ArtifactPage,
		WorkID: "work", AccessScope: "operators", Retention: "30d", Content: bytes.NewReader(nil), MaxBytes: 1})
	if err == nil {
		t.Fatal("expected denied resource error")
	}
}

func TestStoreArtifactValidatesMetadataBeforeCreatingResource(t *testing.T) {
	creator := &artifactCreatorStub{writer: &writeHandleStub{}}
	_, err := storeArtifact(creator, artifactWrite{Address: "artifact", ArtifactID: "a", Kind: model.ArtifactPage,
		Content: bytes.NewReader(nil), MaxBytes: 1})
	if err == nil || creator.created != "" {
		t.Fatalf("expected pre-create validation, err=%v created=%q", err, creator.created)
	}
}

func jsonMarshal(value any) ([]byte, error) {
	return json.Marshal(value)
}
