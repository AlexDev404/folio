package folio

import (
	"bytes"
	"errors"
	"html/template"
	"io/fs"
	"maps"
	"mime"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"sync"
)

// View is what every page, layout and error template receives as its dot.
type View struct {
	// Route is the matched route name, e.g. "index" or "journal/[id]".
	Route string
	// Path is the request's URL path.
	Path string
	// Params holds the route's [param] values.
	Params map[string]string
	// Page is the page data set by hooks and Config.Globals. It always has
	// Message, Failure and Errors (a map of field name to message).
	Page map[string]any
	// Model is whatever a hook passed to Ctx.SetModel.
	Model any
	// Flash is the message a previous request stored with Ctx.Flash.
	Flash string
	// HTML is the rendered page, or the next layout in. Layouts only.
	HTML template.HTML
	// Shell is the outermost layout's name.
	Shell string
	// Status is the response status code.
	Status int
	// Dev is true when Config.Reload is on.
	Dev bool
	// Error describes what went wrong. Error pages in dev mode only.
	Error string
}

// Request headers sent by the client script, and response headers it reads.
const (
	headerNavigate = "X-Folio-Navigate"
	headerShell    = "X-Folio-Shell"
	headerRoute    = "X-Folio-Route"
	headerPartial  = "X-Folio-Partial"
	headerTitle    = "X-Folio-Title"
)

var bufferPool = sync.Pool{New: func() any { return new(bytes.Buffer) }}

func getBuffer() *bytes.Buffer {
	b := bufferPool.Get().(*bytes.Buffer)
	b.Reset()
	return b
}

func putBuffer(b *bytes.Buffer) {
	if b.Cap() <= 1<<20 {
		bufferPool.Put(b)
	}
}

func (a *App) servePage(w http.ResponseWriter, r *http.Request) {
	s, err := a.currentSite()
	if err != nil {
		a.logger.Error("parsing templates", "error", err)
		a.errorPage(w, r, nil, http.StatusInternalServerError, err)
		return
	}

	p, params, ok := s.match(r.URL.EscapedPath())
	if !ok {
		a.errorPage(w, r, s, http.StatusNotFound, nil)
		return
	}
	hooks := a.hooks[p.route]

	switch r.Method {
	case http.MethodGet, http.MethodHead:
	case http.MethodPost:
		if hooks == nil || hooks.action == nil {
			a.methodNotAllowed(w, hooks)
			return
		}
	default:
		a.methodNotAllowed(w, hooks)
		return
	}

	data := map[string]any{
		"Errors":  map[string]string{},
		"Message": nil,
		"Failure": nil,
	}
	maps.Copy(data, a.cfg.Globals)
	if params == nil {
		params = map[string]string{}
	}

	v := &View{
		Route:  p.route,
		Path:   r.URL.Path,
		Params: params,
		Page:   data,
		Shell:  p.shell,
		Status: http.StatusOK,
		Dev:    a.cfg.Reload,
	}
	c := &Ctx{
		Request: r,
		Route:   p.route,
		Page:    data,
		Logger:  a.logger.With("route", p.route),
		view:    v,
		w:       w,
	}

	for _, load := range a.loadAll {
		if !a.runHook(c, load, s) {
			return
		}
	}
	if hooks != nil {
		if r.Method == http.MethodPost {
			if err := a.parseForm(r); err != nil {
				http.Error(w, "Bad Request", http.StatusBadRequest)
				return
			}
			if !a.runHook(c, hooks.action, s) {
				return
			}
		}
		for _, load := range hooks.load {
			if !a.runHook(c, load, s) {
				return
			}
		}
	}

	if sess := GetSession(r); sess.has(flashKey) {
		v.Flash = sess.Pop(flashKey)
	}
	a.render(w, r, s, p, v)
}

func (a *App) parseForm(r *http.Request) error {
	if mt, _, _ := mime.ParseMediaType(r.Header.Get("Content-Type")); mt == "multipart/form-data" {
		return r.ParseMultipartForm(a.cfg.MaxFormMemory)
	}
	return r.ParseForm()
}

// runHook runs fn and reports whether rendering should continue.
func (a *App) runHook(c *Ctx, fn LoadFunc, s *site) bool {
	err := fn(c)
	if c.written {
		if err != nil {
			c.Logger.Error("hook failed after writing the response", "error", err)
		}
		return false
	}
	if errors.Is(err, ErrNotFound) {
		a.errorPage(c.w, c.Request, s, http.StatusNotFound, nil)
		return false
	}
	if err != nil {
		c.Logger.Error("hook failed", "error", err)
		a.errorPage(c.w, c.Request, s, http.StatusInternalServerError, err)
		return false
	}
	return true
}

// render executes the page and wraps it in its layouts. When the client
// navigates in place and already shows the same shell, the shell is left out
// and only the inner HTML is sent.
func (a *App) render(w http.ResponseWriter, r *http.Request, s *site, p *page, v *View) {
	out := getBuffer()
	defer func() { putBuffer(out) }()

	if err := p.tmpl.Execute(out, v); err != nil {
		a.logger.Error("rendering page", "route", p.route, "error", err)
		a.errorPage(w, r, s, http.StatusInternalServerError, err)
		return
	}

	layouts := p.layouts
	partial := r.Header.Get(headerNavigate) != "" && p.shell != "" && r.Header.Get(headerShell) == p.shell
	if partial {
		layouts = layouts[:len(layouts)-1]
	}
	for _, layout := range layouts {
		v.HTML = template.HTML(out.String())
		next := getBuffer()
		if err := layout.Execute(next, v); err != nil {
			putBuffer(next)
			a.logger.Error("rendering layout", "route", p.route, "layout", layout.Name(), "error", err)
			a.errorPage(w, r, s, http.StatusInternalServerError, err)
			return
		}
		putBuffer(out)
		out = next
	}

	h := w.Header()
	h.Set("Content-Type", "text/html; charset=utf-8")
	h.Set("Content-Length", strconv.Itoa(out.Len()))
	h.Add("Vary", headerNavigate+", "+headerShell)
	h.Set(headerRoute, p.route)
	h.Set(headerShell, p.shell)
	if partial {
		h.Set(headerPartial, "1")
		if title, ok := a.pageTitle(s, v); ok {
			h.Set(headerTitle, url.PathEscape(title))
		}
	}
	w.WriteHeader(v.Status)
	out.WriteTo(w)
}

// pageTitle is the document title for in-place navigation: the output of the
// app's "folio/title" template if it defines one (use the same template in
// the shell's <title>), otherwise Page["Title"]. ok is false when neither
// gives a title, and the browser keeps the current one.
func (a *App) pageTitle(s *site, v *View) (string, bool) {
	if t := s.tmpl.Lookup("folio/title"); t != nil {
		var b strings.Builder
		if err := t.Execute(&b, v); err != nil {
			a.logger.Error("rendering folio/title", "route", v.Route, "error", err)
			return "", false
		}
		return strings.TrimSpace(b.String()), true
	}
	title, ok := v.Page["Title"].(string)
	return title, ok
}

func (a *App) methodNotAllowed(w http.ResponseWriter, hooks *routeHooks) {
	allow := "GET, HEAD"
	if hooks != nil && hooks.action != nil {
		allow += ", POST"
	}
	w.Header().Set("Allow", allow)
	http.Error(w, "Method Not Allowed", http.StatusMethodNotAllowed)
}

// errorPage renders the "error/<code>" template if one is defined, then falls
// back to <static>/errors/<code>.html, then to plain text. In dev mode a 500
// caused by err shows the error instead.
func (a *App) errorPage(w http.ResponseWriter, r *http.Request, s *site, code int, err error) {
	h := w.Header()
	h.Del("Content-Length")
	h.Set("Cache-Control", "no-store")

	v := &View{Path: r.URL.Path, Status: code, Dev: a.cfg.Reload, Page: map[string]any{}, Params: map[string]string{}}
	if a.cfg.Reload && err != nil {
		v.Error = err.Error()
		buf := getBuffer()
		defer putBuffer(buf)
		devErrorPage.Execute(buf, v)
		h.Set("Content-Type", "text/html; charset=utf-8")
		w.WriteHeader(code)
		buf.WriteTo(w)
		return
	}

	name := strconv.Itoa(code)
	if s != nil {
		if t := s.tmpl.Lookup("error/" + name); t != nil {
			buf := getBuffer()
			defer putBuffer(buf)
			if err := t.Execute(buf, v); err == nil {
				h.Set("Content-Type", "text/html; charset=utf-8")
				w.WriteHeader(code)
				buf.WriteTo(w)
				return
			}
		}
	}
	if b, err := fs.ReadFile(a.cfg.Static, "errors/"+name+".html"); err == nil {
		h.Set("Content-Type", "text/html; charset=utf-8")
		w.WriteHeader(code)
		w.Write(b)
		return
	}
	http.Error(w, http.StatusText(code), code)
}

var devErrorPage = template.Must(template.New("dev-error").Parse(`<!DOCTYPE html>
<html lang="en">
<head>
<meta charset="utf-8">
<meta name="viewport" content="width=device-width, initial-scale=1">
<title>{{.Status}} · folio</title>
<style>
  body { margin: 0; font: 15px/1.5 system-ui, sans-serif; background: #1c1917; color: #f5f5f4; }
  main { max-width: 860px; margin: 0 auto; padding: 48px 16px; }
  h1 { font-size: 20px; color: #fca5a5; margin: 0 0 4px; }
  p { color: #a8a29e; margin: 0 0 24px; }
  pre { background: #292524; border: 1px solid #44403c; border-radius: 8px; padding: 16px; overflow-x: auto; white-space: pre-wrap; word-break: break-word; font: 13px/1.6 ui-monospace, monospace; }
</style>
</head>
<body>
<main>
  <h1>{{.Status}} while rendering {{.Path}}</h1>
  <p>This page is shown because the app runs with Reload on. Fix the error and save; the page reloads by itself.</p>
  <pre>{{.Error}}</pre>
</main>
<script src="/_folio/folio.js"></script>
</body>
</html>`))
