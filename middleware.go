package folio

import (
	"compress/gzip"
	"fmt"
	"log/slog"
	"mime"
	"net/http"
	"net/url"
	"runtime/debug"
	"strings"
	"sync"
	"time"
)

// RequestLogger logs each request's method, path, status and duration.
func RequestLogger(logger *slog.Logger) Middleware {
	return func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			start := time.Now()
			rec := &statusRecorder{ResponseWriter: w, status: http.StatusOK}
			next.ServeHTTP(rec, r)
			logger.Info("request",
				"method", r.Method,
				"path", r.URL.Path,
				"status", rec.status,
				"duration", time.Since(start).String(),
			)
		})
	}
}

// Recoverer turns a panic in a handler into a 500 response instead of a
// dropped connection.
func Recoverer(logger *slog.Logger) Middleware {
	return func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			defer func() {
				if v := recover(); v != nil {
					if v == http.ErrAbortHandler {
						panic(v)
					}
					logger.Error("panic", "error", fmt.Sprint(v), "path", r.URL.Path, "stack", string(debug.Stack()))
					w.Header().Set("Connection", "close")
					http.Error(w, http.StatusText(http.StatusInternalServerError), http.StatusInternalServerError)
				}
			}()
			next.ServeHTTP(w, r)
		})
	}
}

type statusRecorder struct {
	http.ResponseWriter
	status int
}

func (r *statusRecorder) WriteHeader(code int) {
	r.status = code
	r.ResponseWriter.WriteHeader(code)
}

func (r *statusRecorder) Unwrap() http.ResponseWriter { return r.ResponseWriter }

// LoginConfig configures RequireLogin.
type LoginConfig struct {
	// SessionKey is the session value that marks a signed-in user, e.g.
	// "user_id". Login sets it; logout clears the session.
	SessionKey string
	// LoginURL is where signed-out visitors are sent. The page they wanted
	// is passed as ?next=, ready for SafeNext. Default: "/login".
	LoginURL string
	// Public paths don't need a login. An entry ending in "/" covers
	// everything under it. LoginURL, the static prefix and folio's own
	// files are always public.
	Public []string
}

// RequireLogin sends visitors without cfg.SessionKey in their session to the
// login page; requests that want JSON get 401 instead. Use it with Use for
// a mostly-private app, or with Group to protect one section.
func (a *App) RequireLogin(cfg LoginConfig) Middleware {
	if cfg.LoginURL == "" {
		cfg.LoginURL = "/login"
	}
	public := append([]string{cfg.LoginURL, a.cfg.StaticPrefix, ClientPrefix}, cfg.Public...)
	isPublic := func(p string) bool {
		for _, pub := range public {
			if p == pub || p == strings.TrimSuffix(pub, "/") || (strings.HasSuffix(pub, "/") && strings.HasPrefix(p, pub)) {
				return true
			}
		}
		return false
	}
	return func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			if isPublic(r.URL.Path) || GetSession(r).Get(cfg.SessionKey) != "" {
				next.ServeHTTP(w, r)
				return
			}
			if WantsJSON(r) {
				JSON(w, http.StatusUnauthorized, map[string]string{"error": "login required"})
				return
			}
			target := cfg.LoginURL + "?next=" + url.QueryEscape(r.URL.RequestURI())
			http.Redirect(w, r, target, http.StatusSeeOther)
		})
	}
}

// Compress gzips text responses (HTML, CSS, JS, JSON, SVG, WebAssembly) for
// clients that accept it.
func Compress() Middleware {
	return func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			if r.Method == http.MethodHead || r.Header.Get("Range") != "" || !acceptsGzip(r.Header.Get("Accept-Encoding")) {
				next.ServeHTTP(w, r)
				return
			}
			w.Header().Add("Vary", "Accept-Encoding")
			cw := &compressWriter{ResponseWriter: w}
			defer cw.close()
			next.ServeHTTP(cw, r)
		})
	}
}

func acceptsGzip(header string) bool {
	for _, part := range strings.Split(header, ",") {
		name, params, _ := strings.Cut(strings.TrimSpace(part), ";")
		if name != "gzip" && name != "*" {
			continue
		}
		q := strings.ReplaceAll(params, " ", "")
		return q != "q=0" && q != "q=0.0" && q != "q=0.00" && q != "q=0.000"
	}
	return false
}

func compressible(contentType string) bool {
	mt, _, _ := mime.ParseMediaType(contentType)
	switch {
	case mt == "text/event-stream":
		return false
	case strings.HasPrefix(mt, "text/"):
		return true
	}
	switch mt {
	case "application/json", "application/javascript", "application/xml", "image/svg+xml", "application/wasm", "application/manifest+json":
		return true
	}
	return false
}

var gzipPool = sync.Pool{New: func() any {
	gz, _ := gzip.NewWriterLevel(nil, gzip.DefaultCompression)
	return gz
}}

type compressWriter struct {
	http.ResponseWriter
	gz          *gzip.Writer
	wroteHeader bool
}

func (cw *compressWriter) WriteHeader(code int) {
	if cw.wroteHeader {
		return
	}
	cw.wroteHeader = true
	h := cw.Header()
	if code != http.StatusNoContent && code != http.StatusNotModified && code >= 200 &&
		h.Get("Content-Encoding") == "" && compressible(h.Get("Content-Type")) {
		h.Del("Content-Length")
		h.Del("Accept-Ranges")
		h.Set("Content-Encoding", "gzip")
		if etag := h.Get("ETag"); etag != "" && !strings.HasPrefix(etag, "W/") {
			h.Set("ETag", "W/"+etag)
		}
		cw.gz = gzipPool.Get().(*gzip.Writer)
		cw.gz.Reset(cw.ResponseWriter)
	}
	cw.ResponseWriter.WriteHeader(code)
}

func (cw *compressWriter) Write(b []byte) (int, error) {
	if !cw.wroteHeader {
		if cw.Header().Get("Content-Type") == "" {
			cw.Header().Set("Content-Type", http.DetectContentType(b))
		}
		cw.WriteHeader(http.StatusOK)
	}
	if cw.gz != nil {
		return cw.gz.Write(b)
	}
	return cw.ResponseWriter.Write(b)
}

func (cw *compressWriter) Flush() {
	if cw.gz != nil {
		cw.gz.Flush()
	}
	http.NewResponseController(cw.ResponseWriter).Flush()
}

func (cw *compressWriter) Unwrap() http.ResponseWriter { return cw.ResponseWriter }

func (cw *compressWriter) close() {
	if cw.gz != nil {
		cw.gz.Close()
		gzipPool.Put(cw.gz)
		cw.gz = nil
	}
}
