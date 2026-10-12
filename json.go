package folio

import (
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"strings"
)

// JSON writes v as a JSON response with the given status.
func JSON(w http.ResponseWriter, status int, v any) error {
	b, err := json.Marshal(v)
	if err != nil {
		http.Error(w, http.StatusText(http.StatusInternalServerError), http.StatusInternalServerError)
		return err
	}
	w.Header().Set("Content-Type", "application/json; charset=utf-8")
	w.WriteHeader(status)
	_, err = w.Write(append(b, '\n'))
	return err
}

// MaxJSONBody is the largest request body DecodeJSON accepts.
const MaxJSONBody = 1 << 20

// DecodeJSON reads a single JSON value from the request body into v. It
// rejects unknown fields, trailing data and bodies over MaxJSONBody.
func DecodeJSON(w http.ResponseWriter, r *http.Request, v any) error {
	dec := json.NewDecoder(http.MaxBytesReader(w, r.Body, MaxJSONBody))
	dec.DisallowUnknownFields()
	if err := dec.Decode(v); err != nil {
		return err
	}
	if err := dec.Decode(&struct{}{}); !errors.Is(err, io.EOF) {
		return errors.New("request body must contain a single JSON value")
	}
	return nil
}

// WantsJSON reports whether the client prefers a JSON response, e.g. a
// fetch() call sending Accept: application/json.
func WantsJSON(r *http.Request) bool {
	accept := r.Header.Get("Accept")
	return strings.Contains(accept, "application/json") && !strings.Contains(accept, "text/html")
}

// SafeNext returns the request's "next" value (from the query or form) if it
// is a path on this site, and fallback otherwise. Use it to send users back
// after login without creating an open redirect.
func SafeNext(r *http.Request, fallback string) string {
	next := r.FormValue("next")
	if isLocalPath(next) {
		return next
	}
	return fallback
}

// isLocalPath accepts "/x" but not "//host", "/\host" (browsers treat
// backslashes as slashes) or anything with a scheme.
func isLocalPath(p string) bool {
	if !strings.HasPrefix(p, "/") || strings.HasPrefix(p, "//") || strings.HasPrefix(p, "/\\") {
		return false
	}
	return !strings.ContainsAny(p, "\r\n\t")
}
