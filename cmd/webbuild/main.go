// Command webbuild composes the page and bundles the client.
//
// ── WHY THIS IS GO AND NOT `web/build.mjs` ──────────────────────────────────
//
// It was 142 lines of Node driving esbuild — and esbuild is itself a Go program.
// Node was orchestrating a Go binary. Moving the orchestration into Go removes
// the last thing the BUILD needed a JavaScript runtime for.
//
// `tsc --noEmit` stays in `web/package.json` and still needs Node. That is
// irreducible: typechecking TypeScript requires the TypeScript compiler, which
// exists only as TypeScript. The difference is that a typecheck is a CHECK — it
// emits nothing, so a contributor without Node can still produce a correct
// build, which was not true before.
//
// The esbuild VERSION matches the one `web/package-lock.json` holds, so the web
// tests bundle exactly as this build does; internal/verify's
// TestBothEsbuildsAreTheSameVersion holds the two together.
//
//	go run ./cmd/webbuild            # build once
//	go run ./cmd/webbuild -watch     # rebuild on change
package main

import (
	"encoding/json"
	"flag"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"

	"github.com/evanw/esbuild/pkg/api"

	"mikrodash/internal/i18n"
	"mikrodash/internal/pages"
)

// PAGES is every page with a renderer behind it, read from the one list.
//
// It was a literal here until 2026-09-01, which made it one of five places the
// same 26 keys were written down. `internal/pages` is the source now: a page
// missing there has no markup composed, no URL registered and no TypeScript
// entry, all from the same edit.
var PAGES = pages.Keys()

// head mirrors the live app's <head>. The external stylesheets and Chart.js are
// VENDORED in `web/public/`, served by the Go binary itself.
//
// ── THIS SAID THE OPPOSITE UNTIL 2026-08-27, AND IT WAS RIGHT THEN ──────────
//
// It read: "the external stylesheets are NOT copied: the Node app still serves
// them and the Go server proxies them, so both implementations share one copy
// rather than drifting apart." Correct while Node was running, and fatal the
// moment it stops — the SPA references EIGHT assets nobody would then serve, so
// it rendered unstyled with every chart dead. Found by running standalone and
// watching /vendor/tabler.min.css answer 502.
//
// The drift the old note guarded against is real but ended at cutover: there is
// no second implementation to drift from. Licences for everything vendored are
// in THIRD_PARTY_NOTICES.md, which had to exist before any of it was committed.
const head = `<!doctype html>
<html lang="en" data-bs-theme="dark">
<head>
  <meta charset="utf-8"/>
  <meta name="viewport" content="width=device-width,initial-scale=1"/>
  <script src="/preflight.js"></script>
  <title>MikroDash</title>
  <link rel="stylesheet" href="/vendor/tabler.min.css"/>
  <link rel="stylesheet" href="/vendor/fonts/fonts.css"/>
  <link rel="stylesheet" href="/css/app-fonts.css"/>
  <link rel="stylesheet" href="/css/dashboard-grid.css"/>
  <link rel="stylesheet" href="/css/topology.css"/>
  <link rel="stylesheet" href="/app.css"/>
  <link rel="icon" type="image/png" href="/logo.png"/>
</head>
<body>
`

// tail closes the document.
//
// The Chart.js tag is a CLASSIC script, not a module: it has to define
// window.Chart BEFORE the deferred module below runs, and the Routing page reads
// it as a global.
const tail = `
<!-- Classic script, not a module: it has to define window.Chart BEFORE the
     deferred module below runs, and the Routing page reads it as a global. -->
<script src="/vendor/chart.umd.min.js"></script>
<script type="module" src="/app.js"></script>
</body>
</html>
`

// composeHTML assembles index.html.
//
// index.html is ASSEMBLED rather than authored: the shell and each page body
// are authored in web/src/ui/, one page-<key>.html per page, and composed here
// so there is one copy of each.
func composeHTML(ui string) (string, error) {
	shellB, err := os.ReadFile(filepath.Join(ui, "shell.html"))
	if err != nil {
		return "", fmt.Errorf("reading the shell: %w", err)
	}
	shell := string(shellB)
	if !strings.Contains(shell, "<!--PAGES-->") {
		return "", fmt.Errorf("the shell has no <!--PAGES--> marker")
	}

	bodies := make([]string, 0, len(PAGES))
	for _, p := range PAGES {
		f := filepath.Join(ui, "page-"+p+".html")
		b, err := os.ReadFile(f)
		if err != nil {
			return "", fmt.Errorf("no markup for page %q: web/src/ui/page-%s.html is missing", p, p)
		}
		bodies = append(bodies, string(b))
	}

	return head + strings.Replace(shell, "<!--PAGES-->", strings.Join(bodies, "\n"), 1) + tail, nil
}

// ── THREE ENTRY POINTS, THREE DOCUMENTS ────────────────────────────────────
//
//	app.js        the dashboard, a module, deferred
//	login.js      the login page, served to a browser with NO SESSION
//	preflight.js  the <head> script, which must run BEFORE the body parses
//
// Not one bundle: `login.html` reaches an unauthenticated browser, and shipping
// the whole dashboard to one would both leak the app's shape and fail on its
// first line — there is no socket to open and no `/api/*` that would answer.
// `preflight` cannot be in `app.js` at all, because a module is deferred and
// both of the things preflight does have to happen before the first paint.
//
// FORMAT: `app.js` is an ESM module (index.html loads it with type="module").
// The other two are IIFEs — `login.html` and the shell load them as classic
// scripts, and preflight must be BLOCKING, which a module is not.
func appOpts(here, dist string) api.BuildOptions {
	return api.BuildOptions{
		EntryPoints: []string{filepath.Join(here, "src", "main.ts")},
		// RELATIVE TO `web/`, because that is where `npm run build` ran from and
		// esbuild writes the source path into a comment above each module. Without
		// this the bundles are correct but not byte-identical to the Node build —
		// every module comment reads `web/src/...` instead of `src/...`, which is a
		// 440-byte diff in app.js that means nothing and hides one that would.
		AbsWorkingDir: here,
		Bundle:        true,
		Format:        api.FormatESModule,
		Target:        api.ES2020,
		Outfile:       filepath.Join(dist, "app.js"),
		Sourcemap:     api.SourceMapLinked,
		Loader:        map[string]api.Loader{".html": api.LoaderText},
		LogLevel:      api.LogLevelInfo,
		Write:         true,
	}
}

// classic are the two classic scripts. Same target, NO SOURCEMAP — they are
// short, and a map for a file served to an unauthenticated browser is a file
// served to an unauthenticated browser.
//
// See `src/entry/README.md`: these are separate DOCUMENTS, not modules of the
// app, and several of the audits ask per-document questions. The directory name
// is what tells them apart, and they read this list rather than guessing.
var classic = []struct{ src, out string }{
	{"entry/login.ts", "login.js"},
	{"entry/preflight.ts", "preflight.js"},
}

func classicOpts(here, dist string) []api.BuildOptions {
	out := make([]api.BuildOptions, 0, len(classic))
	for _, c := range classic {
		out = append(out, api.BuildOptions{
			EntryPoints:   []string{filepath.Join(here, "src", filepath.FromSlash(c.src))},
			AbsWorkingDir: here, // see appOpts
			Bundle:        true,
			Format:        api.FormatIIFE,
			Target:        api.ES2020,
			Outfile:       filepath.Join(dist, c.out),
			LogLevel:      api.LogLevelInfo,
			Write:         true,
		})
	}
	return out
}

func copyFile(src, dst string) error {
	b, err := os.ReadFile(src)
	if err != nil {
		return err
	}
	return os.WriteFile(dst, b, 0o644)
}

// fatal reports esbuild's own errors and stops.
//
// A build that printed its errors and exited 0 would be a build whose failure
// nothing notices — the same defect the gates in this repo are written to avoid.
func fatal(r api.BuildResult, what string) {
	if len(r.Errors) > 0 {
		fmt.Fprintf(os.Stderr, "webbuild: %s failed with %d error(s)\n", what, len(r.Errors))
		for _, e := range r.Errors {
			fmt.Fprintf(os.Stderr, "  %s\n", e.Text)
		}
		os.Exit(1)
	}
}

func main() {
	watch := flag.Bool("watch", false, "rebuild on change")
	dir := flag.String("dir", "web", "the web directory")
	flag.Parse()

	here, err := filepath.Abs(*dir)
	if err != nil {
		fmt.Fprintln(os.Stderr, "webbuild:", err)
		os.Exit(1)
	}
	ui := filepath.Join(here, "src", "ui")
	dist := filepath.Join(here, "dist")

	if err := os.MkdirAll(dist, 0o755); err != nil {
		fmt.Fprintln(os.Stderr, "webbuild:", err)
		os.Exit(1)
	}
	html, err := composeHTML(ui)
	if err != nil {
		fmt.Fprintln(os.Stderr, "webbuild:", err)
		os.Exit(1)
	}
	if err := os.WriteFile(filepath.Join(dist, "index.html"), []byte(html), 0o644); err != nil {
		fmt.Fprintln(os.Stderr, "webbuild:", err)
		os.Exit(1)
	}
	if err := copyFile(filepath.Join(here, "public", "app.css"), filepath.Join(dist, "app.css")); err != nil {
		fmt.Fprintln(os.Stderr, "webbuild: copying app.css:", err)
		os.Exit(1)
	}
	// login.html is markup in web/src/ui like the page bodies. It ships to dist
	// so the whole served document set comes from one directory.
	if err := copyFile(filepath.Join(ui, "login.html"), filepath.Join(dist, "login.html")); err != nil {
		fmt.Fprintln(os.Stderr, "webbuild: copying login.html:", err)
		os.Exit(1)
	}
	if err := localize(here, dist, html); err != nil {
		fmt.Fprintln(os.Stderr, "webbuild: translating:", err)
		os.Exit(1)
	}

	if *watch {
		ctx, cerr := api.Context(appOpts(here, dist))
		if cerr != nil {
			fmt.Fprintln(os.Stderr, "webbuild:", cerr)
			os.Exit(1)
		}
		if werr := ctx.Watch(api.WatchOptions{}); werr != nil {
			fmt.Fprintln(os.Stderr, "webbuild:", werr)
			os.Exit(1)
		}
		fmt.Println("watching")
		select {} // until interrupted, as `esbuild.context().watch()` did
	}

	fatal(api.Build(appOpts(here, dist)), "app.js")
	for _, o := range classicOpts(here, dist) {
		fatal(api.Build(o), filepath.Base(o.Outfile))
	}
	fmt.Println("built dist/")
}

// localize writes each language's copy of the two documents (#94):
// dist/index.<lang>.html and dist/login.<lang>.html, translated from
// web/locales/<lang>.json by internal/i18n, with `lang` set and the catalog
// embedded for t() (web/src/i18n.ts). The server picks one by the md_lang
// cookie or the browser's language.
//
// ENGLISH IS UNTOUCHED UNTIL THERE IS A SECOND LANGUAGE. With no catalog this
// writes nothing and index.html is byte for byte what it was. With one, the
// English documents gain only the language list, so their selector appears.
//
// A copy left behind by a catalog since removed is deleted, so the server
// never offers a language the build no longer has.
func localize(here, dist, index string) error {
	cats, err := i18n.Locales(filepath.Join(here, "locales"))
	if err != nil {
		return err
	}
	old, _ := filepath.Glob(filepath.Join(dist, "index.*.html"))
	oldLogin, _ := filepath.Glob(filepath.Join(dist, "login.*.html"))
	for _, f := range append(old, oldLogin...) {
		if err := os.Remove(f); err != nil {
			return err
		}
	}
	if len(cats) == 0 {
		return nil
	}
	login, err := os.ReadFile(filepath.Join(dist, "login.html"))
	if err != nil {
		return err
	}

	codes := make([]string, 0, len(cats))
	for code := range cats {
		codes = append(codes, code)
	}
	sort.Strings(codes)
	type lang struct {
		Code string `json:"code"`
		Name string `json:"name"`
	}
	langs := []lang{{Code: "en", Name: "English"}}
	for _, code := range codes {
		langs = append(langs, lang{Code: code, Name: cats[code].Name(code)})
	}

	embed := func(doc, code string, strs map[string]string) (string, error) {
		b, err := json.Marshal(struct {
			Lang      string            `json:"lang"`
			Strings   map[string]string `json:"strings"`
			Languages []lang            `json:"languages"`
		}{code, strs, langs})
		if err != nil {
			return "", err
		}
		// json.Marshal escapes < > & already, so nothing in a catalog can end
		// the script element early.
		tag := `<script type="application/json" id="i18n-catalog">` + string(b) + "</script>\n"
		if !strings.Contains(doc, "</head>") {
			return "", fmt.Errorf("a document has no </head> to carry the catalog")
		}
		doc = strings.Replace(doc, "</head>", tag+"</head>", 1)
		return strings.Replace(doc, `<html lang="en"`, `<html lang="`+code+`"`, 1), nil
	}

	for name, doc := range map[string]string{"index": index, "login": string(login)} {
		en, err := embed(doc, "en", map[string]string{})
		if err != nil {
			return err
		}
		if err := os.WriteFile(filepath.Join(dist, name+".html"), []byte(en), 0o644); err != nil {
			return err
		}
		for _, code := range codes {
			cat := cats[code]
			out, err := embed(i18n.Translate(doc, cat), code, cat.Strings())
			if err != nil {
				return err
			}
			if err := os.WriteFile(filepath.Join(dist, name+"."+code+".html"), []byte(out), 0o644); err != nil {
				return err
			}
		}
	}
	return nil
}
