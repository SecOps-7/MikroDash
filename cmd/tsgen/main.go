// tsgen generates the TypeScript types for the WebSocket payloads from the Go
// structs that produce them.
//
// ── WHY THIS EXISTS ─────────────────────────────────────────────────────────
//
// Every payload is defined TWICE: once as a Go struct with json tags, once as a
// hand-written TypeScript interface that somebody keeps in step by reading the
// Go. `web/src/pages/routing-types.ts` opens by saying so outright -- "The
// routing:update payload, as internal/collect/routing.go emits it" -- and that
// comment is the whole contract. Nothing breaks when the two drift; the page
// just renders a field that is always undefined.
//
// The Go side is already the schema: 3,194 json-tagged fields with a compiler
// enforcing them. This makes the other side a build artefact instead of a
// mirror maintained by hand.
//
// ── AND WHY IT IS A GO PROGRAM ──────────────────────────────────────────────
//
// `cmd/webbuild` and `cmd/geogen` are the precedent: build-time tools live in
// Go so the image needs no Node. It matters more than precedent here, though.
//
// All 105 of this repo's corpus generators read the deleted Node source, so
// every one of their `--check` runs now SKIPS -- `tools/verify.sh` says so
// loudly. A generator whose source is `internal/` is the FIRST one whose
// `--check` actually runs again, on any clone, with nothing mounted.
//
// ── WHAT A NAIVE GENERATOR GETS WRONG ───────────────────────────────────────
//
// Three things, all present in this tree and all silent:
//
//  1. `json:"-"` -- `Route.Flags` is not on the wire, and the hand-written
//     routing types correctly omit it. Emitting it would invent a field.
//  2. `omitempty` -- must become an OPTIONAL property, or every consumer is
//     told a key is always there when it is not.
//  3. `conn:update` and the `ws.go` replay of the same struct DO NOT HAVE THE
//     SAME KEYS. `connsLight` marshals ConnsPayload through a map and deletes
//     four heavy indexes. No reflection can see that, so it is modelled here
//     as a second interface, with the deleted keys read out of the Go source
//     rather than retyped -- see heavyKeys().
//
// Pointer fields are `T | null`, not `T | undefined`: a nil pointer marshals to
// null, which is a value that arrives, not a key that is missing.
package main

import (
	"bytes"
	"flag"
	"fmt"
	"go/ast"
	"go/parser"
	"go/token"
	"os"
	"path/filepath"
	"reflect"
	"sort"
	"strconv"
	"strings"
)

const outRel = "web/src/gen/payloads.ts"

func main() {
	var (
		src   = flag.String("src", "internal/collect", "package directory to read structs from")
		also  = flag.String("also", "internal/guard", "comma-separated packages whose types the payloads reference")
		out   = flag.String("out", outRel, "file to write")
		check = flag.Bool("check", false, "fail if the committed file is stale instead of writing it")
		drift = flag.Bool("drift", false, "report where hand-written interfaces disagree, and write nothing")
	)
	flag.Parse()

	pkg, err := loadPackage(*src)
	if err != nil {
		fatal(err)
	}
	// ── TYPES FROM OTHER PACKAGES ───────────────────────────────────────────
	//
	// `SimpleQueue.limitAt` is a `guard.Pair`, so reading internal/collect alone
	// cannot type it. They are kept in SEPARATE scopes rather than merged, which
	// is not fussiness: `Rate` exists in BOTH packages and means different
	// things. internal/collect's is an internal throughput pair with no json
	// tags; internal/guard's is on the wire AND has its own MarshalJSON. Merging
	// by name would have typed `limitAt` with the wrong struct and still
	// compiled.
	extern := map[string]*pkgTypes{}
	for _, dir := range strings.Split(*also, ",") {
		dir = strings.TrimSpace(dir)
		if dir == "" {
			continue
		}
		extra, err := loadPackage(dir)
		if err != nil {
			fatal(fmt.Errorf("loading %s: %w", dir, err))
		}
		extern[extra.name] = extra
	}
	g := &gen{types: pkg, extern: extern, emitted: map[string]bool{}}
	body, err := g.render()
	if err != nil {
		fatal(err)
	}

	if *drift {
		n, err := reportDrift(g)
		if err != nil {
			fatal(err)
		}
		if n == 0 {
			fmt.Println("tsgen: no drift between the generated payload types and the hand-written ones")
		}
		return
	}

	if *check {
		have, err := os.ReadFile(*out)
		if err != nil {
			fatal(fmt.Errorf("%s is missing; run `go run ./cmd/tsgen`", *out))
		}
		if !bytes.Equal(bytes.TrimSpace(have), bytes.TrimSpace(body)) {
			fmt.Fprintf(os.Stderr, "tsgen: %s is STALE — a payload struct changed and the types were not regenerated.\n"+
				"Run: go run ./cmd/tsgen\n", *out)
			os.Exit(1)
		}
		fmt.Printf("tsgen: %s is current (%d interfaces from %d payload structs)\n",
			*out, g.count, len(payloadRoots(pkg)))
		return
	}

	if err := os.WriteFile(*out, body, 0o644); err != nil {
		fatal(err)
	}
	fmt.Printf("tsgen: wrote %s — %d interfaces from %d payload structs\n",
		*out, g.count, len(payloadRoots(pkg)))
}

func fatal(err error) {
	fmt.Fprintln(os.Stderr, "tsgen:", err)
	os.Exit(1)
}

// ── Reading the package ─────────────────────────────────────────────────────

type pkgTypes struct {
	name    string
	structs map[string]*ast.StructType
	// marshalers are types with their own MarshalJSON. Their wire form is NOT
	// their field list, so rendering one as an interface would be confidently
	// wrong -- see the override table.
	marshalers map[string]bool
	// aliases maps a named non-struct type (e.g. `type RouteFlags uint8`) to its
	// underlying expression, so it can be resolved rather than emitted.
	aliases map[string]ast.Expr
	files   map[string]string // filename -> source, for heavyKeys()
}

func loadPackage(dir string) (*pkgTypes, error) {
	fset := token.NewFileSet()
	pkgs, err := parser.ParseDir(fset, dir, func(fi os.FileInfo) bool {
		return !strings.HasSuffix(fi.Name(), "_test.go")
	}, parser.ParseComments)
	if err != nil {
		return nil, err
	}
	p := &pkgTypes{
		name:       filepath.Base(dir),
		structs:    map[string]*ast.StructType{},
		aliases:    map[string]ast.Expr{},
		files:      map[string]string{},
		marshalers: map[string]bool{},
	}
	for _, pk := range pkgs {
		for name, f := range pk.Files {
			b, err := os.ReadFile(name)
			if err == nil {
				p.files[filepath.Base(name)] = string(b)
			}
			for _, d := range f.Decls {
				if fd, ok := d.(*ast.FuncDecl); ok {
					if fd.Name.Name == "MarshalJSON" && fd.Recv != nil && len(fd.Recv.List) == 1 {
						if rn := recvTypeName(fd.Recv.List[0].Type); rn != "" {
							p.marshalers[rn] = true
						}
					}
					continue
				}
				gd, ok := d.(*ast.GenDecl)
				if !ok || gd.Tok != token.TYPE {
					continue
				}
				for _, sp := range gd.Specs {
					ts, ok := sp.(*ast.TypeSpec)
					if !ok {
						continue
					}
					if st, ok := ts.Type.(*ast.StructType); ok {
						p.structs[ts.Name.Name] = st
					} else {
						p.aliases[ts.Name.Name] = ts.Type
					}
				}
			}
		}
	}
	if len(p.structs) == 0 {
		return nil, fmt.Errorf("no structs found in %s — the parser or the path is wrong", dir)
	}
	return p, nil
}

// mergeUsed folds another package's declarations in, refusing on a name that is
// already taken. A silent overwrite here would type a field with the wrong
// package's struct and still compile, which is precisely the class of bug this
// tool exists to remove.
func (p *pkgTypes) mergeUsed(other *pkgTypes, dir string) error {
	for name, st := range other.structs {
		if _, clash := p.structs[name]; clash {
			return fmt.Errorf("type %q is declared both in the payload package and in %s; "+
				"one of them must be renamed before the wire types can be generated unambiguously", name, dir)
		}
		p.structs[name] = st
	}
	for name, e := range other.aliases {
		if _, clash := p.aliases[name]; clash {
			continue
		}
		p.aliases[name] = e
	}
	return nil
}

func recvTypeName(e ast.Expr) string {
	switch t := e.(type) {
	case *ast.Ident:
		return t.Name
	case *ast.StarExpr:
		return recvTypeName(t.X)
	}
	return ""
}

func payloadRoots(p *pkgTypes) []string {
	var roots []string
	for name := range p.structs {
		if strings.HasSuffix(name, "Payload") {
			roots = append(roots, name)
		}
	}
	sort.Strings(roots)
	return roots
}

// ── Rendering ───────────────────────────────────────────────────────────────

type gen struct {
	types   *pkgTypes
	extern  map[string]*pkgTypes
	emitted map[string]bool
	order   []string // "Name" or "pkg.Name"
	count   int
}

// ── TYPES WHOSE WIRE FORM IS NOT THEIR FIELD LIST ───────────────────────────
//
// A type with its own MarshalJSON marshals to whatever that method writes, so
// its struct fields say nothing about the wire. There is no way to derive the
// answer -- it is arbitrary Go -- so each one is declared here, next to the
// reason, and an undeclared marshaler is a hard error rather than a guess.
//
// `guard.Rate` is `{Bps int64; Set bool}` and would generate a two-field
// interface. It actually writes a BARE NUMBER, or null when unset, because the
// original's value was `null | number` and the page draws those differently.
var marshalOverrides = map[string]string{
	"guard.Rate": "number | null",
}

// resolve returns the package an expression should be read in.
func (g *gen) pkg(qual string) *pkgTypes {
	if qual == "" {
		return g.types
	}
	return g.extern[qual]
}

// key names a type for the emit list: bare in the main package, qualified
// elsewhere, so two packages' same-named types cannot collide silently.
func key(qual, name string) string {
	if qual == "" {
		return name
	}
	return qual + "." + name
}

// fieldOut is one rendered property.
type fieldOut struct {
	name     string
	ts       string
	optional bool
	comment  string
}

func (g *gen) render() ([]byte, error) {
	roots := payloadRoots(g.types)
	var buf bytes.Buffer

	buf.WriteString(`// GENERATED by cmd/tsgen — do not edit.
//
// The WebSocket payload types, read from the Go structs that produce them
// (internal/collect). Every property, its type and its optionality come from a
// json tag, so this file cannot drift from the wire: ` + "`go run ./cmd/tsgen -check`" + `
// fails when it does, and unlike the 105 corpus generators that check runs on
// any clone, because its source is this repository rather than a deleted one.
//
// Optional (` + "`?`" + `) means the Go field carries ` + "`omitempty`" + ` and the key can be
// ABSENT. ` + "`| null`" + ` means the Go field is a pointer and the key is present
// carrying null. They are different things and the distinction is load-bearing:
// the dashboard's count setters test ` + "`!== undefined`" + `, so an absent key renders an
// em dash while an explicit null renders the string "null".

`)

	// Depth-first so a nested type is declared before the payload that uses it.
	for _, r := range roots {
		if err := g.walk("", r); err != nil {
			return nil, err
		}
	}
	// The one payload whose wire form is not its struct form.
	light, err := g.renderConnsLight()
	if err != nil {
		return nil, err
	}

	// A TypeScript interface name must be unique even though a Go type name is
	// only unique per package. Two packages contributing the same name would
	// silently redeclare it, so it is refused here with both sources named.
	seen := map[string]string{}
	for _, k := range g.order {
		bare := k
		if i := strings.IndexByte(k, '.'); i >= 0 {
			bare = k[i+1:]
		}
		if prev, clash := seen[bare]; clash {
			return nil, fmt.Errorf("interface name %q would be declared twice, from %s and %s", bare, prev, k)
		}
		seen[bare] = k
	}
	for _, k := range g.order {
		block, err := g.renderStruct(k)
		if err != nil {
			return nil, err
		}
		buf.WriteString(block)
		buf.WriteString("\n")
	}
	buf.WriteString(light)
	return buf.Bytes(), nil
}

// walk records a type and everything it references, dependencies first.
func (g *gen) walk(qual, name string) error {
	k := key(qual, name)
	if g.emitted[k] {
		return nil
	}
	if _, overridden := marshalOverrides[k]; overridden {
		return nil // renders as a scalar; nothing to declare
	}
	p := g.pkg(qual)
	if p == nil {
		return fmt.Errorf("package %q was not loaded; add it to -also", qual)
	}
	st, ok := p.structs[name]
	if !ok {
		return fmt.Errorf("type %q is referenced but not declared in %s", name, p.name)
	}
	if p.marshalers[name] {
		return fmt.Errorf("%s has its own MarshalJSON, so its wire form is not its fields; "+
			"add it to marshalOverrides with the shape it actually writes", k)
	}
	g.emitted[k] = true // set before recursing, so a cycle terminates
	for _, f := range st.Fields.List {
		if skipField(f) {
			continue
		}
		for _, dep := range g.namedDeps(qual, f.Type) {
			dp := g.pkg(dep.qual)
			if dp == nil {
				continue
			}
			if _, isStruct := dp.structs[dep.name]; isStruct {
				if err := g.walk(dep.qual, dep.name); err != nil {
					return err
				}
			}
		}
	}
	g.order = append(g.order, k)
	g.count++
	return nil
}

type ref struct{ qual, name string }

// namedDeps returns the named types an expression mentions, each carried with
// the package it must be resolved in.
func (g *gen) namedDeps(qual string, e ast.Expr) []ref {
	switch t := e.(type) {
	case *ast.Ident:
		return []ref{{qual, t.Name}}
	case *ast.StarExpr:
		return g.namedDeps(qual, t.X)
	case *ast.ArrayType:
		return g.namedDeps(qual, t.Elt)
	case *ast.MapType:
		return append(g.namedDeps(qual, t.Key), g.namedDeps(qual, t.Value)...)
	case *ast.SelectorExpr:
		return []ref{{exprName(t.X), t.Sel.Name}}
	}
	return nil
}

// skipField is true for anything that never reaches the wire: an unexported
// field, or one tagged `json:"-"`. Route.Flags is the live example of the
// second, and emitting it would invent a property no consumer can read.
func skipField(f *ast.Field) bool {
	if len(f.Names) == 0 {
		return false // embedded; handled by the caller
	}
	if !f.Names[0].IsExported() {
		return true
	}
	name, _ := jsonTag(f)
	return name == "-"
}

func jsonTag(f *ast.Field) (name string, omitempty bool) {
	if f.Tag == nil {
		return "", false
	}
	raw, err := strconv.Unquote(f.Tag.Value)
	if err != nil {
		return "", false
	}
	tag := reflect.StructTag(raw).Get("json")
	if tag == "" {
		return "", false
	}
	parts := strings.Split(tag, ",")
	for _, p := range parts[1:] {
		if p == "omitempty" {
			omitempty = true
		}
	}
	return parts[0], omitempty
}

func (g *gen) renderStruct(k string) (string, error) {
	qual, name := "", k
	if i := strings.IndexByte(k, '.'); i >= 0 {
		qual, name = k[:i], k[i+1:]
	}
	st := g.pkg(qual).structs[name]
	fields, err := g.fields(qual, st)
	if err != nil {
		return "", fmt.Errorf("%s: %w", k, err)
	}
	var b strings.Builder
	// The TypeScript name is the bare Go name; a cross-package collision is
	// caught in render() rather than papered over with a prefix.
	fmt.Fprintf(&b, "export interface %s {\n", name)
	for _, f := range fields {
		opt := ""
		if f.optional {
			opt = "?"
		}
		fmt.Fprintf(&b, "  %s%s: %s;\n", f.name, opt, f.ts)
	}
	b.WriteString("}\n")
	return b.String(), nil
}

func (g *gen) fields(qual string, st *ast.StructType) ([]fieldOut, error) {
	var out []fieldOut
	for _, f := range st.Fields.List {
		// EMBEDDED STRUCTS ARE FLATTENED, because that is what encoding/json
		// does with them: the inner fields appear at the outer level.
		if len(f.Names) == 0 {
			id, ok := f.Type.(*ast.Ident)
			if !ok {
				return nil, fmt.Errorf("embedded field of unsupported shape")
			}
			inner, ok := g.pkg(qual).structs[id.Name]
			if !ok {
				return nil, fmt.Errorf("embedded %s is not a struct in this package", id.Name)
			}
			sub, err := g.fields(qual, inner)
			if err != nil {
				return nil, err
			}
			out = append(out, sub...)
			continue
		}
		if skipField(f) {
			continue
		}
		name, omitempty := jsonTag(f)
		if name == "" {
			// NO TAG IS A REFUSAL, not a guess. encoding/json would use the Go
			// field name verbatim, which is a capitalised key no hand-written
			// TypeScript in this repo uses -- so a missing tag is much more
			// likely an oversight than an intent, and inventing the key would
			// hide it.
			return nil, fmt.Errorf("field %s has no json tag; add one (or `json:\"-\"`) rather than letting the key be guessed", f.Names[0].Name)
		}
		ts, err := g.tsType(qual, f.Type)
		if err != nil {
			return nil, fmt.Errorf("field %s: %w", name, err)
		}
		out = append(out, fieldOut{name: tsKey(name), ts: ts, optional: omitempty})
	}
	return out, nil
}

// tsKey quotes a key that is not a plain identifier, so a json tag with a dash
// or a dot still produces valid TypeScript.
func tsKey(k string) string {
	ok := k != ""
	for i, r := range k {
		valid := r == '_' || r == '$' || (r >= 'a' && r <= 'z') || (r >= 'A' && r <= 'Z') || (i > 0 && r >= '0' && r <= '9')
		if !valid {
			ok = false
			break
		}
	}
	if ok {
		return k
	}
	return strconv.Quote(k)
}

// tsType maps a Go type expression to TypeScript. It returns an ERROR for
// anything it does not recognise rather than falling back to `any`: a wrong
// type that compiles is exactly the failure this generator exists to remove,
// and `any` would reintroduce it silently under a nicer name.
func (g *gen) tsType(qual string, e ast.Expr) (string, error) {
	switch t := e.(type) {
	case *ast.Ident:
		return g.tsNamed(qual, t.Name)
	case *ast.StarExpr:
		inner, err := g.tsType(qual, t.X)
		if err != nil {
			return "", err
		}
		// A nil pointer marshals to null -- a key that is PRESENT and null,
		// which is not the same as an absent key. See the file header.
		return inner + " | null", nil
	case *ast.ArrayType:
		inner, err := g.tsType(qual, t.Elt)
		if err != nil {
			return "", err
		}
		// A nil slice marshals to null too, and several of these fields are
		// left nil on purpose -- connsLight's four indexes among them.
		return inner + "[] | null", nil
	case *ast.MapType:
		k, err := g.tsType(qual, t.Key)
		if err != nil {
			return "", err
		}
		v, err := g.tsType(qual, t.Value)
		if err != nil {
			return "", err
		}
		if k != "string" && k != "number" {
			return "", fmt.Errorf("map key %s is not usable as a TypeScript index", k)
		}
		return fmt.Sprintf("Record<%s, %s> | null", k, v), nil
	case *ast.InterfaceType:
		if t.Methods == nil || len(t.Methods.List) == 0 {
			return "unknown", nil // `any` in Go; `unknown` forces a check in TS
		}
		return "", fmt.Errorf("non-empty interface types have no TypeScript equivalent here")
	case *ast.SelectorExpr:
		return g.tsNamed(exprName(t.X), t.Sel.Name)
	}
	return "", fmt.Errorf("unsupported type expression %T", e)
}

func exprName(e ast.Expr) string {
	if id, ok := e.(*ast.Ident); ok {
		return id.Name
	}
	return "?"
}

func (g *gen) tsNamed(qual, name string) (string, error) {
	if over, ok := marshalOverrides[key(qual, name)]; ok {
		return over, nil
	}
	switch name {
	case "string":
		return "string", nil
	case "bool":
		return "boolean", nil
	case "int", "int8", "int16", "int32", "int64",
		"uint", "uint8", "uint16", "uint32", "uint64",
		"float32", "float64":
		return "number", nil
	case "any":
		return "unknown", nil
	}
	p := g.pkg(qual)
	if p == nil {
		return "", fmt.Errorf("package %q was not loaded; add it to -also", qual)
	}
	if _, ok := p.structs[name]; ok {
		if p.marshalers[name] {
			return "", fmt.Errorf("%s has its own MarshalJSON; add it to marshalOverrides", key(qual, name))
		}
		return name, nil
	}
	// A named scalar (`type RouteFlags uint8`) resolves to its underlying type.
	if under, ok := p.aliases[name]; ok {
		return g.tsType(qual, under)
	}
	return "", fmt.Errorf("unknown type %q in package %q", name, p.name)
}

// ── The one payload whose wire form is not its struct form ──────────────────
//
// `conn:update` is ConnsPayload marshalled through `connsLight`, which round-
// trips it through a map and DELETES four keys. The struct is unchanged, so
// nothing about the Go type says this happened -- a generator that only reads
// the struct would tell every consumer of `conn:update` that four heavy indexes
// are present when they are not there at all.
//
// The key names are read out of `connsHeavyKeys` rather than retyped here, so
// adding a fifth to that array is carried through instead of silently missed.
func (g *gen) renderConnsLight() (string, error) {
	keys, err := g.heavyKeys()
	if err != nil {
		return "", err
	}
	st, ok := g.types.structs["ConnsPayload"]
	if !ok {
		return "", fmt.Errorf("ConnsPayload not found")
	}
	fields, err := g.fields("", st)
	if err != nil {
		return "", err
	}
	drop := map[string]bool{}
	for _, k := range keys {
		drop[k] = true
	}
	var kept []fieldOut
	for _, f := range fields {
		if !drop[f.name] {
			kept = append(kept, f)
		}
	}
	if len(kept) == len(fields) {
		return "", fmt.Errorf("connsHeavyKeys %v matched no field of ConnsPayload — the names have drifted apart", keys)
	}

	var b strings.Builder
	fmt.Fprintf(&b, `// The `+"`conn:update`"+` payload, which is NOT ConnsPayload.
//
// internal/collect/connections.go marshals it through `+"`connsLight`"+`, which deletes
// %d keys (%s). The page room gets the full ConnsPayload; the dashboard card
// room gets this. Both are real, and a consumer of one must not be typed as the
// other -- the keys are ABSENT here, not null.
export interface ConnsUpdate {
`, len(keys), strings.Join(keys, ", "))
	for _, f := range kept {
		opt := ""
		if f.optional {
			opt = "?"
		}
		fmt.Fprintf(&b, "  %s%s: %s;\n", f.name, opt, f.ts)
	}
	b.WriteString("}\n")
	return b.String(), nil
}

// heavyKeys reads the string literals out of the `connsHeavyKeys` array.
func (g *gen) heavyKeys() ([]string, error) {
	src, ok := g.types.files["connections.go"]
	if !ok {
		return nil, fmt.Errorf("connections.go not read")
	}
	i := strings.Index(src, "connsHeavyKeys = ")
	if i < 0 {
		return nil, fmt.Errorf("connsHeavyKeys not found in connections.go — it was renamed, and conn:update's key set is now unknown")
	}
	open := strings.Index(src[i:], "{")
	close := strings.Index(src[i:], "}")
	if open < 0 || close < 0 || close < open {
		return nil, fmt.Errorf("connsHeavyKeys is not a brace literal")
	}
	var keys []string
	for _, part := range strings.Split(src[i+open+1:i+close], ",") {
		p := strings.TrimSpace(part)
		if p == "" {
			continue
		}
		s, err := strconv.Unquote(p)
		if err != nil {
			return nil, fmt.Errorf("connsHeavyKeys entry %q is not a plain string literal", p)
		}
		keys = append(keys, s)
	}
	if len(keys) == 0 {
		return nil, fmt.Errorf("connsHeavyKeys is empty")
	}
	return keys, nil
}

// ── The drift report ────────────────────────────────────────────────────────
//
// REPORT ONLY, and deliberately not a gate. It scans web/src for hand-written
// `interface X { ... }` blocks whose name matches a generated one and lists the
// properties they disagree about.
//
// It is not a gate because the comparison is by NAME, and several hand-written
// interfaces are honest partial views -- a card that reads four fields of a
// twenty-two field payload is not wrong to describe four. Failing on that would
// train people to widen the ignore list. What it is good for is answering "what
// would migrating this file cost", which is the question before 2c.
func reportDrift(g *gen) (int, error) {
	generated := map[string]map[string]string{}
	for _, k := range g.order {
		qual, name := "", k
		if i := strings.IndexByte(k, '.'); i >= 0 {
			qual, name = k[:i], k[i+1:]
		}
		st := g.pkg(qual).structs[name]
		fs, err := g.fields(qual, st)
		if err != nil {
			continue
		}
		m := map[string]string{}
		for _, f := range fs {
			m[f.name] = f.ts
		}
		generated[name] = m
	}

	var files []string
	err := filepath.Walk("web/src", func(p string, fi os.FileInfo, err error) error {
		if err != nil {
			return err
		}
		if !fi.IsDir() && strings.HasSuffix(p, ".ts") && !strings.Contains(p, "/gen/") {
			files = append(files, p)
		}
		return nil
	})
	if err != nil {
		return 0, err
	}
	sort.Strings(files)

	total := 0
	for _, f := range files {
		b, err := os.ReadFile(f)
		if err != nil {
			continue
		}
		for name, props := range scanInterfaces(string(b)) {
			gp, ok := generated[name]
			if !ok {
				continue
			}
			var extra, missing []string
			for p := range props {
				if _, ok := gp[p]; !ok {
					extra = append(extra, p)
				}
			}
			for p := range gp {
				if _, ok := props[p]; !ok {
					missing = append(missing, p)
				}
			}
			sort.Strings(extra)
			sort.Strings(missing)
			if len(extra) == 0 && len(missing) == 0 {
				continue
			}
			total++
			fmt.Printf("\n%s — interface %s\n", f, name)
			if len(extra) > 0 {
				// THE SERIOUS DIRECTION. A property the Go struct does not emit
				// is one the page reads and never receives, and TypeScript is
				// perfectly happy about it.
				fmt.Printf("  NOT ON THE WIRE (%d): %s\n", len(extra), strings.Join(extra, ", "))
			}
			if len(missing) > 0 {
				fmt.Printf("  sent but undeclared (%d): %s\n", len(missing), strings.Join(missing, ", "))
			}
		}
	}
	if total > 0 {
		fmt.Printf("\ntsgen: %d hand-written interface(s) disagree with the Go structs.\n"+
			"Names matching a payload type are compared; a partial view is normal, a\n"+
			"NOT ON THE WIRE property is not — nothing will ever set it.\n", total)
	}
	return total, nil
}

// scanInterfaces pulls `interface Name { prop: ... }` blocks out of TypeScript
// source. A regex-grade parse is enough for a report: it is looking for names
// and top-level property keys, and anything it misparses shows up as noise in a
// report a human reads rather than as a false failure in a gate.
// collapseInline replaces every balanced `{...}` span with a placeholder, so
// keys inside a one-line nested object are not mistaken for keys beside it.
func collapseInline(line string) string {
	var b strings.Builder
	depth := 0
	for _, r := range line {
		switch r {
		case '{':
			if depth == 0 {
				b.WriteString("object")
			}
			depth++
		case '}':
			if depth > 0 {
				depth--
			}
		default:
			if depth == 0 {
				b.WriteRune(r)
			}
		}
	}
	return b.String()
}

func scanInterfaces(src string) map[string]map[string]string {
	out := map[string]map[string]string{}
	for i := 0; i < len(src); {
		k := strings.Index(src[i:], "interface ")
		if k < 0 {
			break
		}
		k += i
		rest := src[k+len("interface "):]
		nameEnd := strings.IndexAny(rest, " \t\n{")
		if nameEnd <= 0 {
			i = k + 1
			continue
		}
		name := strings.TrimSpace(rest[:nameEnd])
		open := strings.Index(rest, "{")
		if open < 0 {
			break
		}
		depth, end := 0, -1
		for j := open; j < len(rest); j++ {
			switch rest[j] {
			case '{':
				depth++
			case '}':
				depth--
				if depth == 0 {
					end = j
				}
			}
			if end >= 0 {
				break
			}
		}
		if end < 0 {
			break
		}
		// ── DEPTH MATTERS, AND IGNORING IT PRODUCED A FALSE POSITIVE ────────
		//
		// Several interfaces here declare INLINE nested objects --
		// `manager: { enabled: boolean; ... }` in capsman.ts. A line-based scan
		// counts those inner keys as top-level, and the report then claimed five
		// CapsmanPayload properties were "NOT ON THE WIRE" when all five are
		// real, nested one level down. Only depth-1 keys are properties of THIS
		// interface.
		props := map[string]string{}
		nest := 0
		for _, line := range strings.Split(rest[open+1:end], "\n") {
			trimmed := strings.TrimSpace(line)
			opens := strings.Count(line, "{")
			closes := strings.Count(line, "}")
			atTop := nest == 0
			nest += opens - closes
			if nest < 0 {
				nest = 0
			}
			if !atTop {
				continue
			}
			if trimmed == "" || strings.HasPrefix(trimmed, "//") || strings.HasPrefix(trimmed, "*") || strings.HasPrefix(trimmed, "/*") {
				continue
			}
			// AND A BALANCED INLINE OBJECT IS STILL NESTED. `summary: { total:
			// number; down: number }` opens and closes on ONE line, so the depth
			// counter above never leaves the top level and the inner keys were
			// read as properties of the outer interface. That was the second
			// wave of false positives -- RoutingPayload's `down` and
			// `established` among them, both of which are real, inside
			// `summary`. Collapsing balanced braces leaves only this level.
			trimmed = collapseInline(trimmed)
			// One line can carry several properties (`ts: number; pollMs: number;`).
			for _, part := range strings.Split(trimmed, ";") {
				part = strings.TrimSpace(part)
				c := strings.Index(part, ":")
				if c <= 0 {
					continue
				}
				key := strings.TrimSpace(strings.TrimSuffix(strings.TrimSpace(part[:c]), "?"))
				key = strings.Trim(key, "'\"")
				if key == "" || strings.ContainsAny(key, " ()[]{}") {
					continue
				}
				props[key] = strings.TrimSpace(part[c+1:])
			}
		}
		if len(props) > 0 {
			out[name] = props
		}
		i = k + len("interface ") + end
	}
	return out
}
