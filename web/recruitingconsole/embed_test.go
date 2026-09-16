package recruitingconsole

import (
	"io/fs"
	"testing"
)

func TestAssetsContainConsoleEntryPoint(t *testing.T) {
	for _, name := range []string{"index.html", "styles.css", "app.js"} {
		if _, err := fs.Stat(Assets(), name); err != nil {
			t.Fatalf("missing embedded console asset %s: %v", name, err)
		}
	}
}
