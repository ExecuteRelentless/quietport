package agent

import (
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"

	"github.com/fsnotify/fsnotify"
)

// Watcher: native filesystem events (FR-31) with a debounce. fsnotify uses FSEvents-equivalent kqueue on macOS and
// ReadDirectoryChangesW on Windows; directories are added recursively as they appear.
type Watcher struct {
	w        *fsnotify.Watcher
	mu       sync.Mutex
	dirty    map[string]time.Time // circle slug -> last event
	roots    map[string]string    // root path -> slug
	trigger  chan string
	debounce time.Duration
}

func NewWatcher(debounce time.Duration) (*Watcher, error) {
	fw, err := fsnotify.NewWatcher()
	if err != nil {
		return nil, err
	}
	w := &Watcher{w: fw, dirty: map[string]time.Time{}, roots: map[string]string{}, trigger: make(chan string, 64), debounce: debounce}
	go w.loop()
	return w, nil
}

func (w *Watcher) Trigger() <-chan string { return w.trigger }

func (w *Watcher) AddRoot(slug, root string) {
	w.mu.Lock()
	w.roots[root] = slug
	w.mu.Unlock()
	_ = filepath.WalkDir(root, func(p string, d os.DirEntry, err error) error {
		if err != nil {
			return nil
		}
		if d.IsDir() {
			if d.Name() == VersionsDir {
				return filepath.SkipDir
			}
			_ = w.w.Add(p)
		}
		return nil
	})
}

func (w *Watcher) RemoveRoot(root string) {
	w.mu.Lock()
	delete(w.roots, root)
	w.mu.Unlock()
	for _, p := range w.w.WatchList() {
		if strings.HasPrefix(p, root) {
			_ = w.w.Remove(p)
		}
	}
}

func (w *Watcher) slugFor(p string) string {
	w.mu.Lock()
	defer w.mu.Unlock()
	for root, slug := range w.roots {
		if p == root || strings.HasPrefix(p, root+string(os.PathSeparator)) {
			return slug
		}
	}
	return ""
}

func (w *Watcher) loop() {
	tick := time.NewTicker(500 * time.Millisecond)
	defer tick.Stop()
	for {
		select {
		case ev, ok := <-w.w.Events:
			if !ok {
				return
			}
			base := filepath.Base(ev.Name)
			if strings.Contains(ev.Name, string(os.PathSeparator)+VersionsDir) || strings.HasPrefix(base, ".qp-") || base == ".DS_Store" {
				continue
			}
			if ev.Has(fsnotify.Create) {
				if st, err := os.Stat(ev.Name); err == nil && st.IsDir() {
					_ = w.w.Add(ev.Name)
				}
			}
			if slug := w.slugFor(ev.Name); slug != "" {
				w.mu.Lock()
				w.dirty[slug] = time.Now()
				w.mu.Unlock()
			}
		case <-w.w.Errors:
		case <-tick.C:
			w.mu.Lock()
			for slug, t := range w.dirty {
				if time.Since(t) >= w.debounce {
					delete(w.dirty, slug)
					select {
					case w.trigger <- slug:
					default:
					}
				}
			}
			w.mu.Unlock()
		}
	}
}
