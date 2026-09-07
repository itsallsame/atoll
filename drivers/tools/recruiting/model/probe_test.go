package model

import "testing"

func TestProbeWorkLifecycleAndFencing(t *testing.T) {
	w, err := NewProbeWork("w1", "a1", "c1", "executor-1", "fixture")
	if err != nil {
		t.Fatal(err)
	}
	w, err = w.MarkOffered()
	if err != nil || w.Status != ProbeOffered || w.Version != 2 {
		t.Fatalf("offer = %+v, %v", w, err)
	}
	if _, err := w.AcceptResult("old", 2, "ok"); err == nil {
		t.Fatal("wrong attempt was accepted")
	}
	if _, err := w.AcceptResult("a1", 1, "ok"); err == nil {
		t.Fatal("stale version was accepted")
	}
	w, err = w.AcceptResult("a1", 2, "ok")
	if err != nil || w.Status != ProbeCompleted || w.Version != 3 {
		t.Fatalf("accept = %+v, %v", w, err)
	}
	dup, err := w.AcceptResult("a1", 999, "ok")
	if err != nil || dup != w {
		t.Fatalf("equal duplicate did not converge: %+v, %v", dup, err)
	}
	if _, err := w.AcceptResult("a1", 3, "different"); err == nil {
		t.Fatal("conflicting terminal result was accepted")
	}
}

func TestNewProbeWorkRejectsMissingIdentity(t *testing.T) {
	if _, err := NewProbeWork("", "a1", "c1", "executor-1", ""); err == nil {
		t.Fatal("missing work identity was accepted")
	}
}
