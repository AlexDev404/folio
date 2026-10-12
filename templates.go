package folio

import (
	"fmt"
	"html/template"
	"io/fs"
	"path"
	"strings"
	"text/template/parse"
)

const routesDir = "routes"

// site is one parse of the template tree.
type site struct {
	tmpl *template.Template
	// routes maps a route name to its page template.
	routes map[string]*template.Template
}

// parseSite parses every .tmpl file in cfg.Templates. Files under routes/
// become pages named after their path; a route file may either be the page
// body itself or {{define}} a template with the route's name.
func parseSite(cfg Config) (*site, error) {
	root := template.New("").Funcs(cfg.Funcs)
	routeFiles := map[string]string{} // route -> file

	err := fs.WalkDir(cfg.Templates, ".", func(file string, d fs.DirEntry, err error) error {
		if err != nil || d.IsDir() || path.Ext(file) != ".tmpl" {
			return err
		}
		src, err := fs.ReadFile(cfg.Templates, file)
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

	s := &site{tmpl: root, routes: make(map[string]*template.Template, len(routeFiles))}
	for route, file := range routeFiles {
		t := root.Lookup(route)
		if t == nil || t.Tree == nil || parse.IsEmptyTree(t.Tree.Root) {
			return nil, fmt.Errorf("folio: %s is empty; it must contain the page or {{define %q}}", file, route)
		}
		s.routes[route] = t
	}
	return s, nil
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

// routeForPath maps a URL path to a route name: "/" -> "index", "/tools/" -> "tools".
func routeForPath(urlPath string) string {
	route := strings.Trim(urlPath, "/")
	if route == "" {
		return "index"
	}
	return route
}
