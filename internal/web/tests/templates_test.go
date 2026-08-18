package web_test

import (
	"bufio"
	"regexp"
	"strings"
	"testing"

	"github.com/fitbase/fitbase/internal/web"
)

// TestTemplatesParse catches template breakage at test time instead of as a
// runtime panic on first page load. Template loading uses template.Must, so a
// malformed action anywhere in any page template panics during handler
// construction. The nil DB is fine: construction only parses templates.
func TestTemplatesParse(t *testing.T) {
	defer func() {
		if r := recover(); r != nil {
			t.Fatalf("template parse panic: %v", r)
		}
	}()
	if got := web.NewTemplateHandler(nil, false, web.FS); got == nil {
		t.Fatal("NewTemplateHandler returned nil")
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
	forEachTemplateLine(t, func(name string, n int, line string) {
		if mangledAction.MatchString(line) {
			t.Errorf("%s:%d: template action with split braces (formatter damage?): %s",
				name, n, line)
		}
	})
}

// urlHelperCall / stringLit find sortURL/filterURL actions and the string
// literals inside them, for TestTemplatesNoPaddedURLKeys.
var (
	urlHelperCall = regexp.MustCompile(`\{\{[^{}]*(?:sortURL|filterURL)[^{}]*\}\}`)
	stringLit     = regexp.MustCompile(`"([^"]*)"`)
)

// TestTemplatesNoPaddedURLKeys flags sort/filter keys that grew padding —
// `sortURL .Sort .Dir " date"` instead of `"date"`. The index handler
// whitelist-validates these keys, so a padded value fails validation and the
// link silently becomes a no-op. HTML auto-formatters do exactly this when a
// quoted template literal sits inside an href (the inner quote reads as the
// end of the attribute, and the "attributes" after it get re-spaced) — it has
// happened: every homepage sort and filter link shipped broken. This test is
// why it won't ship again.
func TestTemplatesNoPaddedURLKeys(t *testing.T) {
	forEachTemplateLine(t, func(name string, n int, line string) {
		for _, call := range urlHelperCall.FindAllString(line, -1) {
			for _, lit := range stringLit.FindAllStringSubmatch(call, -1) {
				if lit[1] != strings.TrimSpace(lit[1]) {
					t.Errorf("%s:%d: padded key %q in %s (formatter damage?)",
						name, n, lit[1], call)
				}
			}
		}
	})
}

// forEachTemplateLine runs fn for every line of every embedded template.
func forEachTemplateLine(t *testing.T, fn func(name string, lineNo int, line string)) {
	t.Helper()
	entries, err := web.FS.ReadDir("templates")
	if err != nil {
		t.Fatal(err)
	}
	for _, e := range entries {
		f, err := web.FS.Open("templates/" + e.Name())
		if err != nil {
			t.Fatal(err)
		}
		sc := bufio.NewScanner(f)
		sc.Buffer(make([]byte, 1024*1024), 1024*1024)
		for n := 1; sc.Scan(); n++ {
			fn(e.Name(), n, sc.Text())
		}
		_ = f.Close() // read-only embedded file; Close cannot fail meaningfully
	}
}
