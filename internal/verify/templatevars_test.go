package verify

import (
	"path/filepath"
	"regexp"
	"sort"
	"strings"
	"testing"
)

// TestTemplateVariablesAreOfferedAndFilled holds the Settings page's Message
// Templates list and the variables alerts actually carry to each other, in both
// directions.
//
// For years the page offered {{comment}}, {{ifaceName}} and nine more that no
// alert filled, so they always rendered empty ("Commented: " and nothing
// after it), and nothing failed. An offered variable nothing fills is that bug
// again; a filled one the page does not offer is a feature nobody can find.
//
// Filled means: set by alertdispatch.Build (the five every alert has), named in
// a tplVars/pingVars call in internal/alert, or a key powerVars or
// dispatchPower writes in internal/server/power_notify.go.
func TestTemplateVariablesAreOfferedAndFilled(t *testing.T) {
	root := repoRoot(t)
	html := mustRead(t, filepath.Join(root, "web", "src", "ui", "page-settings.html"))
	offered := map[string]bool{}
	for _, m := range regexp.MustCompile(`<code>\{\{(\w+)\}\}</code>`).FindAllStringSubmatch(
		sliceBetween(t, html, `<aside class="msg-vars">`, `</aside>`), -1) {
		offered[m[1]] = true
	}

	filled := map[string]bool{}
	build := sliceBetween(t, mustRead(t, filepath.Join(root, "internal", "alertdispatch", "dispatch.go")),
		"func Build(", "\n}\n")
	for _, m := range regexp.MustCompile(`"(\w+)":`).FindAllStringSubmatch(build, -1) {
		filled[m[1]] = true
	}
	filled["alertType"] = strings.Contains(build, `vars["alertType"]`)
	rules := mustRead(t, filepath.Join(root, "internal", "alert", "eval.go")) +
		mustRead(t, filepath.Join(root, "internal", "alert", "routerstatus.go"))
	for _, call := range regexp.MustCompile(`(?:tplVars|pingVars)\(([^)]*)\)`).FindAllStringSubmatch(rules, -1) {
		args := strings.Split(call[1], ",")
		if strings.HasPrefix(call[0], "pingVars") {
			continue // its keys are literal inside pingVars itself, read below
		}
		for i := 0; i < len(args); i += 2 {
			if k := strings.Trim(strings.TrimSpace(args[i]), `"`); regexp.MustCompile(`^\w+$`).MatchString(k) {
				filled[k] = true
			}
		}
	}
	for _, k := range regexp.MustCompile(`v\["(\w+)"\]`).FindAllStringSubmatch(sliceBetween(t, rules, "func pingVars(", "\n}\n"), -1) {
		filled[k[1]] = true
	}
	notify := mustRead(t, filepath.Join(root, "internal", "server", "power_notify.go"))
	for _, m := range regexp.MustCompile(`(?:num\(|out\[|f\.Vars\[)"(\w+)"`).FindAllStringSubmatch(notify, -1) {
		filled[m[1]] = true
	}
	for _, m := range regexp.MustCompile(`out\["(\w+)"\], out\["(\w+)"\]|f\.Vars\["(\w+)"\], f\.Vars\["(\w+)"\]`).FindAllStringSubmatch(notify, -1) {
		for _, k := range m[1:] {
			if k != "" {
				filled[k] = true
			}
		}
	}
	if len(offered) < 10 || len(filled) < 10 {
		t.Fatalf("read %d offered and %d filled variables: an anchor moved and this check reads nothing", len(offered), len(filled))
	}

	var notFilled, notOffered []string
	for k := range offered {
		if !filled[k] {
			notFilled = append(notFilled, k)
		}
	}
	for k, ok := range filled {
		if ok && !offered[k] {
			notOffered = append(notOffered, k)
		}
	}
	sort.Strings(notFilled)
	sort.Strings(notOffered)
	if len(notFilled) > 0 {
		t.Errorf("the Settings page offers variables no alert fills (they always render empty): %v", notFilled)
	}
	if len(notOffered) > 0 {
		t.Errorf("alerts fill variables the Settings page does not list: %v", notOffered)
	}
}
