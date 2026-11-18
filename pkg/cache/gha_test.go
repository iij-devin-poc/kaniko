/*
Copyright 2026 Google LLC

Licensed under the Apache License, Version 2.0 (the "License");
you may not use this file except in compliance with the License.
You may obtain a copy of the License at

    http://www.apache.org/licenses/LICENSE-2.0

Unless required by applicable law or agreed to in writing, software
distributed under the License is distributed on an "AS IS" BASIS,
WITHOUT WARRANTIES OR CONDITIONS OF ANY KIND, either express or implied.
See the License for the specific language governing permissions and
limitations under the License.
*/

package cache

import (
	"encoding/base64"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/chainguard-dev/kaniko/pkg/config"
	v1 "github.com/google/go-containerregistry/pkg/v1"
	"github.com/google/go-containerregistry/pkg/v1/empty"
	"github.com/google/go-containerregistry/pkg/v1/mutate"
	"github.com/google/go-containerregistry/pkg/v1/random"
)

type fakeGHACacheServer struct {
	mu        sync.Mutex
	nextID    int
	reserved  map[int]string
	uploads   map[int][]byte
	committed map[string][]byte
	saves     int
}

func newFakeGHACacheServer() *fakeGHACacheServer {
	return &fakeGHACacheServer{reserved: map[int]string{}, uploads: map[int][]byte{}, committed: map[string][]byte{}}
}

func (f *fakeGHACacheServer) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if r.Header.Get("Authorization") == "" && !strings.HasPrefix(r.URL.Path, "/archive/") {
		w.WriteHeader(http.StatusUnauthorized)
		return
	}
	p := strings.TrimPrefix(r.URL.Path, "/_apis/artifactcache/")
	switch {
	case r.Method == http.MethodGet && strings.HasPrefix(r.URL.Path, "/archive/"):
		w.Write(f.committed[strings.TrimPrefix(r.URL.Path, "/archive/")])
	case r.Method == http.MethodGet && p == "cache":
		prefix := r.URL.Query().Get("keys")
		for k := range f.committed {
			if strings.HasPrefix(k, prefix) {
				json.NewEncoder(w).Encode(map[string]string{
					"cacheKey":        k,
					"archiveLocation": "http://" + r.Host + "/archive/" + k,
				})
				return
			}
		}
		w.WriteHeader(http.StatusNoContent)
	case r.Method == http.MethodPost && p == "caches":
		var req struct{ Key string }
		json.NewDecoder(r.Body).Decode(&req)
		if _, ok := f.committed[req.Key]; ok {
			w.WriteHeader(http.StatusConflict)
			fmt.Fprintf(w, `{"message":"Cache already exists.","typeKey":"ArtifactCacheItemAlreadyExistsException"}`)
			return
		}
		f.nextID++
		f.reserved[f.nextID] = req.Key
		fmt.Fprintf(w, `{"cacheID":%d}`, f.nextID)
	case strings.HasPrefix(p, "caches/"):
		id, _ := strconv.Atoi(strings.TrimPrefix(p, "caches/"))
		if r.Method == http.MethodPatch {
			var start, end int
			fmt.Sscanf(r.Header.Get("Content-Range"), "bytes %d-%d/*", &start, &end)
			b, _ := io.ReadAll(r.Body)
			buf := f.uploads[id]
			if len(buf) < end+1 {
				buf = append(buf, make([]byte, end+1-len(buf))...)
			}
			copy(buf[start:], b)
			f.uploads[id] = buf
			return
		}
		f.committed[f.reserved[id]] = f.uploads[id]
		f.saves++
	default:
		w.WriteHeader(http.StatusNotFound)
	}
}

func unsetenv(t *testing.T, key string) {
	t.Helper()
	t.Setenv(key, "")
	os.Unsetenv(key)
}

func fakeRuntimeToken(t *testing.T) string {
	t.Helper()
	enc := func(v any) string {
		b, err := json.Marshal(v)
		if err != nil {
			t.Fatal(err)
		}
		return base64.RawURLEncoding.EncodeToString(b)
	}
	now := time.Now()
	return enc(map[string]string{"alg": "none", "typ": "JWT"}) + "." + enc(map[string]any{
		"ac":  `[{"Scope":"refs/heads/main","Permission":3}]`,
		"nbf": now.Add(-time.Minute).Unix(),
		"exp": now.Add(time.Hour).Unix(),
	}) + ".sig"
}

func setupGHA(t *testing.T) *fakeGHACacheServer {
	t.Helper()
	f := newFakeGHACacheServer()
	srv := httptest.NewServer(f)
	t.Cleanup(srv.Close)
	t.Setenv("ACTIONS_RUNTIME_TOKEN", fakeRuntimeToken(t))
	t.Setenv("ACTIONS_CACHE_URL", srv.URL+"/")
	unsetenv(t, "ACTIONS_CACHE_SERVICE_V2")
	unsetenv(t, "ACTIONS_CACHE_API_FORCE_VERSION")
	unsetenv(t, "GHCACHE_TOKEN_ENC")
	oldDir := GHADir
	GHADir = t.TempDir()
	t.Cleanup(func() { GHADir = oldDir })
	return f
}

func cacheImage(t *testing.T) v1.Image {
	t.Helper()
	layer, err := random.Layer(1024, "application/vnd.docker.image.rootfs.diff.tar.gzip")
	if err != nil {
		t.Fatal(err)
	}
	img, err := mutate.CreatedAt(empty.Image, v1.Time{Time: time.Now()})
	if err != nil {
		t.Fatal(err)
	}
	img, err = mutate.AppendLayers(img, layer)
	if err != nil {
		t.Fatal(err)
	}
	return img
}

func TestIsGHAAndKey(t *testing.T) {
	for _, tc := range []struct {
		repo string
		gha  bool
		key  string
	}{
		{repo: "gha", gha: true, key: "kaniko-abc"},
		{repo: "gha:app", gha: true, key: "kaniko-app-abc"},
		{repo: "ghaa", gha: false},
		{repo: "gcr.io/gha", gha: false},
		{repo: "oci:/cache", gha: false},
		{repo: "", gha: false},
	} {
		if got := IsGHA(tc.repo); got != tc.gha {
			t.Errorf("IsGHA(%q) = %v, want %v", tc.repo, got, tc.gha)
		}
		if tc.gha {
			if got := GHAKey(tc.repo, "abc"); got != tc.key {
				t.Errorf("GHAKey(%q) = %q, want %q", tc.repo, got, tc.key)
			}
		}
	}
}

func TestGHACacheRoundTrip(t *testing.T) {
	f := setupGHA(t)
	opts := &config.KanikoOptions{CacheRepo: "gha:app", CacheOptions: config.CacheOptions{CacheTTL: time.Hour}}
	gc := &GHACache{Opts: opts}

	if _, err := gc.RetrieveLayer("abc"); !IsNotFound(err) {
		t.Fatalf("expected not found before save, got %v", err)
	}

	img := cacheImage(t)
	if err := SaveGHA(opts, "abc", img); err != nil {
		t.Fatal(err)
	}
	if _, ok := f.committed["kaniko-app-abc"]; !ok {
		t.Fatalf("expected key kaniko-app-abc to be committed, got %v", f.committed)
	}
	// Saving an existing key is not an error.
	if err := SaveGHA(opts, "abc", img); err != nil {
		t.Fatalf("saving an existing key: %v", err)
	}
	if f.saves != 1 {
		t.Fatalf("expected 1 commit, got %d", f.saves)
	}

	got, err := gc.RetrieveLayer("abc")
	if err != nil {
		t.Fatal(err)
	}
	wantLayers, _ := img.Layers()
	gotLayers, err := got.Layers()
	if err != nil {
		t.Fatal(err)
	}
	if len(gotLayers) != 1 {
		t.Fatalf("expected 1 layer, got %d", len(gotLayers))
	}
	wd, _ := wantLayers[0].Digest()
	gd, _ := gotLayers[0].Digest()
	if wd != gd {
		t.Fatalf("layer digest = %s, want %s", gd, wd)
	}

	// Load matches by prefix; a different key sharing the prefix must miss.
	if _, err := gc.RetrieveLayer("ab"); !IsNotFound(err) {
		t.Fatalf("expected prefix lookup to miss, got %v", err)
	}
	// Other scopes do not share entries.
	other := &GHACache{Opts: &config.KanikoOptions{CacheRepo: "gha", CacheOptions: config.CacheOptions{CacheTTL: time.Hour}}}
	if _, err := other.RetrieveLayer("abc"); !IsNotFound(err) {
		t.Fatalf("expected miss in default scope, got %v", err)
	}
}

func TestGHACacheExpired(t *testing.T) {
	setupGHA(t)
	opts := &config.KanikoOptions{CacheRepo: "gha", CacheOptions: config.CacheOptions{CacheTTL: time.Hour}}
	img, err := mutate.CreatedAt(cacheImage(t), v1.Time{Time: time.Now().Add(-2 * time.Hour)})
	if err != nil {
		t.Fatal(err)
	}
	if err := SaveGHA(opts, "old", img); err != nil {
		t.Fatal(err)
	}
	if _, err := (&GHACache{Opts: opts}).RetrieveLayer("old"); err == nil || !strings.Contains(err.Error(), "expired") {
		t.Fatalf("expected expired error, got %v", err)
	}
}

func TestNewGHAClientRequiresEnv(t *testing.T) {
	unsetenv(t, "ACTIONS_RUNTIME_TOKEN")
	unsetenv(t, "GHCACHE_TOKEN_ENC")
	if _, err := NewGHAClient(); err == nil || !strings.Contains(err.Error(), "ACTIONS_RUNTIME_TOKEN") {
		t.Fatalf("expected missing token error, got %v", err)
	}
	t.Setenv("ACTIONS_RUNTIME_TOKEN", fakeRuntimeToken(t))
	unsetenv(t, "ACTIONS_CACHE_URL")
	unsetenv(t, "ACTIONS_CACHE_SERVICE_V2")
	unsetenv(t, "ACTIONS_CACHE_API_FORCE_VERSION")
	if _, err := NewGHAClient(); err == nil || !strings.Contains(err.Error(), "URL") {
		t.Fatalf("expected missing URL error, got %v", err)
	}
}
