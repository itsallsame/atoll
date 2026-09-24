package portal

import (
	"io/fs"
	"net/http"
	"net/http/httptest"
	"testing"
	"testing/fstest"
)

func TestOptionalWebMountServesItsOwnIndexAndAssets(t *testing.T) {
	assets := fstest.MapFS{
		"index.html": &fstest.MapFile{Data: []byte("laboratory")},
		"app.js":     &fstest.MapFile{Data: []byte("working-surface")},
	}
	portal := New(Config{ContractVersion: "5", WebMounts: map[string]fs.FS{"/staircase/": assets}})

	redirect := httptest.NewRecorder()
	portal.ServeHTTP(redirect, httptest.NewRequest(http.MethodGet, "/staircase", nil))
	if redirect.Code != http.StatusTemporaryRedirect || redirect.Header().Get("Location") != "/staircase/" {
		t.Fatalf("mount redirect status=%d location=%q", redirect.Code, redirect.Header().Get("Location"))
	}

	for path, want := range map[string]string{"/staircase/": "laboratory", "/staircase/app.js": "working-surface"} {
		response := httptest.NewRecorder()
		portal.ServeHTTP(response, httptest.NewRequest(http.MethodGet, path, nil))
		if response.Code != http.StatusOK || response.Body.String() != want {
			t.Fatalf("GET %s status=%d body=%q", path, response.Code, response.Body.String())
		}
	}
}
