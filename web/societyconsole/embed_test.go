package societyconsole

import (
	"io/fs"
	"strings"
	"testing"
)

func TestAssetsContainCompleteConsole(t *testing.T) {
	assets := Assets()
	for _, name := range []string{"index.html", "styles.css", "app.js"} {
		info, err := fs.Stat(assets, name)
		if err != nil || info.Size() == 0 {
			t.Fatalf("asset %s info=%v err=%v", name, info, err)
		}
	}
	index, err := fs.ReadFile(assets, "index.html")
	if err != nil {
		t.Fatal(err)
	}
	page := string(index)
	for _, required := range []string{"社会总产出", "居民余额分布", "今日财政流", "冲击恢复"} {
		if !strings.Contains(page, required) {
			t.Fatalf("console omitted %q", required)
		}
	}
	if strings.Contains(page, "login-form") {
		t.Fatal("console regressed to a visible login form")
	}
}
