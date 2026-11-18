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
	"context"
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"github.com/chainguard-dev/kaniko/pkg/config"
	"github.com/google/go-containerregistry/pkg/name"
	v1 "github.com/google/go-containerregistry/pkg/v1"
	"github.com/google/go-containerregistry/pkg/v1/tarball"
	"github.com/pkg/errors"
	"github.com/sirupsen/logrus"
	actionscache "github.com/tonistiigi/go-actions-cache"
)

// GHAPrefix selects the GitHub Actions cache service as the layer cache when
// used as --cache-repo, either alone ("gha") or with a scope ("gha:<scope>").
const GHAPrefix = "gha"

// ghaTagRepo is the placeholder repository used when writing cache images as
// tarballs; it is never pushed anywhere.
const ghaTagRepo = "kaniko.local/gha-cache"

// GHADir is where layers retrieved from the GitHub Actions cache are stored.
var GHADir = filepath.Join(config.KanikoDir, "gha-cache")

// IsGHA reports whether cacheRepo selects the GitHub Actions cache.
func IsGHA(cacheRepo string) bool {
	return cacheRepo == GHAPrefix || strings.HasPrefix(cacheRepo, GHAPrefix+":")
}

// GHAKey returns the GitHub Actions cache key for cacheKey.
func GHAKey(cacheRepo, cacheKey string) string {
	if scope := strings.TrimPrefix(strings.TrimPrefix(cacheRepo, GHAPrefix), ":"); scope != "" {
		return fmt.Sprintf("kaniko-%s-%s", scope, cacheKey)
	}
	return "kaniko-" + cacheKey
}

// NewGHAClient returns a GitHub Actions cache client configured from the
// ACTIONS_RUNTIME_TOKEN, ACTIONS_CACHE_URL, ACTIONS_RESULTS_URL and
// ACTIONS_CACHE_SERVICE_V2 environment variables.
var NewGHAClient = func() (*actionscache.Cache, error) {
	c, err := actionscache.TryEnv(actionscache.Opt{UserAgent: "kaniko"})
	if err != nil {
		return nil, errors.Wrap(err, "configuring GitHub Actions cache")
	}
	if c == nil {
		return nil, errors.New("ACTIONS_RUNTIME_TOKEN is not set; the GitHub Actions cache requires ACTIONS_RUNTIME_TOKEN and ACTIONS_CACHE_URL (or ACTIONS_RESULTS_URL with ACTIONS_CACHE_SERVICE_V2) to be exported to the step")
	}
	if c.URL == "" {
		return nil, errors.New("GitHub Actions cache URL is not set; export ACTIONS_CACHE_URL (or ACTIONS_RESULTS_URL with ACTIONS_CACHE_SERVICE_V2) to the step")
	}
	return c, nil
}

// GHACache is the GitHub Actions cache service layer cache.
type GHACache struct {
	Opts *config.KanikoOptions
}

// RetrieveLayer retrieves a layer from the GitHub Actions cache given the cache key ck.
func (gc *GHACache) RetrieveLayer(ck string) (v1.Image, error) {
	key := GHAKey(gc.Opts.CacheRepo, ck)
	logrus.Infof("Checking for cached layer %s in GitHub Actions cache...", key)

	c, err := NewGHAClient()
	if err != nil {
		return nil, err
	}
	ctx := context.Background()
	entry, err := c.Load(ctx, key)
	if err != nil {
		return nil, errors.Wrapf(err, "loading %s from GitHub Actions cache", key)
	}
	// Load matches keys by prefix, so require an exact match.
	if entry == nil || entry.Key != key {
		return nil, NotFoundErr{msg: fmt.Sprintf("%s not found in GitHub Actions cache", key)}
	}

	if err := os.MkdirAll(GHADir, 0o700); err != nil {
		return nil, errors.Wrap(err, "creating GitHub Actions cache directory")
	}
	f, err := os.CreateTemp(GHADir, ck+"-*.tar")
	if err != nil {
		return nil, errors.Wrap(err, "creating GitHub Actions cache file")
	}
	p := f.Name()
	err = entry.WriteTo(ctx, f)
	if cerr := f.Close(); err == nil {
		err = cerr
	}
	if err != nil {
		os.Remove(p)
		return nil, errors.Wrapf(err, "downloading %s from GitHub Actions cache", key)
	}

	img, err := tarball.ImageFromPath(p, nil)
	if err != nil {
		return nil, errors.Wrapf(err, "reading %s from GitHub Actions cache", key)
	}
	if err := verifyImage(img, gc.Opts.CacheTTL, key); err != nil {
		return nil, err
	}
	return img, nil
}

// SaveGHA stores img in the GitHub Actions cache under cacheKey.
func SaveGHA(opts *config.KanikoOptions, cacheKey string, img v1.Image) error {
	key := GHAKey(opts.CacheRepo, cacheKey)
	c, err := NewGHAClient()
	if err != nil {
		return err
	}

	tag, err := name.NewTag(ghaTagRepo+":"+cacheKey, name.WeakValidation)
	if err != nil {
		return errors.Wrap(err, "creating cache image tag")
	}
	// Write under GHADir so concurrent saves never touch the snapshotted filesystem.
	if err := os.MkdirAll(GHADir, 0o700); err != nil {
		return errors.Wrap(err, "creating GitHub Actions cache directory")
	}
	f, err := os.CreateTemp(GHADir, "save-"+cacheKey+"-*.tar")
	if err != nil {
		return errors.Wrap(err, "creating GitHub Actions cache file")
	}
	defer os.Remove(f.Name())
	defer f.Close()
	if err := tarball.Write(tag, img, f); err != nil {
		return errors.Wrap(err, "writing cache image tarball")
	}
	fi, err := f.Stat()
	if err != nil {
		return err
	}

	err = c.Save(context.Background(), key, &fileBlob{File: f, size: fi.Size()})
	if errors.Is(err, os.ErrExist) {
		logrus.Infof("Layer %s already exists in GitHub Actions cache", key)
		return nil
	}
	if err != nil {
		return errors.Wrapf(err, "saving %s to GitHub Actions cache", key)
	}
	return nil
}

type fileBlob struct {
	*os.File
	size int64
}

func (b *fileBlob) Size() int64 {
	return b.size
}
