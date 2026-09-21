package guard

// Would this file make the router DO something, rather than hold it?
//
// Two names do. RouterOS runs a file whose name ends `.auto.rsc` as it lands
// (and loads `.auto.npk` the same way), and installs any `.npk` at the next
// reboot. Creating either through the Files page, or having the router fetch
// one, would be running code or installing a package by the back door: the
// first is the Scripts page's codeGate, the second the Packages page's job.
// Any `.auto.` is refused, not only the two documented suffixes, because a rule
// written against the suffix list is one RouterOS release from being short.
//
// A path climbing out with `..`, or starting at `/`, is refused too: the name
// is where the file goes, and MikroDash writes under the router's own root.
//
// REFUSE, NEVER WARN: none of these is a file anybody needs to make from here.
import "strings"

// CheckFileName answers for a file about to be created or fetched.
func CheckFileName(action, name string) Verdict {
	if action != "create" {
		return Verdict{Level: "none"}
	}
	low := strings.ToLower(strings.TrimSpace(name))
	refuse := func(code string) Verdict {
		return Verdict{Level: "refuse", Code: code, Detail: map[string]any{"value": name}}
	}
	switch {
	case strings.Contains(low, ".auto."):
		return refuse("file-runs")
	case strings.HasSuffix(low, ".npk"):
		return refuse("file-installs")
	case strings.HasPrefix(low, "/"):
		return refuse("file-path")
	}
	for _, seg := range strings.Split(low, "/") {
		if seg == ".." {
			return refuse("file-path")
		}
	}
	return Verdict{Level: "none"}
}
