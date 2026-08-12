package web

import (
	"bufio"
	"regexp"
	"testing"
)

// TestTemplatesParse catches template breakage at test time instead of as a
// runtime panic on first page load. loadTemplatesFrom uses template.Must, so a
// malformed action anywhere in any page template panics here.
func TestTemplatesParse(t *testing.T) {
	defer func() {
		if r := recover(); r != nil {
			t.Fatalf("template parse panic: %v", r)
		}
	}()
	if got := loadTemplatesFrom(FS); got == nil {
		t.Fatal("loadTemplatesFrom returned nil")
	}
}

// mangledAction flags template actions whose double braces were split apart —
// "{ { if .X} }" instead of "{{if .X}}". Go's parser treats split braces as
// literal text, so this never fails parsing; it silently leaks the directive
// into the rendered page (inside a <script> it's a JS syntax error that kills
// every script on the page). HTML auto-formatters do exactly this — it has
// happened; this test is why it won't ship again.
var mangledAction = regexp.MustCompile(
	`\{\s+\{[^{}]*(?:\bif\b|\belse\b|\bend\b|\brange\b|\bwith\b|\btemplate\b|\bdefine\b|\.[A-Z])` +
		`|(?:\bif\b|\belse\b|\bend\b|\brange\b|\bwith\b|\.[A-Z]\w*)[^{}]*\}\s+\}`)

func TestTemplatesNoMangledActions(t *testing.T) {
	entries, err := FS.ReadDir("templates")
	if err != nil {
		t.Fatal(err)
	}
	for _, e := range entries {
		f, err := FS.Open("templates/" + e.Name())
		if err != nil {
			t.Fatal(err)
		}
		sc := bufio.NewScanner(f)
		sc.Buffer(make([]byte, 1024*1024), 1024*1024)
		for n := 1; sc.Scan(); n++ {
			if mangledAction.MatchString(sc.Text()) {
				t.Errorf("%s:%d: template action with split braces (formatter damage?): %s",
					e.Name(), n, sc.Text())
			}
		}
		_ = f.Close() // read-only embedded file; Close cannot fail meaningfully
	}
}
