// tsgen generates the TypeScript types for the WebSocket payloads, and the
// browser's event map, from the Go declarations that send them.
//
// ── WHY THIS EXISTS ─────────────────────────────────────────────────────────
//
// Every payload used to be defined TWICE: once as a Go struct with json tags,
// once as a hand-written TypeScript interface somebody kept in step by reading
// the Go. Nothing broke when the two drifted; the page rendered a field that
// was always undefined.
//
// The Go side is the schema. Every event is declared with its payload type —
// `hub.Declare[RoutingPayload]("routing:update")`, see internal/hub/event.go —
// and the compiler holds every send to its declaration. This reads the same
// declarations and writes three things:
//
//	an interface for every struct a declared payload reaches
//	`Events`, each struct-payload event mapped to its type, which
//	  web/src/socket.ts uses to type every handler
//	`HandEventName`, the events whose payload is a map and so has no struct to
//	  generate from; web/src/events-hand.ts must type exactly those
//
// So a Go field renamed is a TypeScript error wherever a page reads it, and an
// event added in Go is typed in the browser by the next run.
//
// ── AND WHY IT IS A GO PROGRAM ──────────────────────────────────────────────
//
// `cmd/webbuild` is the precedent: build-time tools live in Go so the image
// needs no Node. And its source is this repository, so its `-check` runs on
// any clone with nothing mounted.
//
// ── WHAT A NAIVE GENERATOR GETS WRONG ───────────────────────────────────────
//
//  1. `json:"-"` -- `Route.Flags` is not on the wire. Emitting it would invent
//     a property no consumer can read.
//  2. `omitempty` -- must become an OPTIONAL property, or every consumer is told
//     a key is always there when it is not.
//  3. A type with its own MarshalJSON writes whatever that method writes, so
//     its fields say nothing about the wire. Each one is declared in
//     marshalOverrides, and an undeclared one is a hard error rather than a
//     guess. `conn:update` is the largest: ConnsLight marshals ConnsPayload
//     with four keys DELETED, read out of `connsHeavyKeys` rather than retyped.
//
// Pointers are `T | null`: a nil pointer marshals to null, a key that arrives.
// Slices are `T[]`, never `T[] | null`: Go never sends a null array, and
// TestNoPayloadSendsANullArray is what holds it to that.
package main

import (
	"bytes"
	"flag"
	"fmt"
	"go/ast"
	"go/parser"
	"go/token"
	"io/fs"
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
		out   = flag.String("out", outRel, "file to write")
		check = flag.Bool("check", false, "fail if the committed file is stale instead of writing it")
	)
	flag.Parse()

	decls, err := findDeclarations("internal")
	if err != nil {
		fatal(err)
	}
	g := &gen{pkgs: map[string]*pkgTypes{}, emitted: map[string]bool{}}
	body, err := g.render(decls)
	if err != nil {
		fatal(err)
	}

	if *check {
		have, err := os.ReadFile(*out)
		if err != nil {
			fatal(fmt.Errorf("%s is missing; run `go run ./cmd/tsgen`", *out))
		}
		if !bytes.Equal(bytes.TrimSpace(have), bytes.TrimSpace(body)) {
			fmt.Fprintf(os.Stderr, "tsgen: %s is STALE - a payload struct or an event declaration changed and the types were not regenerated.\n"+
				"Run: go run ./cmd/tsgen\n", *out)
			os.Exit(1)
		}
		fmt.Printf("tsgen: %s is current (%d interfaces; %d events, %d of them hand-typed)\n",
			*out, g.count, len(decls), g.hand)
		return
	}

	if err := os.WriteFile(*out, body, 0o644); err != nil {
		fatal(err)
	}
	fmt.Printf("tsgen: wrote %s - %d interfaces; %d events, %d of them hand-typed\n",
		*out, g.count, len(decls), g.hand)
}

func fatal(err error) {
	fmt.Fprintln(os.Stderr, "tsgen:", err)
	os.Exit(1)
}

// ── Finding the declarations ────────────────────────────────────────────────

// decl is one `hub.Declare[T]("name")`.
type decl struct {
	event string
	pkg   string // the declaring package, which is where a bare T is resolved
	typ   ast.Expr
}

// findDeclarations reads every event declaration under root.
//
// Test files are skipped: they declare `test:*` events of their own. Comments
// are not parsed, so the example in internal/hub/event.go's doc is not taken
// for a declaration.
func findDeclarations(root string) ([]decl, error) {
	var out []decl
	err := filepath.WalkDir(root, func(p string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if d.IsDir() {
			if d.Name() == "testdata" {
				return filepath.SkipDir
			}
			return nil
		}
		if !strings.HasSuffix(p, ".go") || strings.HasSuffix(p, "_test.go") {
			return nil
		}
		src, err := os.ReadFile(p)
		if err != nil {
			return err
		}
		if !bytes.Contains(src, []byte("hub.Declare[")) {
			return nil
		}
		f, err := parser.ParseFile(token.NewFileSet(), p, src, 0)
		if err != nil {
			return err
		}
		var bad error
		ast.Inspect(f, func(n ast.Node) bool {
			call, ok := n.(*ast.CallExpr)
			if !ok {
				return true
			}
			ix, ok := call.Fun.(*ast.IndexExpr)
			if !ok {
				return true
			}
			sel, ok := ix.X.(*ast.SelectorExpr)
			if !ok || sel.Sel.Name != "Declare" || exprName(sel.X) != "hub" {
				return true
			}
			var lit *ast.BasicLit
			if len(call.Args) == 1 {
				lit, _ = call.Args[0].(*ast.BasicLit)
			}
			if lit == nil || lit.Kind != token.STRING {
				// A computed name cannot be read without running the program, so
				// its event could not be typed — refused rather than skipped.
				bad = fmt.Errorf("%s: hub.Declare must be given its event name as a string literal", p)
				return false
			}
			name, err := strconv.Unquote(lit.Value)
			if err != nil {
				bad = fmt.Errorf("%s: %v", p, err)
				return false
			}
			out = append(out, decl{event: name, pkg: f.Name.Name, typ: ix.Index})
			return true
		})
		return bad
	})
	if err != nil {
		return nil, err
	}
	// A FLOOR. The form is matched by shape, and a shape that stopped matching
	// would generate an event map with nothing in it — which type-checks.
	if len(out) < 40 {
		return nil, fmt.Errorf("only %d event declarations found under %s - the declaration form changed and this is reading nothing", len(out), root)
	}
	sort.Slice(out, func(i, j int) bool { return out[i].event < out[j].event })
	for i := 1; i < len(out); i++ {
		if out[i].event == out[i-1].event {
			return nil, fmt.Errorf("event %q is declared twice", out[i].event)
		}
	}
	return out, nil
}

// ── Reading a package ───────────────────────────────────────────────────────

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
	if len(p.structs) == 0 && len(p.aliases) == 0 {
		return nil, fmt.Errorf("no types found in %s - the parser or the path is wrong", dir)
	}
	return p, nil
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

// ── Rendering ───────────────────────────────────────────────────────────────

type gen struct {
	pkgs       map[string]*pkgTypes
	emitted    map[string]bool
	order      []string // "pkg.Name"
	count      int
	hand       int
	connsLight bool
}

// pkg loads a package by the name it is referred to by. Every package here is
// internal/<name>, which is what lets a qualifier be resolved without reading
// the importing file's import block.
func (g *gen) pkg(qual string) (*pkgTypes, error) {
	if p, ok := g.pkgs[qual]; ok {
		return p, nil
	}
	p, err := loadPackage(filepath.Join("internal", qual))
	if err != nil {
		return nil, fmt.Errorf("package %q: %w", qual, err)
	}
	g.pkgs[qual] = p
	return p, nil
}

// key names a Go type unambiguously: two packages may both declare `Row`.
func key(qual, name string) string { return qual + "." + name }

func splitKey(k string) (qual, name string) {
	i := strings.IndexByte(k, '.')
	return k[:i], k[i+1:]
}

// ── TYPES WHOSE WIRE FORM IS NOT THEIR FIELD LIST ───────────────────────────
//
// `guard.Rate` is `{Bps int64; Set bool}` and would generate a two-field
// interface. It actually writes a BARE NUMBER, or null when unset, because the
// page draws those differently.
//
// `geoplace.Location` enumerates its keys rather than reflecting its fields:
// four always, and `accuracyKm` / `wanIp` only for an auto-located router —
// where `accuracyKm` is sent as null when there is no usable radius, which is
// why it is optional AND nullable. Its `CC` field is never on the wire.
//
// `collect.ConnsLight` is handled by renderConnsLight, because its wire form
// is a struct with keys removed rather than a shape that fits here.
var marshalOverrides = map[string]string{
	"guard.Rate":        "number | null",
	"geoplace.Location": "{ lat: number; lon: number; source: string; label: string; accuracyKm?: number | null; wanIp?: string }",
}

// ── FIELDS WHOSE GO TYPE SAYS NOTHING ───────────────────────────────────────
//
// `TopologyPayload.Nodes` is `[]any` because one slice holds three kinds of
// node — the router itself, its neighbours and its clients — each its own
// struct. Generated naively it is `unknown[]`, and the page would type the
// nodes by hand. Instead the kinds are named here, generated, and the field
// becomes their union.
//
// A LIST OF KINDS IS A CLAIM, so it is checked: TestTopologyNodesAreTheDeclaredKinds
// in internal/collect builds the payload from a capture and fails unless the
// kinds actually in the slice are exactly these.
var fieldOverrides = map[string][]string{
	"collect.TopologyPayload.Nodes": {"collect.TopoCore", "collect.TopoNeighbor", "collect.TopoClient"},
}

// ── STRUCTS ONLY A MAP PAYLOAD CARRIES ──────────────────────────────────────
//
// backups:diff sends its hunks, ping:history its points, res:error its field
// errors and the wifi scan its rows — each a Go struct, inside a payload built
// as a map. No declaration reaches them, so nothing would generate them, and
// web/src/events-hand.ts would have to restate them by hand. Listed here, they
// are generated like any other and imported there instead.
var extraRoots = []string{"backups.Hunk", "collect.PingPoint", "resource.Error", "wifiscan.Row"}

// ── TYPESCRIPT NAMES ────────────────────────────────────────────────────────
//
// A TypeScript interface name is global to the file, a Go type name only to its
// package. Three packages each have a `Row`, and each is renamed here to the
// name the frontend already used for it. `resource.Error` would shadow the
// global `Error`. An unlisted collision is refused in render(), with both
// sources named, rather than resolved by a rule nobody chose.
var tsNames = map[string]string{
	"alert.Row":      "AlertRow",
	"backups.Row":    "BackupRow",
	"resource.Error": "ResFieldError",
	"routers.Row":    "RouterStatsRow",
	"wifiscan.Row":   "WifiscanRow",
}

func tsName(k string) string {
	if n, ok := tsNames[k]; ok {
		return n
	}
	_, name := splitKey(k)
	return name
}

// fieldOut is one rendered property.
type fieldOut struct {
	name     string
	ts       string
	optional bool
}

func (g *gen) render(decls []decl) ([]byte, error) {
	type evType struct{ event, ts string }
	var structEvents []evType
	var handEvents []string
	for _, d := range decls {
		ts, hand, err := g.declType(d.pkg, d.typ)
		if err != nil {
			return nil, fmt.Errorf("event %q: %w", d.event, err)
		}
		if hand {
			handEvents = append(handEvents, d.event)
		} else {
			structEvents = append(structEvents, evType{d.event, ts})
		}
	}
	g.hand = len(handEvents)
	for _, k := range extraRoots {
		if err := g.walk(splitKey(k)); err != nil {
			return nil, fmt.Errorf("extraRoots %s: %w", k, err)
		}
	}

	seen := map[string]string{}
	for _, k := range g.order {
		n := tsName(k)
		if prev, clash := seen[n]; clash {
			return nil, fmt.Errorf("interface name %q would be declared twice, from %s and %s; add one to tsNames", n, prev, k)
		}
		seen[n] = k
	}

	var buf bytes.Buffer
	buf.WriteString(`// GENERATED by cmd/tsgen - do not edit.
//
// The WebSocket payload types and the event map, read from the Go declarations
// that send them (` + "`hub.Declare`" + `, internal/hub/event.go). Every property, its
// type and its optionality come from a json tag. ` + "`go run ./cmd/tsgen -check`" + `
// fails when this file is stale, and tools/verify.sh runs it.
//
// Optional (` + "`?`" + `) means the Go field carries ` + "`omitempty`" + ` and the key can be
// ABSENT. ` + "`| null`" + ` means the Go field is a pointer and the key is present
// carrying null. They are different things and the distinction is load-bearing:
// the dashboard's count setters test ` + "`!== undefined`" + `, so an absent key renders an
// em dash while an explicit null renders the string "null". Arrays are never
// null: Go does not send one, and TestNoPayloadSendsANullArray holds it to that.

`)
	for _, k := range g.order {
		block, err := g.renderStruct(k)
		if err != nil {
			return nil, err
		}
		buf.WriteString(block)
		buf.WriteString("\n")
	}
	if g.connsLight {
		light, err := g.renderConnsLight()
		if err != nil {
			return nil, err
		}
		buf.WriteString(light)
		buf.WriteString("\n")
	}

	buf.WriteString(`// Every event whose payload is a struct, and its type. web/src/socket.ts types
// each handler from this, so a page listening for an event gets exactly the
// payload Go declared for it.
export interface Events {
`)
	for _, e := range structEvents {
		fmt.Fprintf(&buf, "  '%s': %s;\n", e.event, e.ts)
	}
	buf.WriteString("}\n\n")

	buf.WriteString(`// The events whose payload is a map, and so has no struct to generate from.
// web/src/events-hand.ts types exactly these - tsc fails if it misses one or
// types one that is not here.
export type HandEventName =
`)
	for i, e := range handEvents {
		end := ""
		if i == len(handEvents)-1 {
			end = ";"
		}
		fmt.Fprintf(&buf, "  | '%s'%s\n", e, end)
	}
	return buf.Bytes(), nil
}

// declType is the TypeScript type of a declared payload, or hand=true when the
// payload is a map and so has no struct to generate from.
func (g *gen) declType(pkg string, e ast.Expr) (ts string, hand bool, err error) {
	switch t := e.(type) {
	case *ast.MapType:
		return "", true, nil
	case *ast.ArrayType:
		inner, hand, err := g.declType(pkg, t.Elt)
		if err != nil || hand {
			return "", hand, err
		}
		return inner + "[]", false, nil
	case *ast.Ident:
		return g.declNamed(pkg, t.Name)
	case *ast.SelectorExpr:
		return g.declNamed(exprName(t.X), t.Sel.Name)
	}
	return "", false, fmt.Errorf("a payload of type %T cannot be generated", e)
}

func (g *gen) declNamed(qual, name string) (string, bool, error) {
	k := key(qual, name)
	if k == "collect.ConnsLight" {
		// ConnsPayload is emitted as well: conn:country-data and
		// conn:source-data carry its heavy indexes, and web/src/events-hand.ts
		// types them as Picks of it rather than retyping the fields.
		g.connsLight = true
		if err := g.walk("collect", "ConnsPayload"); err != nil {
			return "", false, err
		}
		return "ConnsUpdate", false, nil
	}
	p, err := g.pkg(qual)
	if err != nil {
		return "", false, err
	}
	if _, ok := p.structs[name]; ok {
		if err := g.walk(qual, name); err != nil {
			return "", false, err
		}
		return tsName(k), false, nil
	}
	// A named map (`type Settings map[string]any`) is a map.
	if under, ok := p.aliases[name]; ok {
		return g.declType(qual, under)
	}
	return "", false, fmt.Errorf("%s is not a struct, a slice of structs or a map", k)
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
	p, err := g.pkg(qual)
	if err != nil {
		return err
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
		if len(f.Names) > 0 {
			if kinds, ok := fieldOverrides[k+"."+f.Names[0].Name]; ok {
				for _, kind := range kinds {
					if err := g.walk(splitKey(kind)); err != nil {
						return err
					}
				}
				continue
			}
		}
		for _, dep := range namedDeps(qual, f.Type) {
			// A package that cannot be loaded (`time`) is skipped here; tsType
			// reports it properly if a field really is of that type.
			dp, err := g.pkg(dep.qual)
			if err != nil {
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
func namedDeps(qual string, e ast.Expr) []ref {
	switch t := e.(type) {
	case *ast.Ident:
		return []ref{{qual, t.Name}}
	case *ast.StarExpr:
		return namedDeps(qual, t.X)
	case *ast.ArrayType:
		return namedDeps(qual, t.Elt)
	case *ast.MapType:
		return append(namedDeps(qual, t.Key), namedDeps(qual, t.Value)...)
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
	qual, name := splitKey(k)
	p, err := g.pkg(qual)
	if err != nil {
		return "", err
	}
	fields, err := g.fields(qual, name, p.structs[name])
	if err != nil {
		return "", fmt.Errorf("%s: %w", k, err)
	}
	var b strings.Builder
	fmt.Fprintf(&b, "export interface %s {\n", tsName(k))
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

func (g *gen) fields(qual, name string, st *ast.StructType) ([]fieldOut, error) {
	var out []fieldOut
	for _, f := range st.Fields.List {
		// EMBEDDED STRUCTS ARE FLATTENED, because that is what encoding/json
		// does with them: the inner fields appear at the outer level.
		if len(f.Names) == 0 {
			id, ok := f.Type.(*ast.Ident)
			if !ok {
				return nil, fmt.Errorf("embedded field of unsupported shape")
			}
			p, err := g.pkg(qual)
			if err != nil {
				return nil, err
			}
			inner, ok := p.structs[id.Name]
			if !ok {
				return nil, fmt.Errorf("embedded %s is not a struct in this package", id.Name)
			}
			sub, err := g.fields(qual, id.Name, inner)
			if err != nil {
				return nil, err
			}
			out = append(out, sub...)
			continue
		}
		if skipField(f) {
			continue
		}
		tag, omitempty := jsonTag(f)
		if tag == "" {
			// NO TAG IS A REFUSAL, not a guess. encoding/json would use the Go
			// field name verbatim, which is a capitalised key no TypeScript in
			// this repo uses -- so a missing tag is much more likely an oversight
			// than an intent, and inventing the key would hide it.
			return nil, fmt.Errorf("field %s has no json tag; add one (or `json:\"-\"`) rather than letting the key be guessed", f.Names[0].Name)
		}
		var ts string
		if kinds, ok := fieldOverrides[key(qual, name)+"."+f.Names[0].Name]; ok {
			if _, isSlice := f.Type.(*ast.ArrayType); !isSlice {
				return nil, fmt.Errorf("field %s is overridden as a union of kinds, and only a slice can be", tag)
			}
			names := make([]string, len(kinds))
			for i, kind := range kinds {
				names[i] = tsName(kind)
			}
			ts = "(" + strings.Join(names, " | ") + ")[]"
		} else {
			var err error
			if ts, err = g.tsType(qual, f.Type); err != nil {
				return nil, fmt.Errorf("field %s: %w", tag, err)
			}
		}
		out = append(out, fieldOut{name: tsKey(tag), ts: ts, optional: omitempty})
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
		// NEVER NULL. A nil slice would marshal to null, and Go does not send
		// one: TestNoPayloadSendsANullArray builds every declared payload and
		// fails on any. A union element keeps its parentheses.
		if strings.Contains(inner, " | ") {
			inner = "(" + inner + ")"
		}
		return inner + "[]", nil
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
	p, err := g.pkg(qual)
	if err != nil {
		return "", err
	}
	if _, ok := p.structs[name]; ok {
		if p.marshalers[name] {
			return "", fmt.Errorf("%s has its own MarshalJSON; add it to marshalOverrides", key(qual, name))
		}
		return tsName(key(qual, name)), nil
	}
	// A named scalar (`type RouteFlags uint8`) resolves to its underlying type.
	if under, ok := p.aliases[name]; ok {
		return g.tsType(qual, under)
	}
	return "", fmt.Errorf("unknown type %q in package %q", name, p.name)
}

// ── The one payload whose wire form is not its struct form ──────────────────
//
// `conn:update` carries ConnsLight, which marshals a ConnsPayload through a
// map and DELETES four keys. The struct is unchanged, so nothing about the Go
// type says this happened -- a generator that only read the struct would tell
// every consumer of `conn:update` that four heavy indexes are present when they
// are not there at all.
//
// The key names are read out of `connsHeavyKeys` rather than retyped here, so
// adding a fifth to that array is carried through instead of silently missed.
func (g *gen) renderConnsLight() (string, error) {
	keys, err := g.heavyKeys()
	if err != nil {
		return "", err
	}
	p, err := g.pkg("collect")
	if err != nil {
		return "", err
	}
	st, ok := p.structs["ConnsPayload"]
	if !ok {
		return "", fmt.Errorf("ConnsPayload not found")
	}
	fields, err := g.fields("collect", "ConnsPayload", st)
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
		return "", fmt.Errorf("connsHeavyKeys %v matched no field of ConnsPayload - the names have drifted apart", keys)
	}

	var b strings.Builder
	fmt.Fprintf(&b, `// The `+"`conn:update`"+` payload, which is NOT ConnsPayload.
//
// internal/collect/connections.go sends it through `+"`ConnsLight`"+`, which deletes
// %d keys (%s). They travel separately, as conn:country-data and
// conn:source-data -- the keys are ABSENT here, not null.
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
	p, err := g.pkg("collect")
	if err != nil {
		return nil, err
	}
	src, ok := p.files["connections.go"]
	if !ok {
		return nil, fmt.Errorf("connections.go not read")
	}
	i := strings.Index(src, "connsHeavyKeys = ")
	if i < 0 {
		return nil, fmt.Errorf("connsHeavyKeys not found in connections.go - it was renamed, and conn:update's key set is now unknown")
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
