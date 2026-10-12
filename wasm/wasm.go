//go:build js && wasm

// Package wasm runs Go WebAssembly code for folio pages. Register a function
// per route, then call Run from main:
//
//	func main() {
//		wasm.Page("index", func() func() {
//			// set up the page; optionally return a cleanup function
//			return nil
//		})
//		wasm.Run()
//	}
//
// The function for the current route runs on the first page load and after
// every in-place navigation; the previous page's cleanup runs first.
package wasm

import "syscall/js"

// PageFunc sets up a page and may return a cleanup function (or nil) that
// runs before the next page's PageFunc.
type PageFunc func() (cleanup func())

var (
	pages   = map[string]PageFunc{}
	always  []PageFunc
	cleanup []func()
)

// Page registers fn for a route name, e.g. "index" or "journal/[id]".
func Page(route string, fn PageFunc) { pages[route] = fn }

// Always registers fn to run on every page, before the route's own function.
func Always(fn PageFunc) { always = append(always, fn) }

// Route returns the current route name, as reported by the folio client.
func Route() string {
	if f := js.Global().Get("folio"); f.Truthy() {
		return f.Get("route").String()
	}
	return ""
}

func run(route string) {
	for _, c := range cleanup {
		c()
	}
	cleanup = cleanup[:0]
	fns := append([]PageFunc{}, always...)
	if fn, ok := pages[route]; ok {
		fns = append(fns, fn)
	}
	for _, fn := range fns {
		if c := fn(); c != nil {
			cleanup = append(cleanup, c)
		}
	}
}

// Run starts running page functions and blocks forever. The folio client
// script must be on the page.
func Run() {
	onLoad := js.FuncOf(func(this js.Value, args []js.Value) any {
		run(args[0].Get("detail").Get("route").String())
		return nil
	})
	js.Global().Get("document").Call("addEventListener", "appLoad", onLoad)

	// The first appLoad may have fired before the module finished loading.
	if f := js.Global().Get("folio"); f.Truthy() && f.Get("loaded").Truthy() {
		run(Route())
	}
	select {}
}
