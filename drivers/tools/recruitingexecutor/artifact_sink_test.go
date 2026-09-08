package recruitingexecutor

import (
	"bytes"
	"context"
	"testing"

	"github.com/wanpengxie/atoll/drivers/tools/recruitingexecutor/httpdriver"
	"github.com/wanpengxie/atoll/protocol/access"
	"github.com/wanpengxie/atoll/protocol/resource"
	"github.com/wanpengxie/atoll/runtime/accessdoor"
)

func testArtifactSink(t *testing.T, resources resourceArtifactStore) *atollArtifactSink {
	t.Helper()
	sink, err := newAtollArtifactSink(resources, artifactSinkConfig{DeviceName: "worker-a", ChannelName: "recruiting",
		Directory: "recruiting-artifacts", WorkID: "work-1", AttemptID: "attempt-1", AccessScope: "operators",
		Retention: "30d", Redacted: false, MaxBytes: 1024})
	if err != nil {
		t.Fatal(err)
	}
	return sink
}

func TestAtollArtifactSinkCreatesStableFileResource(t *testing.T) {
	writer := &writeHandleStub{}
	resources := &artifactCreatorStub{writer: writer}
	sink := testArtifactSink(t, resources)
	if got, want := resources.directory.String(), "daemon://worker-a/recruiting/recruiting-artifacts--attempt-1"; got != want {
		t.Fatalf("attempt evidence directory=%q want %q", got, want)
	}
	body := []byte(`{"jobs":[{"id":"1"}]}`)
	write := httpdriver.ArtifactWrite{Kind: "page", AttemptID: "attempt-1", PageSequence: 1,
		URL: "https://jobs.example.com/openings", Body: body}
	ref, err := sink.Put(context.Background(), write)
	if err != nil {
		t.Fatal(err)
	}
	if !writer.committed || !bytes.Equal(writer.Bytes(), body) || ref.ArtifactID == "" || ref.ContentHash == "" ||
		ref.ObjectRef != resources.created.String() || len(ref.ObjectRef) < len("daemon://worker-a/recruiting/recruiting-artifacts--attempt-1/") ||
		ref.ObjectRef[:len("daemon://worker-a/recruiting/recruiting-artifacts--attempt-1/")] != "daemon://worker-a/recruiting/recruiting-artifacts--attempt-1/" {
		t.Fatalf("unexpected artifact write/ref: committed=%v body=%q ref=%+v", writer.committed, writer.Bytes(), ref)
	}

	resources.outcome = accessdoor.Outcome{RejectReason: access.AlreadyExists}
	resources.existing = append([]byte(nil), body...)
	replayed, err := sink.Put(context.Background(), write)
	if err != nil || replayed != ref {
		t.Fatalf("idempotent sink replay: got=%+v want=%+v err=%v", replayed, ref, err)
	}
	resources.existing = []byte("different")
	if _, err := sink.Put(context.Background(), write); err == nil {
		t.Fatal("expected existing resource content conflict")
	}
}

func TestAtollArtifactSinkUsesIndependentAttemptDirectories(t *testing.T) {
	first := &artifactCreatorStub{writer: &writeHandleStub{}}
	testArtifactSink(t, first)
	second := &artifactCreatorStub{writer: &writeHandleStub{}}
	if _, err := newAtollArtifactSink(second, artifactSinkConfig{DeviceName: "worker-a", ChannelName: "recruiting",
		Directory: "recruiting-artifacts", WorkID: "work-2", AttemptID: "attempt-2", AccessScope: "operators",
		Retention: "30d", MaxBytes: 1024}); err != nil {
		t.Fatal(err)
	}
	if first.directory == second.directory {
		t.Fatalf("attempts share evidence directory %q", first.directory)
	}
	if _, err := newAtollArtifactSink(second, artifactSinkConfig{DeviceName: "worker-a", ChannelName: "recruiting",
		Directory: "recruiting-artifacts", WorkID: "work-3", AttemptID: "../escape", AccessScope: "operators",
		Retention: "30d", MaxBytes: 1024}); err == nil {
		t.Fatal("expected unsafe attempt path segment rejection")
	}
}

func TestAtollArtifactSinkRejectsCrossAttemptAndHashMismatch(t *testing.T) {
	resources := &artifactCreatorStub{writer: &writeHandleStub{}}
	sink := testArtifactSink(t, resources)
	if _, err := sink.Put(context.Background(), httpdriver.ArtifactWrite{Kind: "page", AttemptID: "other", PageSequence: 1,
		Body: []byte("body")}); err == nil {
		t.Fatal("expected cross-attempt artifact rejection")
	}
	if _, err := sink.Put(context.Background(), httpdriver.ArtifactWrite{Kind: "page", AttemptID: "attempt-1", PageSequence: 1,
		ContentHash: "sha256:not-the-body", Body: []byte("body")}); err == nil {
		t.Fatal("expected declared hash mismatch")
	}
}

func TestAtollArtifactSinkRequiresDirectoryCapability(t *testing.T) {
	resources := &directoryDeniedArtifactStore{artifactCreatorStub: artifactCreatorStub{writer: &writeHandleStub{}}}
	if _, err := newAtollArtifactSink(resources, artifactSinkConfig{DeviceName: "worker-a", ChannelName: "recruiting",
		Directory: "artifacts", WorkID: "work", AttemptID: "attempt", AccessScope: "operators", Retention: "30d", MaxBytes: 10}); err == nil {
		t.Fatal("expected directory authorization failure")
	}
}

type directoryDeniedArtifactStore struct{ artifactCreatorStub }

func (s *directoryDeniedArtifactStore) CreateDirectory(resourceID resource.ResourceID) (accessdoor.Outcome, error) {
	return accessdoor.Outcome{RejectReason: access.AccessDenied}, nil
}
