package folio

import (
	"errors"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"
	"testing/fstest"
)

func testFS() fstest.MapFS {
	return fstest.MapFS{
		"root.tmpl":                {Data: []byte(`{{define "layout/app"}}<main>{{.Data.HTML}}</main>{{end}}`)},
		"partials/greet.tmpl":      {Data: []byte(`{{define "partial/greet"}}hi {{.}}{{end}}`)},
		"routes/index.tmpl":        {Data: []byte(`{{define "index"}}home {{.Data.PageData.AppName}} {{.Data.PageData.count}}{{end}}`)},
		"routes/about.tmpl":        {Data: []byte(`about {{template "partial/greet" "you"}}`)},
		"routes/tools/index.tmpl":  {Data: []byte(`{{define "tools"}}tools{{end}}`)},
		"routes/docs/intro.tmpl":   {Data: []byte(`intro`)},
		"routes/bare.tmpl":         {Data: []byte(`bare`)},
		"layouts/bare.tmpl":        {Data: []byte(`{{define "layout/bare"}}[{{.Data.HTML}}]{{end}}`)},
		"routes/form.tmpl":         {Data: []byte(`{{.Data.PageData.Message}}{{.Data.PageData.Failure}}|{{.Data.PageData.n}}`)},
		"errors/notfound.tmpl":     {Data: []byte(`{{define "error/404"}}nope {{.Data.Path}}{{end}}`)},
		"routes/broken/index.tmpl": {Data: []byte(`{{template "does-not-exist"}}`)},
	}
}

func newTestApp(t *testing.T) *App {
	t.Helper()
	app, err := New(Config{
		Templates: testFS(),
		Static:    fstest.MapFS{"app.css": {Data: []byte("body{}")}},
		Globals:   map[string]any{"AppName": "Test"},
		Logger:    slog.New(slog.NewTextHandler(io.Discard, nil)),
	})
	if err != nil {
		t.Fatal(err)
	}
	return app
}

func do(app *App, method, target string, form url.Values) (int, string, http.Header) {
	var body io.Reader
	if form != nil {
		body = strings.NewReader(form.Encode())
	}
	req := httptest.NewRequest(method, target, body)
	if form != nil {
		req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	}
	rec := httptest.NewRecorder()
	app.Handler().ServeHTTP(rec, req)
	return rec.Code, rec.Body.String(), rec.Header()
}

func TestRouting(t *testing.T) {
	app := newTestApp(t)
	app.Load("index", func(c *Ctx) error { c.Set("count", 3); return nil })

	tests := []struct {
		path, want string
		code       int
	}{
		{"/", "<main>home Test 3</main>", 200},
		{"/about", "<main>about hi you</main>", 200},
		{"/tools/", "<main>tools</main>", 200},
		{"/docs/intro", "<main>intro</main>", 200},
		{"/bare", "[bare]", 200},
		{"/missing", "nope /missing", 404},
		// Partials and layouts are not pages.
		{"/partial/greet", "nope /partial/greet", 404},
		{"/layout/app", "nope /layout/app", 404},
		{"/root.tmpl", "nope /root.tmpl", 404},
	}
	for _, tt := range tests {
		code, body, _ := do(app, "GET", tt.path, nil)
		if code != tt.code || body != tt.want {
			t.Errorf("GET %s = %d %q, want %d %q", tt.path, code, body, tt.code, tt.want)
		}
	}
}

func TestActionRunsBeforeLoad(t *testing.T) {
	app := newTestApp(t)
	n := 0
	app.Action("form", func(c *Ctx) error {
		if c.Request.PostFormValue("v") == "" {
			c.Fail("required", map[string]string{"v": "required"})
			c.Status(http.StatusUnprocessableEntity)
			return nil
		}
		n++
		c.Success("saved")
		return nil
	})
	app.Load("form", func(c *Ctx) error { c.Set("n", n); return nil })

	if code, body, _ := do(app, "POST", "/form", url.Values{"v": {"x"}}); code != 200 || body != "<main>saved|1</main>" {
		t.Errorf("valid POST = %d %q", code, body)
	}
	if code, body, _ := do(app, "POST", "/form", url.Values{}); code != 422 || body != "<main>required|1</main>" {
		t.Errorf("invalid POST = %d %q", code, body)
	}
}

func TestMethods(t *testing.T) {
	app := newTestApp(t)
	if code, _, h := do(app, "POST", "/about", url.Values{}); code != 405 || h.Get("Allow") != "GET, HEAD" {
		t.Errorf("POST without action = %d allow=%q", code, h.Get("Allow"))
	}
	if code, _, _ := do(app, "DELETE", "/", nil); code != 405 {
		t.Errorf("DELETE = %d", code)
	}
}

func TestHookTakesOverResponse(t *testing.T) {
	app := newTestApp(t)
	loaded := false
	app.Action("form", func(c *Ctx) error {
		c.Redirect("/", http.StatusSeeOther)
		return nil
	})
	app.Load("form", func(c *Ctx) error { loaded = true; return nil })

	code, _, h := do(app, "POST", "/form", url.Values{"v": {"x"}})
	if code != 303 || h.Get("Location") != "/" || loaded {
		t.Errorf("redirect = %d %q loaded=%v", code, h.Get("Location"), loaded)
	}
}

func TestErrors(t *testing.T) {
	app := newTestApp(t)
	app.Load("about", func(c *Ctx) error { return errors.New("db down") })
	if code, _, _ := do(app, "GET", "/about", nil); code != 500 {
		t.Errorf("hook error = %d", code)
	}
	if code, _, _ := do(app, "GET", "/broken", nil); code != 500 {
		t.Errorf("template error = %d", code)
	}
}

func TestStaticAndClient(t *testing.T) {
	app := newTestApp(t)
	if code, body, _ := do(app, "GET", "/static/app.css", nil); code != 200 || body != "body{}" {
		t.Errorf("static = %d %q", code, body)
	}
	if code, body, _ := do(app, "GET", "/_folio/folio.js", nil); code != 200 || !strings.Contains(body, "window.folio") {
		t.Errorf("client = %d", code)
	}
}

func TestParseErrors(t *testing.T) {
	_, err := New(Config{Templates: fstest.MapFS{
		"routes/a.tmpl":       {Data: []byte("a")},
		"routes/a/index.tmpl": {Data: []byte("a")},
	}})
	if err == nil || !strings.Contains(err.Error(), "both map to route") {
		t.Errorf("duplicate route err = %v", err)
	}
	_, err = New(Config{Templates: fstest.MapFS{"routes/a.tmpl": {Data: []byte("{{define \"other\"}}x{{end}}")}}})
	if err == nil || !strings.Contains(err.Error(), "is empty") {
		t.Errorf("empty route err = %v", err)
	}
}

func TestRouteName(t *testing.T) {
	for in, want := range map[string]string{
		"routes/index.tmpl":       "index",
		"routes/about.tmpl":       "about",
		"routes/tools/index.tmpl": "tools",
		"routes/a/b/index.tmpl":   "a/b",
	} {
		if got, ok := routeName(in); !ok || got != want {
			t.Errorf("routeName(%q) = %q, want %q", in, got, want)
		}
	}
	if _, ok := routeName("partials/x.tmpl"); ok {
		t.Error("partial treated as route")
	}
}
