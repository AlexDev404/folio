# folio

A small server-rendered web framework for Go, built on `net/http` and
`html/template`, with no third-party dependencies.

- **File-based routing.** Drop a template in `templates/routes/` and it's a page. URL parameters work too: `routes/journal/[id].tmpl` serves `/journal/42`.
- **Load and Action hooks.** These run Go code for a route before it renders, similar to SvelteKit's `load`/`actions`.
- **Nested layouts.** Layouts can wrap each other, and a folder can swap in its own outer layout (a "shell").
- **Sessions and flash messages.** By default they live in an encrypted cookie, or you can keep them on the server through a pluggable store.
- **CSRF protection** is on by default, with no tokens to add to your forms.
- **Login-required areas.** Route groups take middleware, and `RequireLogin` sends visitors to a login page with a safe `next` link back.
- **In-place navigation and forms.** The bundled client script updates pages without full reloads, and forms work without JavaScript too.
- **Dev mode.** Templates reload automatically, the browser refreshes when files change, and errors show on the page.
- **Production details.** gzip, cacheable fingerprinted assets, graceful shutdown and JSON helpers.
- **Extras.** A WebAssembly page bridge, a test client, and a `folio new` command to start projects.

## Contents

- [Start a project](#start-a-project)
- [Routing](#routing)
- [Hooks](#hooks)
- [Templates](#templates)
- [Layouts](#layouts)
- [Forms and validation](#forms-and-validation)
- [Sessions, flash messages and login](#sessions-flash-messages-and-login)
- [Security](#security)
- [Middleware](#middleware)
- [Client script](#client-script)
- [Static files](#static-files)
- [JSON](#json)
- [WebAssembly](#webassembly)
- [Testing](#testing)
- [Development mode](#development-mode)
- [Config reference](#config-reference)

## Start a project

```sh
go install github.com/AlexDev404/folio/cmd/folio@latest
folio new myapp            # or: folio new -module github.com/me/myapp myapp
cd myapp
go run . -dev              # http://localhost:4000
```

The generated app includes a layout, three pages (one with a URL parameter), a validated form that uses a flash message and a redirect, and a test.

To add folio to an existing module, run `go get github.com/AlexDev404/folio`. folio needs Go 1.25 or newer.

## Routing

Every `.tmpl` file under the templates directory is parsed into one template set. Only files under `routes/` are pages:

| File                          | Route            | URL                          |
| ----------------------------- | ---------------- | ---------------------------- |
| `routes/index.tmpl`           | `index`          | `/`                          |
| `routes/about.tmpl`           | `about`          | `/about`                     |
| `routes/tools/index.tmpl`     | `tools`          | `/tools`                     |
| `routes/journal/[id].tmpl`    | `journal/[id]`   | `/journal/42`                |
| `routes/journal/new.tmpl`     | `journal/new`    | `/journal/new` (wins over `[id]`) |
| `routes/files/[...path].tmpl` | `files/[...path]`| `/files/a/b.txt` (path = `a/b.txt`) |

- **Page files.** A route file can be the page markup itself, or it can `{{define "<route>"}}` it.
- **Shared templates.** Everything outside `routes/`, such as layouts and partials in any folder, is shared and never served directly.
- **Matching order.** A fixed route always wins over one with parameters. Among parameter routes, the one with more fixed segments wins, and `[...catch-all]` routes are tried last.
- **Reading parameters.** Hooks read them with `c.Param("id")`, and templates use `{{.Params.id}}`.

## Hooks

```go
app.LoadAll(fn)           // every page, before the route's own hooks (e.g. load the signed-in user)
app.Load(route, fn)       // GET, HEAD and POST; several allowed, run in order
app.Action(route, fn)     // POST only; runs before Load so the page shows the result
```

All three take a `func(c *folio.Ctx) error`:

| Method | Description |
| ------ | ----------- |
| `c.Set(k, v)` / `c.Get(k)` | Page data, read in templates as `.Page.k` |
| `c.SetModel(v)` | Typed data for the template, read as `.Model`. A misspelled field is an error, not blank output |
| `c.Param(name)` | A URL parameter |
| `c.Status(code)` | The response status, e.g. 422 for invalid forms |
| `c.Success(msg)` / `c.Fail(msg, fieldErrors)` | Set `.Page.Message`, or `.Page.Failure` and `.Page.Errors` |
| `c.Flash(msg)` | A message for the *next* page, read as `.Flash`. Pair it with `Redirect` |
| `c.Redirect(url, code)` | Redirects and skips rendering |
| `c.JSON(status, v)` | Responds with JSON and skips rendering |
| `c.Session()` | The visitor's [session](#sessions-flash-messages-and-login) |
| `c.Header()` / `c.Writer()` | Raw response access. Writing through `Writer` takes over the response |
| `c.Request`, `c.Route`, `c.Logger` | The request, the route name, and a logger tagged with the route |

- **Errors.** A hook that returns `folio.ErrNotFound` (or wraps it) renders the 404 page, e.g. when `/journal/[id]` names an entry that doesn't exist. Any other error renders the 500 page.
- **Unregistered POSTs.** A POST to a route without an `Action` gets a 405.
- **Typo warning.** Hooks registered for a route that has no template are logged as a warning at startup, so a typo like `Load("tool", ...)` is easy to spot.
- **Other endpoints.** For APIs, health checks and the like, use `app.Handle(pattern, handler)` or `app.HandleFunc`. Patterns follow `http.ServeMux`, e.g. `"POST /api/moods"`.

## Templates

Every page, layout and error template receives a `folio.View`:

| Field     | Description |
| --------- | ----------- |
| `.Route`  | The route name, e.g. `journal/[id]` |
| `.Path`   | The URL path |
| `.Params` | URL parameters |
| `.Page`   | Page data from hooks and `Config.Globals`. Always contains `Message`, `Failure` and `Errors` |
| `.Model`  | Whatever a hook passed to `SetModel` |
| `.Flash`  | A flash message from the previous request |
| `.HTML`   | The rendered page, or the next layout in (layouts only) |
| `.Shell`, `.Status`, `.Dev` | The outer layout's name, the status code, and whether dev mode is on |

Templates can also use these built-ins:

- `{{template "folio/head" .}}`: put this in the shell's `<head>`. It adds the client script and the route metadata it needs.
- `{{asset "app.css"}}`: a cache-busting static URL, e.g. `/static/app.css?v=3f2a91c0de`.
- `folio/title`: define this template to control the document title. Use it in your `<title>`; folio also sends it when navigating in place, so the title updates correctly.

```html
{{define "folio/title"}}{{with .Page.Title}}{{.}} · {{end}}{{.Page.AppName}}{{end}}
```

Register your own functions with `Config.Funcs`. Set `StrictTemplates: true` to make a misspelled `.Page` key fail the page instead of printing nothing. For keys that are legitimately optional, use `{{index .Page "key"}}` or `{{index .Page.Errors "field"}}`.

## Layouts

Pages are wrapped from the inside out:

1. `layout/<folder>` templates for each folder above the page, nearest first. A page under `docs/guides/` uses `layout/docs/guides`, then `layout/docs`.
2. The **shell**, which is the outermost layout. By default it's `Config.Layout` (`layout/app`). A `shell/<folder>` template replaces it for everything under that folder, e.g. a bare `shell/print` for printable pages.

Each layout outputs `{{.HTML}}` where the inner content goes. The shell must contain the element that in-place navigation swaps: `id="content"` or `data-folio-content`.

## Forms and validation

```go
app.Action("journal/new", func(c *folio.Ctx) error {
	v := validator.NewValidator()
	title := c.Request.PostFormValue("title")
	v.Check(validator.NotBlank(title), "title", "Title is required")
	v.Check(validator.MaxLength(title, 100), "title", "100 characters at most")
	if !v.ValidData() {
		c.Status(http.StatusUnprocessableEntity)
		c.Fail("Please fix the errors below.", v.Errors)
		c.Set("title", title) // refill the form
		return nil
	}
	id, err := entries.Insert(title)
	if err != nil {
		return err
	}
	c.Flash("Saved!")
	c.Redirect("/journal/"+id, http.StatusSeeOther)
	return nil
})
```

```html
<form method="post" data-folio-form>
  <input name="title" value="{{.Page.title}}">
  {{with index .Page.Errors "title"}}<p class="error">{{.}}</p>{{end}}
  <button>Save</button>
</form>
```

- **Accepted encodings.** Both URL-encoded and multipart forms (file uploads) are parsed.
- **Without JavaScript.** The form above posts normally.
- **With the client script.** `data-folio-form` submits it in place.

## Sessions, flash messages and login

```go
app, _ := folio.New(folio.Config{SecretKey: []byte(os.Getenv("SESSION_SECRET"))}) // 32+ bytes

app.Action("login", func(c *folio.Ctx) error {
	user, err := users.Authenticate(c.Request.PostFormValue("email"), c.Request.PostFormValue("password"))
	if err != nil {
		c.Fail("Invalid email or password", nil)
		return nil
	}
	c.Session().Renew() // new session ID after login
	c.Session().Set("user_id", user.ID)
	c.Redirect(folio.SafeNext(c.Request, "/"), http.StatusSeeOther)
	return nil
})

app.HandleFunc("POST /logout", func(w http.ResponseWriter, r *http.Request) {
	folio.GetSession(r).Clear()
	http.Redirect(w, r, "/login", http.StatusSeeOther)
})

// Make the user available to every page (e.g. for the header).
app.LoadAll(func(c *folio.Ctx) error {
	if id := c.Session().Get("user_id"); id != "" {
		user, err := users.Get(id)
		if err != nil {
			return err
		}
		c.Set("User", user)
	}
	return nil
})

// Everything except these paths needs a login...
app.Use(app.RequireLogin(folio.LoginConfig{
	SessionKey: "user_id",
	LoginURL:   "/login",
	Public:     []string{"/register", "/about"},
}))
// ...or protect just one section:
// app.Group("/admin", app.RequireLogin(folio.LoginConfig{SessionKey: "user_id"}))
```

**Where sessions are stored.** By default a session is an encrypted, signed cookie. That means no database is needed, but it has two limits:

- **No server-side logout.** If a cookie is stolen, or you need to end someone's session after a password change, the server can't cancel it. It stays valid until it expires, after 30 days by default.
- **Size.** Everything in the session has to fit in about 4 KB.

When either limit matters, set `Config.SessionStore`. The cookie then holds only a random ID, and `Clear` deletes the session on the server.

```go
folio.Config{SessionStore: folio.NewMemoryStore()} // single instance; lost on restart
```

To keep sessions in your database, implement the three methods of `folio.SessionStore` (`Load`, `Save`, `Delete`).

Other session details:
- **Session cookie.** It's `HttpOnly`, uses `SameSite=Lax`, and is marked `Secure` behind HTTPS.
- **Missing `SecretKey`.** A random key is generated at startup, so every restart logs everyone out. folio logs a warning when this happens.
- **Login redirects.** `RequireLogin` sends visitors to `LoginURL?next=<the page they wanted>`, and `SafeNext` reads that value back. Only paths on your own site are accepted, so `//evil.com`, `/\evil.com` and `https://…` all fall back to the default. Requests that ask for JSON get a 401 instead of a redirect.

## Security

- **CSRF.** Every app is protected by Go's [`http.CrossOriginProtection`](https://pkg.go.dev/net/http#CrossOriginProtection). It uses the `Sec-Fetch-Site` and `Origin` headers that browsers send to reject form posts and `fetch` calls from other sites. You don't need hidden tokens in your forms or headers in your scripts. Allow specific other origins with `TrustedOrigins`, or turn the check off with `DisableCSRF`.
- **Escaping.** Templates escape output for HTML, URLs and JavaScript.
- **Routing.** Only files under `routes/` can be reached as pages. Static file serving doesn't list directories.

## Middleware

```go
app.Use(folio.RequestLogger(logger), folio.Recoverer(logger), folio.Compress())
app.Group("/admin", requireAdmin, auditLog)
```

- **`Use`** applies to every request. The first middleware added is the outermost.
- **`Group(prefix, ...)`** applies only to `prefix` and the paths under it, so `/admin` covers `/admin/users` but not `/administrator`.
- **Built-in middleware.** `RequestLogger` logs method, path, status and duration. `Recoverer` turns panics into 500s. `Compress` gzips text, JSON, SVG and WebAssembly responses. `RequireLogin` is described [above](#sessions-flash-messages-and-login).

## Client script

`{{template "folio/head" .}}` loads `/_folio/folio.js`. It's a classic script, not a module, so inline page scripts can use it straight away. Everything it does is opt-in:

| Feature | How to use it |
| ------- | ------------- |
| Links that load in place | Add `data-folio-link` to a link or to any ancestor, e.g. `<nav data-folio-link>`. Use `data-folio-link="false"` to opt a link back out |
| Forms that submit in place | Add `data-folio-form` to the form. GET forms navigate, and POST forms show the response and follow redirects |
| Navigating from code | `folio.navigate(url)`, `folio.submit(form)` |
| Page setup | Listen for `appLoad` on `document`. It fires on the first load and after every navigation, with `detail: {path, route}` |
| Cleanup on navigation | Timers, intervals and animation frames are cleared automatically. Use `folio.trackEventListener(target, type, fn)` and `folio.trackObserver(observer)` for listeners and observers you want removed the same way |

**What the server sends.** When the browser already shows the right shell, the server sends only the page content, not the whole document. If the next page uses a different shell, or the response is an error page, the browser falls back to a normal page load.

**Inside the swapped content.** Scripts run again, the page scrolls to the top (or to the `#hash`), and the title updates.

## Static files

- **Where they're served.** `Config.Static` is served under `/static/`. ETags are always sent and directories aren't listed.
- **Caching.** URLs built with `{{asset "file"}}` include a content hash, so they're cached for a year and still update when the file changes. Other URLs must revalidate with the server.
- **Embedding.** To ship templates and static files inside the binary:

```go
//go:embed templates static
var files embed.FS

tmpl, _ := fs.Sub(files, "templates")
static, _ := fs.Sub(files, "static")
app, err := folio.New(folio.Config{Templates: tmpl, Static: static})
```

`folio new` sets this up, reading from disk with `-dev` and from the embedded copies otherwise.

## JSON

```go
app.HandleFunc("POST /api/moods", func(w http.ResponseWriter, r *http.Request) {
	var in struct{ Text string }
	if err := folio.DecodeJSON(w, r, &in); err != nil { // 1 MB limit, unknown fields rejected
		folio.JSON(w, http.StatusBadRequest, map[string]string{"error": err.Error()})
		return
	}
	folio.JSON(w, http.StatusCreated, in)
})
```

`folio.WantsJSON(r)` reports whether the client asked for JSON.

## WebAssembly

The `folio/wasm` package runs Go code for each page, both on the first load and after in-place navigation:

```go
//go:build js && wasm

func main() {
	wasm.Always(setUpNavigation) // every page
	wasm.Page("index", func() func() {
		// attach handlers for the home page
		return func() { /* optional cleanup before the next page */ }
	})
	wasm.Run()
}
```

Load the module as usual (`wasm_exec.js` plus `WebAssembly.instantiateStreaming`). The folio client script needs to be on the page too.

## Testing

`foliotest` drives an app in memory and keeps cookies between requests, like a browser does:

```go
func TestJournal(t *testing.T) {
	c := foliotest.New(t, app.Handler())
	c.Get("/journal/new").ExpectStatus(200)
	c.PostForm("/journal/new", url.Values{"title": {""}}).ExpectStatus(422).ExpectContains("Title is required")
	c.PostForm("/journal/new", url.Values{"title": {"Hi"}}).
		ExpectRedirect("/journal/1").
		Follow().
		ExpectContains("Saved!")
}
```

## Development mode

Run with `Reload: true` (the `-dev` flag in generated apps) and folio will:

- re-parse templates on every request;
- reload the browser when templates or static files change, and again after the server restarts (e.g. when a watcher like `air` or `gow` rebuilds the binary);
- show errors and their messages on the 500 page.

## Config reference

| Field | Default | Description |
| ----- | ------- | ----------- |
| `Templates` | `os.DirFS("templates")` | Template tree |
| `Static` | `os.DirFS("static")` | Static files |
| `StaticPrefix` | `/static/` | URL prefix for static files |
| `Layout` | `layout/app` | Default shell |
| `Funcs` | — | Extra template functions |
| `Globals` | — | Values copied into every page's `.Page` |
| `Reload` | `false` | Development mode |
| `StrictTemplates` | `false` | Fail on missing `.Page` keys |
| `SecretKey` | random | 32+ bytes that encrypt the session cookie |
| `SessionStore` | — (cookie) | Server-side session storage |
| `SessionCookie` | `folio_session` | Cookie name |
| `SessionMaxAge` | 30 days | Session lifetime |
| `TrustedOrigins` | — | Other origins allowed to POST |
| `DisableCSRF` | `false` | Turn off cross-origin protection |
| `MaxFormMemory` | 32 MB | Memory for multipart forms; larger files go to disk |
| `Logger` | text to stdout | `*slog.Logger` |
