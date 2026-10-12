package folio

import (
	"cmp"
	"fmt"
	"html/template"
	"io/fs"
	"net/url"
	"path"
	"slices"
	"strings"
	"text/template/parse"
)

const routesDir = "routes"

// builtins are parsed before the app's templates, which may override them.
const builtins = `
{{- define "folio/head" -}}
<meta name="folio-route" content="{{.Route}}">
<meta name="folio-shell" content="{{.Shell}}">
<script src="{{folioClientURL}}"></script>
{{- end -}}
`

// site is one parse of the template tree.
type site struct {
	tmpl *template.Template
	// pages maps every route name to its page.
	pages map[string]*page
	// patterns holds routes with [params], most specific first.
	patterns []*page
}

type page struct {
	route    string
	tmpl     *template.Template
	segments []segment // nil for static routes
	literals int
	// layouts wrap the page, innermost first; the last one is the shell.
	layouts []*template.Template
	shell   string
}

type segment struct {
	literal string
	param   string
	rest    bool
}

// parseSite parses every .tmpl file in the templates FS. Files under routes/
// become pages named after their path; a route file may either be the page
// body itself or {{define}} a template with the route's name.
func (a *App) parseSite() (*site, error) {
	root := template.New("").Funcs(template.FuncMap{
		"asset":          a.assetURL,
		"folioClientURL": a.client.url,
	}).Funcs(a.cfg.Funcs)
	if a.cfg.StrictTemplates {
		root.Option("missingkey=error")
	}
	template.Must(root.New("folio/builtins").Parse(builtins))

	routeFiles := map[string]string{} // route -> file
	err := fs.WalkDir(a.cfg.Templates, ".", func(file string, d fs.DirEntry, err error) error {
		if err != nil || d.IsDir() || path.Ext(file) != ".tmpl" {
			return err
		}
		src, err := fs.ReadFile(a.cfg.Templates, file)
		if err != nil {
			return err
		}

		name := file
		if route, ok := routeName(file); ok {
			if prev, dup := routeFiles[route]; dup {
				return fmt.Errorf("folio: %s and %s both map to route %q", prev, file, route)
			}
			routeFiles[route] = file
			name = route
		}
		if _, err := root.New(name).Parse(string(src)); err != nil {
			return fmt.Errorf("folio: parsing %s: %w", file, err)
		}
		return nil
	})
	if err != nil {
		return nil, err
	}

	s := &site{tmpl: root, pages: make(map[string]*page, len(routeFiles))}
	for route, file := range routeFiles {
		t := root.Lookup(route)
		if t == nil || t.Tree == nil || parse.IsEmptyTree(t.Tree.Root) {
			return nil, fmt.Errorf("folio: %s is empty; it must contain the page or {{define %q}}", file, route)
		}
		p := &page{route: route, tmpl: t}
		if err := p.parsePattern(); err != nil {
			return nil, fmt.Errorf("folio: %s: %w", file, err)
		}
		p.layouts, p.shell = s.layoutsFor(route, a.cfg.Layout)
		s.pages[route] = p
		if p.segments != nil {
			s.patterns = append(s.patterns, p)
		}
	}

	// Most specific first: more literal segments, then catch-alls last, then
	// longer patterns; ties break on the name so matching is deterministic.
	slices.SortFunc(s.patterns, func(x, y *page) int {
		return cmp.Or(
			cmp.Compare(y.literals, x.literals),
			cmp.Compare(btoi(x.hasRest()), btoi(y.hasRest())),
			cmp.Compare(len(y.segments), len(x.segments)),
			cmp.Compare(x.route, y.route),
		)
	})
	return s, nil
}

// layoutsFor walks from the route's own directory up to the root, collecting
// layout/<prefix> templates. A shell/<prefix> template stops the walk and
// becomes the outermost layout; otherwise defaultLayout is.
func (s *site) layoutsFor(route, defaultLayout string) ([]*template.Template, string) {
	var chain []*template.Template
	for prefix := route; prefix != ""; prefix = parentDir(prefix) {
		if t := s.tmpl.Lookup("shell/" + prefix); t != nil {
			return append(chain, t), t.Name()
		}
		if t := s.tmpl.Lookup("layout/" + prefix); t != nil && t.Name() != defaultLayout {
			chain = append(chain, t)
		}
	}
	if t := s.tmpl.Lookup(defaultLayout); t != nil {
		return append(chain, t), t.Name()
	}
	return chain, ""
}

func parentDir(route string) string {
	i := strings.LastIndexByte(route, '/')
	if i < 0 {
		return ""
	}
	return route[:i]
}

// routeName maps "routes/tools/index.tmpl" to "tools", "routes/index.tmpl" to
// "index" and "routes/about.tmpl" to "about". ok is false outside routes/.
func routeName(file string) (string, bool) {
	rest, ok := strings.CutPrefix(file, routesDir+"/")
	if !ok {
		return "", false
	}
	rest = strings.TrimSuffix(rest, ".tmpl")
	if rest == "index" {
		return "index", true
	}
	return strings.TrimSuffix(rest, "/index"), true
}

// parsePattern fills in segments for routes containing [param] or
// [...rest] parts. Static routes keep segments nil.
func (p *page) parsePattern() error {
	parts := strings.Split(p.route, "/")
	segs := make([]segment, len(parts))
	seen := map[string]bool{}
	dynamic := false
	for i, part := range parts {
		name, isParam := strings.CutPrefix(part, "[")
		if !isParam {
			if strings.ContainsAny(part, "[]") {
				return fmt.Errorf("invalid route segment %q", part)
			}
			segs[i] = segment{literal: part}
			p.literals++
			continue
		}
		name, ok := strings.CutSuffix(name, "]")
		if !ok {
			return fmt.Errorf("invalid route segment %q", part)
		}
		name, rest := strings.CutPrefix(name, "...")
		if rest && i != len(parts)-1 {
			return fmt.Errorf("catch-all %q must be the last segment", part)
		}
		if name == "" || strings.ContainsAny(name, "[]") || seen[name] {
			return fmt.Errorf("invalid or duplicate parameter %q", part)
		}
		seen[name] = true
		dynamic = true
		segs[i] = segment{param: name, rest: rest}
	}
	if dynamic {
		p.segments = segs
	}
	return nil
}

func btoi(b bool) int {
	if b {
		return 1
	}
	return 0
}

func (p *page) hasRest() bool {
	return len(p.segments) > 0 && p.segments[len(p.segments)-1].rest
}

func (p *page) match(parts []string) (map[string]string, bool) {
	params := make(map[string]string, len(p.segments))
	for i, seg := range p.segments {
		if i >= len(parts) {
			return nil, false
		}
		switch {
		case seg.rest:
			params[seg.param] = strings.Join(parts[i:], "/")
			return params, true
		case seg.param != "":
			params[seg.param] = parts[i]
		case seg.literal != parts[i]:
			return nil, false
		}
	}
	return params, len(parts) == len(p.segments)
}

// match finds the page for an escaped URL path: "/" is "index", static
// routes win over parameterised ones, and parameter values are unescaped.
func (s *site) match(escapedPath string) (*page, map[string]string, bool) {
	trimmed := strings.Trim(escapedPath, "/")
	if trimmed == "" {
		trimmed = "index"
	}
	parts := strings.Split(trimmed, "/")
	for i, part := range parts {
		u, err := url.PathUnescape(part)
		if err != nil || u == "" {
			return nil, nil, false
		}
		parts[i] = u
	}
	if p, ok := s.pages[strings.Join(parts, "/")]; ok && p.segments == nil {
		return p, nil, true
	}
	for _, p := range s.patterns {
		if params, ok := p.match(parts); ok {
			return p, params, true
		}
	}
	return nil, nil, false
}
