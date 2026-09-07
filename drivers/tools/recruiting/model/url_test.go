package model

import "testing"

func TestCanonicalHTTPURLNormalizesIdentityWithoutNetwork(t *testing.T) {
	got, err := CanonicalHTTPURL(" HTTPS://Jobs.Example.COM:443/openings/?utm_source=x&team=z&team=a#top ")
	if err != nil {
		t.Fatal(err)
	}
	if got != "https://jobs.example.com/openings?team=a&team=z" {
		t.Fatalf("canonical URL=%q", got)
	}
	key1, _ := CanonicalSourceKey("https://jobs.example.com/openings/?team=z&utm_medium=x", " Engineering ")
	key2, _ := CanonicalSourceKey("HTTPS://JOBS.EXAMPLE.COM:443/openings?team=z", "engineering")
	if key1 != key2 {
		t.Fatalf("equivalent source keys differ:\n%s\n%s", key1, key2)
	}
}

func TestCanonicalHTTPURLRejectsUnsafeIdentityForms(t *testing.T) {
	for _, raw := range []string{"ftp://example.com/jobs", "https://user:secret@example.com/jobs", "/relative"} {
		if _, err := CanonicalHTTPURL(raw); err == nil {
			t.Fatalf("unsafe URL accepted: %q", raw)
		}
	}
}
