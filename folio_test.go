package folio

import (
	"bytes"
	"compress/gzip"
	"errors"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"
	"testing/fstest"

	"github.com/AlexDev404/folio/foliotest"
)

func testFS() fstest.MapFS {
	return fstest.MapFS{
		"root.tmpl":                   {Data: []byte(`{{define "layout/app"}}<main>{{.HTML}}</main>{{end}}`)},
		"partials/greet.tmpl":         {Data: []byte(`{{define "partial/greet"}}hi {{.}}{{end}}`)},
		"routes/index.tmpl":           {Data: []byte(`{{define "index"}}home {{.Page.AppName}} {{.Page.count}}{{end}}`)},
		"routes/about.tmpl":           {Data: []byte(`about {{template "partial/greet" "you"}}`)},
		"routes/tools/index.tmpl":     {Data: []byte(`{{define "tools"}}tools{{end}}`)},
		"routes/docs/intro.tmpl":      {Data: []byte(`intro`)},
		"layouts/docs.tmpl":           {Data: []byte(`{{define "layout/docs"}}<docs>{{.HTML}}</docs>{{end}}`)},
		"routes/bare.tmpl":            {Data: []byte(`bare`)},
		"layouts/bare.tmpl":           {Data: []byte(`{{define "shell/bare"}}[{{.HTML}}]{{end}}`)},
		"routes/form.tmpl":            {Data: []byte(`{{.Page.Message}}{{.Page.Failure}}|{{.Page.n}}|{{.Flash}}`)},
		"errors/notfound.tmpl":        {Data: []byte(`{{define "error/404"}}nope {{.Path}}{{end}}`)},
		"routes/broken/index.tmpl":    {Data: []byte(`{{template "does-not-exist"}}`)},
		"routes/journal/[id].tmpl":    {Data: []byte(`entry {{.Params.id}}`)},
		"routes/journal/new.tmpl":     {Data: []byte(`new entry`)},
		"routes/files/[...p].tmpl":    {Data: []byte(`file {{.Params.p}}`)},
		"routes/u/[a]/[b]/index.tmpl": {Data: []byte(`{{.Params.a}}-{{.Params.b}}`)},
		"routes/asset.tmpl":           {Data: []byte(`{{asset "app.css"}}`)},
		"routes/model.tmpl":           {Data: []byte(`{{.Model.Name}}`)},
		"routes/titled.tmpl":          {Data: []byte(`titled`)},
	}
}

var quiet = slog.New(slog.NewTextHandler(io.Discard, nil))

func newTestApp(t *testing.T, mod ...func(*Config)) *App {
	t.Helper()
	cfg := Config{
		Templates: testFS(),
		Static:    fstest.MapFS{"app.css": {Data: []byte("body{}")}, "img/x.svg": {Data: []byte("<svg/>")}},
		Globals:   map[string]any{"AppName": "Test"},
		Logger:    quiet,
		SecretKey: bytes.Repeat([]byte("k"), 32),
	}
	for _, m := range mod {
		m(&cfg)
	}
	app, err := New(cfg)
	if err != nil {
		t.Fatal(err)
	}
	return app
}

func TestRouting(t *testing.T) {
	app := newTestApp(t)
	app.Load("index", func(c *Ctx) error { c.Set("count", 3); return nil })
	c := foliotest.New(t, app.Handler())

	tests := []struct {
		path, want string
		code       int
	}{
		{"/", "<main>home Test 3</main>", 200},
		{"/about", "<main>about hi you</main>", 200},
		{"/tools/", "<main>tools</main>", 200},
		{"/docs/intro", "<main><docs>intro</docs></main>", 200},
		{"/bare", "[bare]", 200},
		{"/journal/42", "<main>entry 42</main>", 200},
		{"/journal/new", "<main>new entry</main>", 200},
		{"/journal/a%20b", "<main>entry a b</main>", 200},
		{"/files/a/b/c.txt", "<main>file a/b/c.txt</main>", 200},
		{"/u/x/y", "<main>x-y</main>", 200},
		{"/files", "nope /files", 404},
		{"/journal/1/2", "nope /journal/1/2", 404},
		{"/missing", "nope /missing", 404},
		// Partials, layouts and route names with brackets are not pages.
		{"/partial/greet", "nope /partial/greet", 404},
		{"/layout/app", "nope /layout/app", 404},
		{"/journal/[id]", "<main>entry [id]</main>", 200},
	}
	for _, tt := range tests {
		r := c.Get(tt.path)
		if r.Code != tt.code || r.Body != tt.want {
			t.Errorf("GET %s = %d %q, want %d %q", tt.path, r.Code, r.Body, tt.code, tt.want)
		}
	}
	if got := strings.Join(app.Routes(), ","); !strings.Contains(got, "journal/[id]") {
		t.Errorf("Routes() = %s", got)
	}
}

func TestParams(t *testing.T) {
	app := newTestApp(t)
	var got string
	app.Load("journal/[id]", func(c *Ctx) error { got = c.Param("id"); return nil })
	foliotest.New(t, app.Handler()).Get("/journal/7").ExpectStatus(200)
	if got != "7" {
		t.Errorf("Param = %q", got)
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
	c := foliotest.New(t, app.Handler())

	c.PostForm("/form", url.Values{"v": {"x"}}).ExpectStatus(200).ExpectContains("<main>saved|1|</main>")
	c.PostForm("/form", url.Values{}).ExpectStatus(422).ExpectContains("<main>required|1|</main>")
}

func TestLoadAll(t *testing.T) {
	app := newTestApp(t)
	var order []string
	app.LoadAll(func(c *Ctx) error { order = append(order, "all:"+c.Route); return nil })
	app.Load("about", func(c *Ctx) error { order = append(order, "about"); return nil })
	foliotest.New(t, app.Handler()).Get("/about").ExpectStatus(200)
	if strings.Join(order, ",") != "all:about,about" {
		t.Errorf("order = %v", order)
	}
}

func TestMultipartForm(t *testing.T) {
	app := newTestApp(t)
	app.Action("form", func(c *Ctx) error { c.Success(c.Request.PostFormValue("v")); return nil })

	var body bytes.Buffer
	body.WriteString("--B\r\nContent-Disposition: form-data; name=\"v\"\r\n\r\nmulti\r\n--B--\r\n")
	req := httptest.NewRequest("POST", foliotest.BaseURL+"/form", &body)
	req.Header.Set("Content-Type", "multipart/form-data; boundary=B")
	foliotest.New(t, app.Handler()).Do(req).ExpectStatus(200).ExpectContains("multi|")
}

func TestMethods(t *testing.T) {
	app := newTestApp(t)
	c := foliotest.New(t, app.Handler())
	r := c.PostForm("/about", url.Values{}).ExpectStatus(405)
	if r.Header.Get("Allow") != "GET, HEAD" {
		t.Errorf("Allow = %q", r.Header.Get("Allow"))
	}
	c.Do(httptest.NewRequest("DELETE", foliotest.BaseURL+"/", nil)).ExpectStatus(405)
}

func TestFlashAfterRedirect(t *testing.T) {
	app := newTestApp(t)
	loaded := false
	app.Action("form", func(c *Ctx) error {
		c.Flash("done!")
		c.Redirect("/form", http.StatusSeeOther)
		return nil
	})
	app.Load("form", func(c *Ctx) error { loaded = true; return nil })
	c := foliotest.New(t, app.Handler())

	r := c.PostForm("/form", url.Values{"v": {"x"}}).ExpectRedirect("/form")
	if loaded {
		t.Error("Load ran after the action redirected")
	}
	r.Follow().ExpectContains("|done!</main>")
	c.Get("/form").ExpectNotContains("done!")
}

func TestSessions(t *testing.T) {
	for name, store := range map[string]SessionStore{"cookie": nil, "store": NewMemoryStore()} {
		t.Run(name, func(t *testing.T) {
			app := newTestApp(t, func(c *Config) { c.SessionStore = store })
			app.HandleFunc("/set", func(w http.ResponseWriter, r *http.Request) { GetSession(r).Set("user", r.FormValue("u")) })
			app.HandleFunc("/renew", func(w http.ResponseWriter, r *http.Request) { GetSession(r).Renew() })
			app.HandleFunc("/get", func(w http.ResponseWriter, r *http.Request) { io.WriteString(w, "user="+GetSession(r).Get("user")) })
			app.HandleFunc("/logout", func(w http.ResponseWriter, r *http.Request) { GetSession(r).Clear() })
			c := foliotest.New(t, app.Handler())

			c.Get("/get").ExpectContains("user=").ExpectNotContains("ada")
			c.Get("/set?u=ada").ExpectStatus(200)
			c.Get("/get").ExpectContains("user=ada")

			before := c.Cookie("folio_session")
			c.Get("/renew")
			if after := c.Cookie("folio_session"); after == before {
				t.Error("Renew didn't change the cookie")
			}
			c.Get("/get").ExpectContains("user=ada")

			if store != nil {
				// The old ID must not work after Renew.
				stale := foliotest.New(t, app.Handler())
				req := httptest.NewRequest("GET", foliotest.BaseURL+"/get", nil)
				req.AddCookie(&http.Cookie{Name: "folio_session", Value: before})
				stale.Do(req).ExpectNotContains("ada")
			}

			// A copy of the cookie stops working after logout only with a store.
			stolen := c.Cookie("folio_session")
			c.Get("/logout")
			c.Get("/get").ExpectNotContains("ada")
			req := httptest.NewRequest("GET", foliotest.BaseURL+"/get", nil)
			req.AddCookie(&http.Cookie{Name: "folio_session", Value: stolen})
			r := foliotest.New(t, app.Handler()).Do(req)
			if store != nil {
				r.ExpectNotContains("ada")
			}

			// Tampered cookies are ignored.
			req = httptest.NewRequest("GET", foliotest.BaseURL+"/get", nil)
			req.AddCookie(&http.Cookie{Name: "folio_session", Value: "x" + stolen[1:]})
			foliotest.New(t, app.Handler()).Do(req).ExpectStatus(200).ExpectNotContains("ada")
		})
	}
}

func TestShortSecretRejected(t *testing.T) {
	if _, err := New(Config{Templates: testFS(), SecretKey: []byte("short")}); err == nil {
		t.Error("short SecretKey accepted")
	}
}

func TestCSRF(t *testing.T) {
	app := newTestApp(t, func(c *Config) { c.TrustedOrigins = []string{"https://admin.example.org"} })
	app.Action("form", func(c *Ctx) error { c.Success("ok"); return nil })
	h := app.Handler()

	post := func(headers map[string]string) int {
		req := httptest.NewRequest("POST", "http://example.com/form", strings.NewReader("v=1"))
		req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
		for k, v := range headers {
			req.Header.Set(k, v)
		}
		rec := httptest.NewRecorder()
		h.ServeHTTP(rec, req)
		return rec.Code
	}
	if code := post(map[string]string{"Sec-Fetch-Site": "cross-site", "Origin": "https://evil.example"}); code != 403 {
		t.Errorf("cross-site POST = %d, want 403", code)
	}
	if code := post(map[string]string{"Sec-Fetch-Site": "same-origin"}); code != 200 {
		t.Errorf("same-origin POST = %d", code)
	}
	if code := post(map[string]string{"Sec-Fetch-Site": "cross-site", "Origin": "https://admin.example.org"}); code != 200 {
		t.Errorf("trusted-origin POST = %d", code)
	}
	if code := post(nil); code != 200 {
		t.Errorf("non-browser POST = %d", code)
	}
}

func TestGroupsAndRequireLogin(t *testing.T) {
	app := newTestApp(t)
	var hits []string
	app.Group("/docs", func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			hits = append(hits, r.URL.Path)
			next.ServeHTTP(w, r)
		})
	})
	app.Group("/journal", app.RequireLogin(LoginConfig{SessionKey: "user", LoginURL: "/about"}))
	app.HandleFunc("/login-as", func(w http.ResponseWriter, r *http.Request) { GetSession(r).Set("user", "1") })
	c := foliotest.New(t, app.Handler())

	c.Get("/docs/intro")
	c.Get("/docsx")
	c.Get("/about")
	if strings.Join(hits, ",") != "/docs/intro" {
		t.Errorf("group hits = %v", hits)
	}

	c.Get("/journal/5?x=1").ExpectRedirect("/about?next=%2Fjournal%2F5%3Fx%3D1")
	c.Header.Set("Accept", "application/json")
	c.Get("/journal/5").ExpectStatus(401).ExpectContains("login required")
	c.Header.Del("Accept")
	c.Get("/login-as")
	c.Get("/journal/5").ExpectStatus(200).ExpectContains("entry 5")
}

func TestSafeNext(t *testing.T) {
	for next, want := range map[string]string{
		"/journal/1":         "/journal/1",
		"":                   "/",
		"//evil.com":         "/",
		"/\\evil.com":        "/",
		"https://evil.com":   "/",
		"javascript:alert()": "/",
	} {
		r := httptest.NewRequest("GET", "/login?next="+url.QueryEscape(next), nil)
		if got := SafeNext(r, "/"); got != want {
			t.Errorf("SafeNext(%q) = %q, want %q", next, got, want)
		}
	}
}

func TestPartialNavigation(t *testing.T) {
	app := newTestApp(t)
	app.Load("titled", func(c *Ctx) error { c.Set("Title", "Hello & bye"); return nil })
	c := foliotest.New(t, app.Handler())
	c.Header.Set("X-Folio-Navigate", "1")

	// Unknown shell: full page.
	r := c.Get("/docs/intro").ExpectContains("<main><docs>intro</docs></main>")
	if r.Header.Get("X-Folio-Partial") != "" || r.Header.Get("X-Folio-Shell") != "layout/app" {
		t.Errorf("headers = %v", r.Header)
	}

	// Same shell: inner layouts only.
	c.Header.Set("X-Folio-Shell", "layout/app")
	r = c.Get("/docs/intro")
	if r.Body != "<docs>intro</docs>" || r.Header.Get("X-Folio-Partial") != "1" || r.Header.Get("X-Folio-Route") != "docs/intro" {
		t.Errorf("partial = %q %v", r.Body, r.Header)
	}
	r = c.Get("/titled")
	if r.Header.Get("X-Folio-Title") != url.PathEscape("Hello & bye") {
		t.Errorf("title header = %q", r.Header.Get("X-Folio-Title"))
	}
	if r := c.Get("/about"); r.Header.Get("X-Folio-Title") != "" {
		t.Errorf("untitled page sent title %q", r.Header.Get("X-Folio-Title"))
	}

	// With a folio/title template every navigation sends a title.
	titled := newTestApp(t, func(cfg *Config) {
		cfg.Templates.(fstest.MapFS)["title.tmpl"] = &fstest.MapFile{Data: []byte(`{{define "folio/title"}}{{with .Page.Title}}{{.}} · {{end}}{{.Page.AppName}}{{end}}`)}
	})
	titled.Load("titled", func(c *Ctx) error { c.Set("Title", "Hi"); return nil })
	tc := foliotest.New(t, titled.Handler())
	tc.Header = c.Header
	for path, want := range map[string]string{"/about": "Test", "/titled": "Hi · Test"} {
		if got, _ := url.PathUnescape(tc.Get(path).Header.Get("X-Folio-Title")); got != want {
			t.Errorf("%s title = %q, want %q", path, got, want)
		}
	}

	// Different shell: full page.
	c.Get("/bare").ExpectContains("[bare]")
}

func TestModel(t *testing.T) {
	app := newTestApp(t)
	app.Load("model", func(c *Ctx) error { c.SetModel(struct{ Name string }{"typed"}); return nil })
	foliotest.New(t, app.Handler()).Get("/model").ExpectContains("typed")
}

func TestHookTakesOverResponse(t *testing.T) {
	app := newTestApp(t)
	app.Load("about", func(c *Ctx) error { return c.JSON(201, map[string]int{"n": 1}) })
	r := foliotest.New(t, app.Handler()).Get("/about").ExpectStatus(201).ExpectContains(`{"n":1}`)
	if r.Header.Get("Content-Type") != "application/json; charset=utf-8" {
		t.Errorf("content type = %q", r.Header.Get("Content-Type"))
	}
}

func TestErrors(t *testing.T) {
	app := newTestApp(t)
	app.Load("about", func(c *Ctx) error { return errors.New("db down") })
	c := foliotest.New(t, app.Handler())
	c.Get("/about").ExpectStatus(500).ExpectNotContains("db down")
	c.Get("/broken").ExpectStatus(500)

	dev := newTestApp(t, func(c *Config) { c.Reload = true })
	dev.Load("about", func(c *Ctx) error { return errors.New("db down") })
	foliotest.New(t, dev.Handler()).Get("/about").ExpectStatus(500).ExpectContains("db down")
}

func TestStrictTemplates(t *testing.T) {
	loose := newTestApp(t)
	foliotest.New(t, loose.Handler()).Get("/").ExpectStatus(200)
	strict := newTestApp(t, func(c *Config) { c.StrictTemplates = true })
	foliotest.New(t, strict.Handler()).Get("/").ExpectStatus(500) // .Page.count is never set
}

func TestStatic(t *testing.T) {
	app := newTestApp(t)
	c := foliotest.New(t, app.Handler())

	hash := shortHash([]byte("body{}"))
	c.Get("/asset").ExpectContains("/static/app.css?v=" + hash)

	r := c.Get("/static/app.css?v=" + hash).ExpectStatus(200).ExpectContains("body{}")
	if r.Header.Get("Cache-Control") != "public, max-age=31536000, immutable" || r.Header.Get("ETag") != `"`+hash+`"` {
		t.Errorf("versioned headers = %v", r.Header)
	}
	if r := c.Get("/static/app.css"); r.Header.Get("Cache-Control") != "no-cache" {
		t.Errorf("unversioned Cache-Control = %q", r.Header.Get("Cache-Control"))
	}
	req := httptest.NewRequest("GET", foliotest.BaseURL+"/static/app.css", nil)
	req.Header.Set("If-None-Match", `"`+hash+`"`)
	c.Do(req).ExpectStatus(304)
	c.Get("/static/img/").ExpectStatus(404)
	c.Get("/static/").ExpectStatus(404)
	c.Get("/static/missing.css").ExpectStatus(404)
}

func TestClientScript(t *testing.T) {
	app := newTestApp(t)
	c := foliotest.New(t, app.Handler())
	r := c.Get(app.client.url()).ExpectStatus(200).ExpectContains("window.folio").ExpectNotContains("__FOLIO_DEV__ = true")
	if r.Header.Get("Cache-Control") != "public, max-age=31536000, immutable" {
		t.Errorf("client Cache-Control = %q", r.Header.Get("Cache-Control"))
	}

	head := newTestApp(t, func(c *Config) {
		c.Templates.(fstest.MapFS)["routes/head.tmpl"] = &fstest.MapFile{Data: []byte(`{{template "folio/head" .}}`)}
	})
	foliotest.New(t, head.Handler()).Get("/head").
		ExpectContains(`<meta name="folio-route" content="head">`).
		ExpectContains(`<meta name="folio-shell" content="layout/app">`).
		ExpectContains(head.client.url())

	dev := newTestApp(t, func(c *Config) { c.Reload = true })
	foliotest.New(t, dev.Handler()).Get("/_folio/folio.js").ExpectContains("__FOLIO_DEV__ = true")
	foliotest.New(t, app.Handler()).Get("/_folio/live").ExpectStatus(404)
}

func TestCompress(t *testing.T) {
	app := newTestApp(t)
	app.Use(Compress())
	c := foliotest.New(t, app.Handler())
	c.Header.Set("Accept-Encoding", "gzip, deflate")

	r := c.Get("/about").ExpectStatus(200)
	if r.Header.Get("Content-Encoding") != "gzip" || r.Header.Get("Content-Length") != "" {
		t.Fatalf("headers = %v", r.Header)
	}
	zr, err := gzip.NewReader(strings.NewReader(r.Body))
	if err != nil {
		t.Fatal(err)
	}
	plain, _ := io.ReadAll(zr)
	if string(plain) != "<main>about hi you</main>" {
		t.Errorf("decompressed = %q", plain)
	}

	c.Header.Set("Accept-Encoding", "gzip;q=0")
	if r := c.Get("/about"); r.Header.Get("Content-Encoding") != "" {
		t.Error("compressed despite q=0")
	}
}

func TestJSONHelpers(t *testing.T) {
	type in struct{ Name string }
	app := newTestApp(t)
	app.HandleFunc("POST /api", func(w http.ResponseWriter, r *http.Request) {
		var v in
		if err := DecodeJSON(w, r, &v); err != nil {
			JSON(w, 400, map[string]string{"error": err.Error()})
			return
		}
		JSON(w, 200, v)
	})
	c := foliotest.New(t, app.Handler())
	c.PostJSON("/api", `{"Name":"ada"}`).ExpectStatus(200).ExpectContains(`{"Name":"ada"}`)
	c.PostJSON("/api", `{"Nope":1}`).ExpectStatus(400)
	c.PostJSON("/api", `{"Name":"a"} {}`).ExpectStatus(400)
}

func TestLayoutsFor(t *testing.T) {
	app := newTestApp(t, func(c *Config) {
		fsys := c.Templates.(fstest.MapFS)
		fsys["routes/docs/deep/page.tmpl"] = &fstest.MapFile{Data: []byte(`deep`)}
		fsys["layouts/deep.tmpl"] = &fstest.MapFile{Data: []byte(`{{define "layout/docs/deep"}}<deep>{{.HTML}}</deep>{{end}}`)}
	})
	foliotest.New(t, app.Handler()).Get("/docs/deep/page").ExpectContains("<main><docs><deep>deep</deep></docs></main>")
}

func TestParseErrors(t *testing.T) {
	for name, files := range map[string]fstest.MapFS{
		"both map to route": {"routes/a.tmpl": {Data: []byte("a")}, "routes/a/index.tmpl": {Data: []byte("a")}},
		"is empty":          {"routes/a.tmpl": {Data: []byte(`{{define "other"}}x{{end}}`)}},
		"must be the last":  {"routes/[...a]/b.tmpl": {Data: []byte("x")}},
		"duplicate":         {"routes/[a]/[a].tmpl": {Data: []byte("x")}},
		"invalid route":     {"routes/a[b].tmpl": {Data: []byte("x")}},
	} {
		_, err := New(Config{Templates: files, Logger: quiet})
		if err == nil || !strings.Contains(err.Error(), name) {
			t.Errorf("%s: err = %v", name, err)
		}
	}
}

func TestRouteName(t *testing.T) {
	for in, want := range map[string]string{
		"routes/index.tmpl":       "index",
		"routes/about.tmpl":       "about",
		"routes/tools/index.tmpl": "tools",
		"routes/a/b/index.tmpl":   "a/b",
		"routes/j/[id].tmpl":      "j/[id]",
	} {
		if got, ok := routeName(in); !ok || got != want {
			t.Errorf("routeName(%q) = %q, want %q", in, got, want)
		}
	}
	if _, ok := routeName("partials/x.tmpl"); ok {
		t.Error("partial treated as route")
	}
}

func TestAcceptsGzip(t *testing.T) {
	for h, want := range map[string]bool{"gzip": true, "br, gzip;q=0.5": true, "gzip;q=0": false, "*": true, "deflate": false, "": false} {
		if acceptsGzip(h) != want {
			t.Errorf("acceptsGzip(%q) != %v", h, want)
		}
	}
}
