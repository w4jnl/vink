package web

import (
	"crypto/sha256"
	"embed"
	"encoding/hex"
	"io/fs"
	"net/http"
	"sort"
	"strings"
)

//go:embed static
var staticFiles embed.FS

// Static serves the embedded assets under /static/<build hash>/ with
// immutable cache headers. The hash covers every file, so a new build
// gets new URLs and old caches never serve stale CSS.
type Static struct {
	fs   fs.FS
	hash string
}

// NewStatic hashes the embedded files.
func NewStatic() (*Static, error) {
	sub, err := fs.Sub(staticFiles, "static")
	if err != nil {
		return nil, err
	}
	var names []string
	if err := fs.WalkDir(sub, ".", func(p string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if !d.IsDir() {
			names = append(names, p)
		}
		return nil
	}); err != nil {
		return nil, err
	}
	sort.Strings(names)
	h := sha256.New()
	for _, n := range names {
		b, err := fs.ReadFile(sub, n)
		if err != nil {
			return nil, err
		}
		h.Write([]byte(n))
		h.Write(b)
	}
	return &Static{fs: sub, hash: hex.EncodeToString(h.Sum(nil))[:12]}, nil
}

// Hash is the build hash segment.
func (s *Static) Hash() string { return s.hash }

// URL returns the hashed URL for a file inside static/.
func (s *Static) URL(name string) string {
	return "/static/" + s.hash + "/" + strings.TrimPrefix(name, "/")
}

// Handler serves /static/{hash}/{path}. A matching hash is cached for a
// year; any other prefix is served without caching so a stale page still
// works after an upgrade.
func (s *Static) Handler() http.Handler {
	files := http.FileServerFS(s.fs)
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		rest := strings.TrimPrefix(r.URL.Path, "/static/")
		hash, file, ok := strings.Cut(rest, "/")
		if !ok || file == "" {
			http.NotFound(w, r)
			return
		}
		if hash == s.hash {
			w.Header().Set("Cache-Control", "public, max-age=31536000, immutable")
		} else {
			w.Header().Set("Cache-Control", "no-cache")
		}
		w.Header().Set("X-Content-Type-Options", "nosniff")
		r2 := r.Clone(r.Context())
		r2.URL.Path = "/" + file
		files.ServeHTTP(w, r2)
	})
}
