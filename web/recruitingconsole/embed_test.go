package recruitingconsole

import (
	"io/fs"
	"strings"
	"testing"
)

func TestAssetsContainConsoleEntryPoint(t *testing.T) {
	for _, name := range []string{"index.html", "styles.css", "app.js"} {
		if _, err := fs.Stat(Assets(), name); err != nil {
			t.Fatalf("missing embedded console asset %s: %v", name, err)
		}
	}
}

func TestConsoleOwnsAuthenticationAndUsesTheLoginHomeChannel(t *testing.T) {
	index, err := fs.ReadFile(Assets(), "index.html")
	if err != nil {
		t.Fatal(err)
	}
	page := string(index)
	if !strings.Contains(page, `id="login-form"`) {
		t.Fatal("console must provide its own integrated login")
	}
	if strings.Contains(page, "返回 Atoll 工作台") || strings.Contains(page, "登录 Atoll") {
		t.Fatal("console must not present Atoll as a separate user-facing product")
	}

	app, err := fs.ReadFile(Assets(), "app.js")
	if err != nil {
		t.Fatal(err)
	}
	script := string(app)
	if !strings.Contains(script, "identity.home_channel_id") || !strings.Contains(script, "channel_id: state.channel") {
		t.Fatal("console must bind requests to the authenticated account's home channel")
	}
	if strings.Contains(script, "const CHANNEL_ID = 'c0'") {
		t.Fatal("console must not hard-code the root channel")
	}
}
