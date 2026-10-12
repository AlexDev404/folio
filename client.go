package folio

import (
	"embed"
	"io/fs"
	"net/http"
)

// ClientPrefix is where the bundled client script is served. Include it in
// your layout's <head>:
//
//	<script src="/_folio/folio.js"></script>
const ClientPrefix = "/_folio/"

//go:embed client
var clientFiles embed.FS

func clientHandler() http.Handler {
	sub, _ := fs.Sub(clientFiles, "client")
	return http.FileServerFS(sub)
}
