// Package foliotest drives a folio app (or any http.Handler) in tests,
// keeping cookies between requests like a browser.
//
//	func TestHome(t *testing.T) {
//		c := foliotest.New(t, app.Handler())
//		c.Get("/").ExpectStatus(200).ExpectContains("Welcome")
//		c.PostForm("/", url.Values{"name": {"Ada"}}).ExpectRedirect("/").Follow().ExpectContains("Saved")
//	}
package foliotest

import (
	"io"
	"net/http"
	"net/http/cookiejar"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"
)

// BaseURL is the origin requests are made against.
const BaseURL = "http://example.com"

// Client sends requests straight to a handler, without a network.
type Client struct {
	t   testing.TB
	h   http.Handler
	jar *cookiejar.Jar
	// Header is added to every request.
	Header http.Header
}

// New returns a Client for h.
func New(t testing.TB, h http.Handler) *Client {
	jar, _ := cookiejar.New(nil)
	return &Client{t: t, h: h, jar: jar, Header: http.Header{}}
}

// Get requests target, e.g. "/journal/42?tab=notes".
func (c *Client) Get(target string) *Response {
	c.t.Helper()
	return c.Do(httptest.NewRequest(http.MethodGet, BaseURL+target, nil))
}

// PostForm submits form values to target as a browser form would.
func (c *Client) PostForm(target string, form url.Values) *Response {
	c.t.Helper()
	req := httptest.NewRequest(http.MethodPost, BaseURL+target, strings.NewReader(form.Encode()))
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	return c.Do(req)
}

// PostJSON sends body (already-encoded JSON) to target.
func (c *Client) PostJSON(target, body string) *Response {
	c.t.Helper()
	req := httptest.NewRequest(http.MethodPost, BaseURL+target, strings.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Accept", "application/json")
	return c.Do(req)
}

// Do sends req with the client's cookies and headers. Redirects aren't
// followed; see Response.Follow.
func (c *Client) Do(req *http.Request) *Response {
	c.t.Helper()
	for k, v := range c.Header {
		req.Header[k] = v
	}
	for _, cookie := range c.jar.Cookies(req.URL) {
		req.AddCookie(cookie)
	}
	rec := httptest.NewRecorder()
	c.h.ServeHTTP(rec, req)
	res := rec.Result()
	c.jar.SetCookies(req.URL, res.Cookies())
	body, _ := io.ReadAll(res.Body)
	return &Response{c: c, Code: res.StatusCode, Header: res.Header, Body: string(body)}
}

// Cookie returns the value of a cookie the client holds, or "".
func (c *Client) Cookie(name string) string {
	u, _ := url.Parse(BaseURL)
	for _, cookie := range c.jar.Cookies(u) {
		if cookie.Name == name {
			return cookie.Value
		}
	}
	return ""
}

// Response is a recorded response with chainable assertions.
type Response struct {
	c      *Client
	Code   int
	Header http.Header
	Body   string
}

// ExpectStatus fails the test unless the status is code.
func (r *Response) ExpectStatus(code int) *Response {
	r.c.t.Helper()
	if r.Code != code {
		r.c.t.Errorf("status = %d, want %d\nbody: %s", r.Code, code, truncate(r.Body))
	}
	return r
}

// ExpectContains fails the test unless the body contains s.
func (r *Response) ExpectContains(s string) *Response {
	r.c.t.Helper()
	if !strings.Contains(r.Body, s) {
		r.c.t.Errorf("body does not contain %q\nbody: %s", s, truncate(r.Body))
	}
	return r
}

// ExpectNotContains fails the test if the body contains s.
func (r *Response) ExpectNotContains(s string) *Response {
	r.c.t.Helper()
	if strings.Contains(r.Body, s) {
		r.c.t.Errorf("body contains %q\nbody: %s", s, truncate(r.Body))
	}
	return r
}

// ExpectRedirect fails the test unless the response redirects to location.
func (r *Response) ExpectRedirect(location string) *Response {
	r.c.t.Helper()
	if r.Code < 300 || r.Code > 399 || r.Header.Get("Location") != location {
		r.c.t.Errorf("got %d to %q, want a redirect to %q", r.Code, r.Header.Get("Location"), location)
	}
	return r
}

// Follow requests the redirect target.
func (r *Response) Follow() *Response {
	r.c.t.Helper()
	loc := r.Header.Get("Location")
	if loc == "" {
		r.c.t.Fatalf("Follow: response %d has no Location header", r.Code)
	}
	if u, err := url.Parse(loc); err == nil && u.IsAbs() {
		loc = u.RequestURI()
	}
	return r.c.Get(loc)
}

func truncate(s string) string {
	if len(s) > 2000 {
		return s[:2000] + "…"
	}
	return s
}
