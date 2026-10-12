package folio

import (
	"log/slog"
	"net/http"
)

// Ctx is the per-request state passed to Load and Action functions.
type Ctx struct {
	Request *http.Request
	// Route is the matched route name, e.g. "index" or "tools".
	Route string
	// Data is the page data, available to templates as .Data.PageData.
	Data   map[string]any
	Logger *slog.Logger

	w       http.ResponseWriter
	status  int
	written bool
}

// Set stores a value for the template.
func (c *Ctx) Set(key string, value any) { c.Data[key] = value }

// Get returns a stored value.
func (c *Ctx) Get(key string) any { return c.Data[key] }

// Status sets the HTTP status the page is rendered with.
func (c *Ctx) Status(code int) { c.status = code }

// Header gives access to the response headers (cookies, caching, ...).
func (c *Ctx) Header() http.Header { return c.w.Header() }

// Success sets the page's Message.
func (c *Ctx) Success(message string) { c.Data["Message"] = message }

// Fail sets the page's Failure message and, optionally, per-field errors.
func (c *Ctx) Fail(message string, fieldErrors map[string]string) {
	c.Data["Failure"] = message
	if fieldErrors != nil {
		c.Data["Errors"] = fieldErrors
	}
}

// Redirect sends a redirect and skips rendering. Use http.StatusSeeOther after
// a successful form post to avoid resubmission on refresh.
func (c *Ctx) Redirect(url string, code int) {
	http.Redirect(c.Writer(), c.Request, url, code)
}

// Writer returns the raw ResponseWriter. Anything written through it takes
// over the response: remaining hooks are skipped and the page isn't rendered.
func (c *Ctx) Writer() http.ResponseWriter { return &ctxWriter{ResponseWriter: c.w, c: c} }

// Done reports whether the response has already been written by a hook.
func (c *Ctx) Done() bool { return c.written }

type ctxWriter struct {
	http.ResponseWriter
	c *Ctx
}

func (w *ctxWriter) WriteHeader(code int) {
	w.c.written = true
	w.ResponseWriter.WriteHeader(code)
}

func (w *ctxWriter) Write(b []byte) (int, error) {
	w.c.written = true
	return w.ResponseWriter.Write(b)
}

func (w *ctxWriter) Unwrap() http.ResponseWriter { return w.ResponseWriter }
