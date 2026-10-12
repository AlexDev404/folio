package folio

import (
	"bytes"
	"html/template"
	"io/fs"
	"maps"
	"net/http"
	"strconv"
	"sync"
)

// TemplateData is what every template receives as its dot. Pages see
// .Data.Path and .Data.PageData; layouts additionally get .Data.HTML, the
// rendered page.
type TemplateData struct {
	Data map[string]any
}

var bufferPool = sync.Pool{New: func() any { return new(bytes.Buffer) }}

func getBuffer() *bytes.Buffer {
	b := bufferPool.Get().(*bytes.Buffer)
	b.Reset()
	return b
}

func (a *App) servePage(w http.ResponseWriter, r *http.Request) {
	s, err := a.currentSite()
	if err != nil {
		a.logger.Error("parsing templates", "error", err)
		a.serverError(w, r, nil)
		return
	}

	route := routeForPath(r.URL.Path)
	page, ok := s.routes[route]
	if !ok {
		a.notFound(w, r, s)
		return
	}
	hooks := a.hooks[route]

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
		"Path":    route,
		"Errors":  map[string]string{},
		"Message": nil,
		"Failure": nil,
	}
	maps.Copy(data, a.cfg.Globals)

	c := &Ctx{
		Request: r,
		Route:   route,
		Data:    data,
		Logger:  a.logger.With("route", route),
		w:       w,
		status:  http.StatusOK,
	}

	if hooks != nil {
		if r.Method == http.MethodPost {
			if err := r.ParseForm(); err != nil {
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

	a.render(w, r, s, page, c.status, data)
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
	if err != nil {
		c.Logger.Error("hook failed", "error", err)
		a.serverError(c.w, c.Request, s)
		return false
	}
	return true
}

func (a *App) render(w http.ResponseWriter, r *http.Request, s *site, page *template.Template, status int, pageData map[string]any) {
	route := page.Name()
	content := getBuffer()
	defer bufferPool.Put(content)

	data := map[string]any{"Path": route, "PageData": pageData}
	if err := page.Execute(content, TemplateData{Data: data}); err != nil {
		a.logger.Error("rendering page", "route", route, "error", err)
		a.serverError(w, r, s)
		return
	}

	out := content
	layout := s.tmpl.Lookup("layout/" + route)
	if layout == nil {
		layout = s.tmpl.Lookup(a.cfg.Layout)
	}
	if layout != nil {
		full := getBuffer()
		defer bufferPool.Put(full)
		data["HTML"] = template.HTML(content.String())
		if err := layout.Execute(full, TemplateData{Data: data}); err != nil {
			a.logger.Error("rendering layout", "route", route, "layout", layout.Name(), "error", err)
			a.serverError(w, r, s)
			return
		}
		out = full
	}

	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	w.Header().Set("Content-Length", strconv.Itoa(out.Len()))
	w.WriteHeader(status)
	out.WriteTo(w)
}

func (a *App) methodNotAllowed(w http.ResponseWriter, hooks *routeHooks) {
	allow := "GET, HEAD"
	if hooks != nil && hooks.action != nil {
		allow += ", POST"
	}
	w.Header().Set("Allow", allow)
	http.Error(w, "Method Not Allowed", http.StatusMethodNotAllowed)
}

func (a *App) notFound(w http.ResponseWriter, r *http.Request, s *site) {
	a.errorPage(w, r, s, http.StatusNotFound)
}

func (a *App) serverError(w http.ResponseWriter, r *http.Request, s *site) {
	a.errorPage(w, r, s, http.StatusInternalServerError)
}

// errorPage renders the "error/<code>" template if one is defined, then falls
// back to <static>/errors/<code>.html, then to plain text.
func (a *App) errorPage(w http.ResponseWriter, r *http.Request, s *site, code int) {
	name := strconv.Itoa(code)
	if s != nil {
		if t := s.tmpl.Lookup("error/" + name); t != nil {
			buf := getBuffer()
			defer bufferPool.Put(buf)
			data := TemplateData{Data: map[string]any{"Path": r.URL.Path, "Status": code}}
			if err := t.Execute(buf, data); err == nil {
				w.Header().Set("Content-Type", "text/html; charset=utf-8")
				w.WriteHeader(code)
				buf.WriteTo(w)
				return
			}
		}
	}
	if b, err := fs.ReadFile(a.cfg.Static, "errors/"+name+".html"); err == nil {
		w.Header().Set("Content-Type", "text/html; charset=utf-8")
		w.WriteHeader(code)
		w.Write(b)
		return
	}
	http.Error(w, http.StatusText(code), code)
}
