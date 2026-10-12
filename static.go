package folio

import (
	"crypto/sha256"
	_ "embed"
	"encoding/hex"
	"io/fs"
	"net/http"
	"path"
	"strings"
	"time"
)

// ClientPrefix is where folio serves its own files. Add the client script to
// your shell layout's <head> with:
//
//	{{template "folio/head" .}}
const ClientPrefix = "/_folio/"

//go:embed client/folio.js
var clientJS []byte

// assetURL is the "asset" template function: it returns the static URL for
// name with a content hash, so the file can be cached forever and still
// update when it changes. {{asset "app.css"}} -> /static/app.css?v=3f2a91c0de
func (a *App) assetURL(name string) (string, error) {
	name = strings.TrimPrefix(path.Clean("/"+name), "/")
	hash, err := a.assetHash(name)
	if err != nil {
		return "", err
	}
	return a.cfg.StaticPrefix + name + "?v=" + hash, nil
}

func (a *App) assetHash(name string) (string, error) {
	if !a.cfg.Reload {
		if h, ok := a.assets.Load(name); ok {
			return h.(string), nil
		}
	}
	b, err := fs.ReadFile(a.cfg.Static, name)
	if err != nil {
		return "", err
	}
	h := shortHash(b)
	if !a.cfg.Reload {
		a.assets.Store(name, h)
	}
	return h, nil
}

func shortHash(b []byte) string {
	sum := sha256.Sum256(b)
	return hex.EncodeToString(sum[:5])
}

// staticHandler serves Config.Static without directory listings. Requests
// carrying the current ?v= hash (from the asset function) are cacheable
// forever; others must revalidate against the ETag.
func (a *App) staticHandler() http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		name := strings.TrimPrefix(path.Clean("/"+r.URL.Path), "/")
		info, err := fs.Stat(a.cfg.Static, name)
		if name == "" || err != nil || info.IsDir() {
			a.errorPage(w, r, a.site.Load(), http.StatusNotFound, nil)
			return
		}
		if hash, err := a.assetHash(name); err == nil {
			w.Header().Set("ETag", `"`+hash+`"`)
			if r.URL.Query().Get("v") == hash && !a.cfg.Reload {
				w.Header().Set("Cache-Control", "public, max-age=31536000, immutable")
			} else {
				w.Header().Set("Cache-Control", "no-cache")
			}
		}
		http.ServeFileFS(w, r, a.cfg.Static, name)
	})
}

// clientAsset serves the bundled client script. In dev mode it also turns on
// live reload.
type clientAsset struct {
	body []byte
	hash string
	dev  bool
}

func newClientAsset(dev bool) clientAsset {
	body := clientJS
	if dev {
		body = append([]byte("window.__FOLIO_DEV__ = true;\n"), clientJS...)
	}
	return clientAsset{body: body, hash: shortHash(body), dev: dev}
}

func (c clientAsset) url() string { return ClientPrefix + "folio.js?v=" + c.hash }

func (c clientAsset) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Content-Type", "text/javascript; charset=utf-8")
	w.Header().Set("ETag", `"`+c.hash+`"`)
	if r.URL.Query().Get("v") == c.hash && !c.dev {
		w.Header().Set("Cache-Control", "public, max-age=31536000, immutable")
	} else {
		w.Header().Set("Cache-Control", "no-cache")
	}
	http.ServeContent(w, r, "folio.js", time.Time{}, strings.NewReader(string(c.body)))
}
