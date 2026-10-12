// Package folio is a small server-rendered web framework built on net/http and
// html/template.
//
// Pages are files under the templates directory's routes/ folder. Every other
// .tmpl file (layouts, partials) is shared but never served directly:
//
//	templates/
//	  root.tmpl                 {{define "layout/app"}} ... {{.HTML}} ... {{end}}
//	  partials/header.tmpl      {{define "header"}} ... {{end}}
//	  routes/index.tmpl         -> /
//	  routes/about.tmpl         -> /about
//	  routes/tools/index.tmpl   -> /tools
//	  routes/journal/[id].tmpl  -> /journal/42 (c.Param("id") == "42")
//
// Go code attaches behaviour to routes with Load (runs on every request to the
// route) and Action (runs on POST, before Load).
package folio

import (
	"context"
	"errors"
	"fmt"
	"html/template"
	"io/fs"
	"log/slog"
	"net/http"
	"os"
	"os/signal"
	"slices"
	"strings"
	"sync"
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

	// Layout is the outermost layout (the "shell") wrapped around every page
	// that doesn't have a shell/<prefix> template of its own. If it isn't
	// defined, pages are written without one. Default: "layout/app".
	Layout string

	// Funcs is made available to every template, alongside the built-in
	// "asset" function.
	Funcs template.FuncMap

	// Globals are copied into every page's data before hooks run.
	Globals map[string]any

	// Reload turns on development mode: templates are re-parsed on every
	// request, the browser reloads when templates or static files change,
	// and 500 pages show the error.
	Reload bool

	// StrictTemplates makes a template fail when it reads a page-data key
	// that was never set, instead of printing nothing. Use {{index .Page
	// "key"}} for keys that are legitimately optional.
	StrictTemplates bool

	// SecretKey encrypts and signs the session cookie. Use at least 32
	// random bytes and keep it stable across restarts and instances. If it's
	// empty a random key is generated, so sessions end when the process
	// restarts.
	SecretKey []byte

	// SessionStore keeps sessions on the server, with only a random ID in
	// the cookie, so they can be ended server-side. Default: nil, meaning
	// sessions live in an encrypted cookie (see Session).
	SessionStore SessionStore

	// SessionCookie is the session cookie's name. Default: "folio_session".
	SessionCookie string

	// SessionMaxAge is how long a session lasts. Default: 30 days.
	SessionMaxAge time.Duration

	// TrustedOrigins may make cross-origin POST requests, e.g.
	// "https://admin.example.com". Same-origin requests are always allowed.
	TrustedOrigins []string

	// DisableCSRF turns off cross-origin request protection.
	DisableCSRF bool

	// MaxFormMemory caps the memory used to parse a multipart form; larger
	// files are stored on disk. Default: 32 MB.
	MaxFormMemory int64

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

type group struct {
	prefix string
	mw     []Middleware
}

// App is a folio application. Create one with New.
type App struct {
	cfg        Config
	logger     *slog.Logger
	site       atomic.Pointer[site]
	hooks      map[string]*routeHooks
	middleware []Middleware
	groups     []group
	mux        *http.ServeMux
	sessions   *sessionManager
	loadAll    []LoadFunc
	client     clientAsset
	assets     sync.Map // static file name -> hash
	live       liveReload

	checkHooks sync.Once
	closing    context.Context
	close      context.CancelFunc
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
	cfg.StaticPrefix = "/" + strings.Trim(cfg.StaticPrefix, "/") + "/"
	if cfg.Layout == "" {
		cfg.Layout = "layout/app"
	}
	if cfg.Logger == nil {
		cfg.Logger = slog.New(slog.NewTextHandler(os.Stdout, nil))
	}
	if cfg.SessionCookie == "" {
		cfg.SessionCookie = "folio_session"
	}
	if cfg.SessionMaxAge == 0 {
		cfg.SessionMaxAge = 30 * 24 * time.Hour
	}
	if cfg.MaxFormMemory == 0 {
		cfg.MaxFormMemory = 32 << 20
	}
	for _, origin := range cfg.TrustedOrigins {
		if err := http.NewCrossOriginProtection().AddTrustedOrigin(origin); err != nil {
			return nil, fmt.Errorf("folio: trusted origin: %w", err)
		}
	}

	app := &App{
		cfg:    cfg,
		logger: cfg.Logger,
		hooks:  map[string]*routeHooks{},
		mux:    http.NewServeMux(),
	}
	app.closing, app.close = context.WithCancel(context.Background())

	var err error
	if app.sessions, err = newSessionManager(cfg, app.logger); err != nil {
		return nil, err
	}
	app.client = newClientAsset(cfg.Reload)

	s, err := app.parseSite()
	if err != nil {
		return nil, err
	}
	app.site.Store(s)

	app.mux.Handle(cfg.StaticPrefix, http.StripPrefix(cfg.StaticPrefix, app.staticHandler()))
	app.mux.Handle(ClientPrefix+"folio.js", app.client)
	if cfg.Reload {
		app.mux.HandleFunc(ClientPrefix+"live", app.liveHandler)
	}
	app.mux.HandleFunc("/", app.servePage)
	return app, nil
}

// Load registers a function that runs on every GET, HEAD and POST request to
// route, in registration order. Route names are the file path under routes/
// without the extension or a trailing /index: "index" for the root page,
// "tools", "journal/[id]".
func (a *App) Load(route string, fn LoadFunc) {
	h := a.hooksFor(route)
	h.load = append(h.load, fn)
}

// LoadAll registers a function that runs before the route's own hooks on
// every page, e.g. to load the signed-in user for the header. It may
// redirect, which skips the page.
func (a *App) LoadAll(fn LoadFunc) {
	a.loadAll = append(a.loadAll, fn)
}

// Action registers the POST handler for route. It runs before the route's
// Load functions, so the page reflects whatever the action changed. A route
// without an action answers POST with 405.
func (a *App) Action(route string, fn LoadFunc) {
	a.hooksFor(route).action = fn
}

// Use appends middleware for every request. The first one added is the
// outermost.
func (a *App) Use(mw ...Middleware) {
	a.middleware = append(a.middleware, mw...)
}

// Group adds middleware that only runs for URLs under prefix, e.g.
// app.Group("/admin", requireAdmin) covers /admin and /admin/users but not
// /administrator. Groups run inside the app-wide middleware, in the order
// they were added.
func (a *App) Group(prefix string, mw ...Middleware) {
	prefix = "/" + strings.Trim(prefix, "/")
	if prefix == "/" {
		a.Use(mw...)
		return
	}
	a.groups = append(a.groups, group{prefix: prefix, mw: mw})
}

// Handle registers a plain http.Handler for a pattern, for things that aren't
// pages (JSON endpoints, health checks, ...). Patterns follow http.ServeMux.
func (a *App) Handle(pattern string, h http.Handler) {
	a.mux.Handle(pattern, h)
}

// HandleFunc is Handle for a function.
func (a *App) HandleFunc(pattern string, h func(http.ResponseWriter, *http.Request)) {
	a.mux.HandleFunc(pattern, h)
}

// Routes lists the page routes found in the templates, sorted.
func (a *App) Routes() []string {
	s := a.site.Load()
	routes := make([]string, 0, len(s.pages))
	for r := range s.pages {
		routes = append(routes, r)
	}
	slices.Sort(routes)
	return routes
}

// Handler returns the app wrapped in its middleware. Middleware and groups
// added after the call don't apply to the returned handler.
func (a *App) Handler() http.Handler {
	a.checkHooks.Do(a.warnUnknownHooks)

	var h http.Handler = a.mux
	if len(a.groups) > 0 {
		h = groupHandler(slices.Clone(a.groups), h)
	}
	if !a.cfg.DisableCSRF {
		cop := http.NewCrossOriginProtection()
		for _, origin := range a.cfg.TrustedOrigins {
			cop.AddTrustedOrigin(origin) // validated in New
		}
		h = cop.Handler(h)
	}
	for i := len(a.middleware) - 1; i >= 0; i-- {
		h = a.middleware[i](h)
	}
	return a.sessions.middleware(h)
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
	// Long-lived live-reload streams would otherwise hold up shutdown.
	srv.RegisterOnShutdown(a.close)

	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()

	errCh := make(chan error, 1)
	go func() { errCh <- srv.ListenAndServe() }()
	url := "http://" + addr
	if strings.HasPrefix(addr, ":") {
		url = "http://localhost" + addr
	}
	a.logger.Info("listening", "url", url, "dev", a.cfg.Reload)

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

// warnUnknownHooks catches typos like app.Load("tool", ...) for routes/tools.
func (a *App) warnUnknownHooks() {
	s := a.site.Load()
	for route := range a.hooks {
		if _, ok := s.pages[route]; !ok {
			a.logger.Warn("hooks registered for a route with no template", "route", route, "routes", a.Routes())
		}
	}
}

// currentSite returns the parsed templates, re-parsing first when Reload is on.
func (a *App) currentSite() (*site, error) {
	if !a.cfg.Reload {
		return a.site.Load(), nil
	}
	s, err := a.parseSite()
	if err != nil {
		return nil, err
	}
	a.site.Store(s)
	return s, nil
}

func groupHandler(groups []group, next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		h := next
		for i := len(groups) - 1; i >= 0; i-- {
			g := groups[i]
			if r.URL.Path != g.prefix && !strings.HasPrefix(r.URL.Path, g.prefix+"/") {
				continue
			}
			for j := len(g.mw) - 1; j >= 0; j-- {
				h = g.mw[j](h)
			}
		}
		h.ServeHTTP(w, r)
	})
}
