package folio

import (
	"fmt"
	"hash/fnv"
	"io/fs"
	"net/http"
	"sync"
	"time"
)

// liveReload tells connected browsers to reload when templates or static
// files change. It only runs with Config.Reload.
type liveReload struct {
	once    sync.Once
	mu      sync.Mutex
	clients map[chan struct{}]struct{}
}

func (l *liveReload) subscribe() chan struct{} {
	ch := make(chan struct{}, 1)
	l.mu.Lock()
	if l.clients == nil {
		l.clients = map[chan struct{}]struct{}{}
	}
	l.clients[ch] = struct{}{}
	l.mu.Unlock()
	return ch
}

func (l *liveReload) unsubscribe(ch chan struct{}) {
	l.mu.Lock()
	delete(l.clients, ch)
	l.mu.Unlock()
}

func (l *liveReload) broadcast() {
	l.mu.Lock()
	defer l.mu.Unlock()
	for ch := range l.clients {
		select {
		case ch <- struct{}{}:
		default:
		}
	}
}

// liveHandler streams a "reload" server-sent event whenever files change.
// When the server restarts the stream drops; the client reloads once it
// reconnects, which covers Go code changes under a watcher like gow or air.
func (a *App) liveHandler(w http.ResponseWriter, r *http.Request) {
	a.live.once.Do(func() { go a.watchFiles() })

	rc := http.NewResponseController(w)
	w.Header().Set("Content-Type", "text/event-stream")
	w.Header().Set("Cache-Control", "no-store")
	w.WriteHeader(http.StatusOK)
	fmt.Fprint(w, ": connected\n\n")
	if rc.Flush() != nil {
		return
	}

	ch := a.live.subscribe()
	defer a.live.unsubscribe(ch)
	ping := time.NewTicker(25 * time.Second)
	defer ping.Stop()
	for {
		select {
		case <-r.Context().Done():
			return
		case <-a.closing.Done():
			return
		case <-ch:
			fmt.Fprint(w, "event: reload\ndata: {}\n\n")
		case <-ping.C:
			fmt.Fprint(w, ": ping\n\n")
		}
		if rc.Flush() != nil {
			return
		}
	}
}

func (a *App) watchFiles() {
	last := fingerprint(a.cfg.Templates) ^ fingerprint(a.cfg.Static)
	tick := time.NewTicker(400 * time.Millisecond)
	defer tick.Stop()
	for {
		select {
		case <-a.closing.Done():
			return
		case <-tick.C:
		}
		if now := fingerprint(a.cfg.Templates) ^ fingerprint(a.cfg.Static); now != last {
			last = now
			a.logger.Info("files changed; reloading browsers")
			a.live.broadcast()
		}
	}
}

// fingerprint hashes every file's path, size and modification time.
func fingerprint(fsys fs.FS) uint64 {
	h := fnv.New64a()
	fs.WalkDir(fsys, ".", func(p string, d fs.DirEntry, err error) error {
		if err != nil || d.IsDir() {
			return nil
		}
		info, err := d.Info()
		if err != nil {
			return nil
		}
		fmt.Fprintf(h, "%s|%d|%d\n", p, info.Size(), info.ModTime().UnixNano())
		return nil
	})
	return h.Sum64()
}
