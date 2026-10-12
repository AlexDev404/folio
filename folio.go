// Package folio is a small server-rendered web framework built on net/http and
// html/template.
//
// Pages are files under the templates directory's routes/ folder. Every other
// .tmpl file (layouts, partials) is shared but never served directly:
//
//	templates/
//	  root.tmpl              {{define "layout/app"}} ... {{.Data.HTML}} ... {{end}}
//	  partials/header.tmpl   {{define "header"}} ... {{end}}
//	  routes/index.tmpl      -> /
//	  routes/about.tmpl      -> /about
//	  routes/tools/index.tmpl-> /tools
//
// Go code attaches behaviour to routes with Load (runs on every request to the
// route) and Action (runs on POST, before Load).
package folio

import (
	"context"
	"errors"
	"html/template"
	"io/fs"
	"log/slog"
	"net/http"
	"os"
	"os/signal"
	"strings"
	"sync/atomic"
	"syscall"
	"time"
)

// Config configures an App. The zero value is usable: templates are read from
// ./templates and static files from ./static, relative to the working directory.
type Config struct {
	// Templates holds the template tree (see the package docs). Pass an
	// embed.FS sub-tree to ship templates inside the binary.
	// Default: os.DirFS("templates").
	Templates fs.FS

	// Static is served under StaticPrefix. Default: os.DirFS("static").
	Static fs.FS

	// StaticPrefix is the URL prefix for Static. Default: "/static/".
	StaticPrefix string

	// Layout is the template wrapped around every page that has no
	// "layout/<route>" template of its own. If it isn't defined, pages are
	// written without a layout. Default: "layout/app".
	Layout string

	// Funcs is made available to every template.
	Funcs template.FuncMap

	// Globals are copied into every page's data before hooks run.
	Globals map[string]any

	// Reload re-parses templates on every request. Use it in development.
	Reload bool

	// Logger defaults to a text logger on stdout.
	Logger *slog.Logger
}

// LoadFunc fills in page data for a route. Returning an error renders the 500 page.
type LoadFunc func(c *Ctx) error

// Middleware wraps an http.Handler.
type Middleware func(http.Handler) http.Handler

type routeHooks struct {
	load   []LoadFunc
	action LoadFunc
}

// App is a folio application. Create one with New.
type App struct {
	cfg        Config
	logger     *slog.Logger
	site       atomic.Pointer[site]
	hooks      map[string]*routeHooks
	middleware []Middleware
	mux        *http.ServeMux
}

// New parses the templates and returns an App ready for hooks to be registered.
func New(cfg Config) (*App, error) {
	if cfg.Templates == nil {
		cfg.Templates = os.DirFS("templates")
	}
	if cfg.Static == nil {
		cfg.Static = os.DirFS("static")
	}
	if cfg.StaticPrefix == "" {
		cfg.StaticPrefix = "/static/"
	}
	if !strings.HasSuffix(cfg.StaticPrefix, "/") {
		cfg.StaticPrefix += "/"
	}
	if cfg.Layout == "" {
		cfg.Layout = "layout/app"
	}
	if cfg.Logger == nil {
		cfg.Logger = slog.New(slog.NewTextHandler(os.Stdout, nil))
	}

	app := &App{
		cfg:    cfg,
		logger: cfg.Logger,
		hooks:  map[string]*routeHooks{},
		mux:    http.NewServeMux(),
	}

	s, err := parseSite(cfg)
	if err != nil {
		return nil, err
	}
	app.site.Store(s)

	app.mux.Handle(cfg.StaticPrefix, http.StripPrefix(cfg.StaticPrefix, http.FileServerFS(cfg.Static)))
	app.mux.Handle(ClientPrefix, http.StripPrefix(ClientPrefix, clientHandler()))
	app.mux.HandleFunc("/", app.servePage)
	return app, nil
}

// Load registers a function that runs on every GET, HEAD and POST request to
// route, in registration order. Route names are paths without slashes at
// either end; the root page is "index".
func (a *App) Load(route string, fn LoadFunc) {
	h := a.hooksFor(route)
	h.load = append(h.load, fn)
}

// Action registers the POST handler for route. It runs before the route's
// Load functions, so the page reflects whatever the action changed. A route
// without an action answers POST with 405.
func (a *App) Action(route string, fn LoadFunc) {
	a.hooksFor(route).action = fn
}

// Use appends middleware. The first one added is the outermost.
func (a *App) Use(mw ...Middleware) {
	a.middleware = append(a.middleware, mw...)
}

// Handle registers a plain http.Handler for a pattern, for things that aren't
// pages (JSON endpoints, health checks, ...). Patterns follow http.ServeMux.
func (a *App) Handle(pattern string, h http.Handler) {
	a.mux.Handle(pattern, h)
}

// Routes lists the page routes found in the templates.
func (a *App) Routes() []string {
	s := a.site.Load()
	routes := make([]string, 0, len(s.routes))
	for r := range s.routes {
		routes = append(routes, r)
	}
	return routes
}

// Handler returns the app wrapped in its middleware.
func (a *App) Handler() http.Handler {
	var h http.Handler = a.mux
	for i := len(a.middleware) - 1; i >= 0; i-- {
		h = a.middleware[i](h)
	}
	return h
}

// ListenAndServe serves the app on addr and shuts down gracefully on
// SIGINT/SIGTERM.
func (a *App) ListenAndServe(addr string) error {
	srv := &http.Server{
		Addr:              addr,
		Handler:           a.Handler(),
		ReadHeaderTimeout: 10 * time.Second,
		IdleTimeout:       time.Minute,
		ErrorLog:          slog.NewLogLogger(a.logger.Handler(), slog.LevelError),
	}

	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()

	errCh := make(chan error, 1)
	go func() { errCh <- srv.ListenAndServe() }()
	url := "http://" + addr
	if strings.HasPrefix(addr, ":") {
		url = "http://localhost" + addr
	}
	a.logger.Info("listening", "url", url)

	select {
	case err := <-errCh:
		return err
	case <-ctx.Done():
	}

	a.logger.Info("shutting down")
	shutdownCtx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	if err := srv.Shutdown(shutdownCtx); err != nil {
		return err
	}
	if err := <-errCh; !errors.Is(err, http.ErrServerClosed) {
		return err
	}
	return nil
}

func (a *App) hooksFor(route string) *routeHooks {
	route = strings.Trim(route, "/")
	if route == "" {
		route = "index"
	}
	h, ok := a.hooks[route]
	if !ok {
		h = &routeHooks{}
		a.hooks[route] = h
	}
	return h
}

// currentSite returns the parsed templates, re-parsing first when Reload is on.
func (a *App) currentSite() (*site, error) {
	if !a.cfg.Reload {
		return a.site.Load(), nil
	}
	s, err := parseSite(a.cfg)
	if err != nil {
		return nil, err
	}
	a.site.Store(s)
	return s, nil
}
