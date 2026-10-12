package folio

import (
	"log/slog"
	"net/http"
)

// Ctx is the per-request state passed to Load and Action functions.
type Ctx struct {
	Request *http.Request
	// Route is the matched route name, e.g. "index" or "journal/[id]".
	Route string
	// Page is the page data, available to templates as .Page.
	Page   map[string]any
	Logger *slog.Logger

	view    *View
	w       http.ResponseWriter
	written bool
}

// Param returns a [param] value from the URL, e.g. c.Param("id") for
// routes/journal/[id].tmpl.
func (c *Ctx) Param(name string) string { return c.view.Params[name] }

// Set stores a value for the template.
func (c *Ctx) Set(key string, value any) { c.Page[key] = value }

// Get returns a stored value.
func (c *Ctx) Get(key string) any { return c.Page[key] }

// SetModel passes a typed value to the template as .Model. Unlike .Page
// keys, a misspelled field on a struct is an error when the template runs.
func (c *Ctx) SetModel(v any) { c.view.Model = v }

// Status sets the HTTP status the page is rendered with.
func (c *Ctx) Status(code int) { c.view.Status = code }

// Header gives access to the response headers (cookies, caching, ...).
func (c *Ctx) Header() http.Header { return c.w.Header() }

// Success sets the page's Message.
func (c *Ctx) Success(message string) { c.Page["Message"] = message }

// Fail sets the page's Failure message and, optionally, per-field errors.
func (c *Ctx) Fail(message string, fieldErrors map[string]string) {
	c.Page["Failure"] = message
	if fieldErrors != nil {
		c.Page["Errors"] = fieldErrors
	}
}

// Session returns the request's session.
func (c *Ctx) Session() *Session { return GetSession(c.Request) }

// Flash stores a message that the next rendered page receives as .Flash.
// Pair it with Redirect after a successful form post.
func (c *Ctx) Flash(message string) { c.Session().Set(flashKey, message) }

// Redirect sends a redirect and skips rendering. Use http.StatusSeeOther after
// a successful form post to avoid resubmission on refresh.
func (c *Ctx) Redirect(url string, code int) {
	http.Redirect(c.Writer(), c.Request, url, code)
}

// JSON writes v as a JSON response and skips rendering.
func (c *Ctx) JSON(status int, v any) error { return JSON(c.Writer(), status, v) }

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
