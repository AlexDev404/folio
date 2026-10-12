# folio

A small server-rendered web framework for Go, built on `net/http` and
`html/template`, with no third-party dependencies.

- **File-based routing.** Drop a template in `templates/routes/` and it's a page.
- **Load and Action hooks.** These run Go code for a route before it renders, similar to SvelteKit's `load`/`actions`.
- **Layouts.** Pages are wrapped in `layout/app`, or in `layout/<route>` when that exists.
- **Client navigation.** A bundled script swaps page content in place and cleans up timers and listeners between pages.
- **Built-in middleware.** Request logging, panic recovery and graceful shutdown.
- **Embeddable.** Templates and static files can come from any `fs.FS`, so a whole site can ship as one binary.

## Install

```sh
go get github.com/AlexDev404/folio
```

While folio lives inside another repo, point at it with a replace directive:

```sh
go mod edit -require=github.com/AlexDev404/folio@v0.0.0 -replace=github.com/AlexDev404/folio=../folio
```

## Quick start

```
myapp/
  main.go
  templates/
    root.tmpl
    routes/
      index.tmpl
      about.tmpl
  static/
```

`templates/root.tmpl`:

```html
{{define "layout/app"}}
<!DOCTYPE html>
<html>
  <head>
    <title>{{.Data.PageData.AppName}}</title>
    <script src="/_folio/folio.js"></script>
  </head>
  <body>
    <nav><a href="/" data-folio-link>Home</a> <a href="/about" data-folio-link>About</a></nav>
    <div id="content">{{.Data.HTML}}</div>
  </body>
</html>
{{end}}
```

`templates/routes/index.tmpl`:

```html
<h1>Hello, {{.Data.PageData.name}}</h1>
{{with .Data.PageData.Message}}<p>{{.}}</p>{{end}}
<form method="post">
  <input name="name"> {{index .Data.PageData.Errors "name"}}
  <button>Save</button>
</form>
```

`main.go`:

```go
package main

import (
	"log"
	"log/slog"
	"net/http"
	"os"

	"github.com/AlexDev404/folio"
	"github.com/AlexDev404/folio/validator"
)

func main() {
	logger := slog.New(slog.NewTextHandler(os.Stdout, nil))
	app, err := folio.New(folio.Config{
		Logger:  logger,
		Globals: map[string]any{"AppName": "My App"},
	})
	if err != nil {
		log.Fatal(err)
	}

	name := "world"
	app.Action("index", func(c *folio.Ctx) error {
		v := validator.NewValidator()
		input := c.Request.PostFormValue("name")
		v.Check(validator.NotBlank(input), "name", "Name is required")
		if !v.ValidData() {
			c.Status(http.StatusUnprocessableEntity)
			c.Fail("Please fix the errors below.", v.Errors)
			return nil
		}
		name = input
		c.Success("Saved!")
		return nil
	})
	app.Load("index", func(c *folio.Ctx) error {
		c.Set("name", name)
		return nil
	})

	app.Use(folio.RequestLogger(logger), folio.Recoverer(logger))
	log.Fatal(app.ListenAndServe(":4000"))
}
```

## Routing

Every `.tmpl` file under the templates directory is parsed into one template set.
Only files under `routes/` are pages:

| File                       | Route     | URL       |
| -------------------------- | --------- | --------- |
| `routes/index.tmpl`        | `index`   | `/`       |
| `routes/about.tmpl`        | `about`   | `/about`  |
| `routes/tools/index.tmpl`  | `tools`   | `/tools`  |
| `routes/docs/intro.tmpl`   | `docs/intro` | `/docs/intro` |

A route file can be the page markup itself, or it can `{{define "<route>"}}` it.
Everything outside `routes/`, such as layouts and partials, is shared and is never served directly.

## Template data

Every template receives a `TemplateData` value:

| Field                | Description                                       |
| -------------------- | ------------------------------------------------- |
| `.Data.Path`         | The route name, e.g. `index`                      |
| `.Data.PageData`     | Values set by hooks, plus `Config.Globals`        |
| `.Data.HTML`         | The rendered page (layouts only)                  |

`PageData` always contains `Message`, `Failure` and `Errors` (a map of field
name to message). `Ctx.Success` and `Ctx.Fail` set them.

## Hooks

```go
app.Load(route, func(c *folio.Ctx) error)   // GET, HEAD and POST; several allowed, run in order
app.Action(route, func(c *folio.Ctx) error) // POST only; runs before Load
```

- `c.Set` / `c.Get` read and write page data, and `c.Status` sets the response code.
- `c.Header()` gives access to the response headers, for example to set cookies.
- `c.Redirect(url, code)` and anything written through `c.Writer()` take over the response, so rendering is skipped.
- A hook that returns an error renders the 500 page.
- A POST to a route that has no Action gets a 405.

For non-page endpoints such as JSON APIs or health checks, use `app.Handle(pattern, handler)`.

## Layouts and error pages

- Each page is wrapped in `layout/<route>` if it's defined, otherwise in `Config.Layout` (default `layout/app`). If neither exists, the page is sent without a layout.
- 404 and 500 pages render the `error/404` and `error/500` templates. If those don't exist, folio serves `<static>/errors/404.html` and `500.html`, and if those are missing too, it sends plain text.

## Client script

Add `<script src="/_folio/folio.js"></script>` to your layout's `<head>`. It must be a classic script, not a module. It provides:

- `folio.navigate(url)` fetches the page and swaps the contents of `#content`. If the content element is missing, it falls back to a full page load. Back/forward navigation is handled the same way.
- `<a data-folio-link>` makes a link navigate in place.
- An `appLoad` event fires on `document` after the first load and after each navigation.
- Interval and timeout timers and animation frames are cleared before each swap. Use `folio.trackEventListener(target, type, fn)` and `folio.trackObserver(observer)` for listeners and observers that should be cleaned up the same way.
- Scripts in swapped-in content run again.

## Config

| Field          | Default                    |
| -------------- | -------------------------- |
| `Templates`    | `os.DirFS("templates")`    |
| `Static`       | `os.DirFS("static")`       |
| `StaticPrefix` | `/static/`                 |
| `Layout`       | `layout/app`               |
| `Funcs`        | none                       |
| `Globals`      | none                       |
| `Reload`       | `false` (when true, templates are re-parsed on every request, for development) |
| `Logger`       | text logger on stdout      |

To embed templates and static files in a single binary:

```go
//go:embed templates static
var files embed.FS

tmpl, _ := fs.Sub(files, "templates")
static, _ := fs.Sub(files, "static")
app, err := folio.New(folio.Config{Templates: tmpl, Static: static})
```
