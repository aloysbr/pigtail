package webui

import (
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"io/fs"
	"os"
	"path"
	"path/filepath"
	"sort"
	"strings"
)

// StampFile is the file in dist that holds the hash of the sources it was
// built from (written by `make web`, checked by `make web-check` and by
// TestDistStampMatchesSources).
const StampFile = "SOURCES.sha256"

// NoSources is the hash when none of the listed source files exists.
const NoSources = "none"

// sourceFiles are the web/ files outside web/src that the hash covers.
var sourceFiles = []string{
	"web/package.json",
	"web/package-lock.json",
	"web/index.html",
	"web/vite.config.ts",
	"web/components.json",
}

// skippedDirs are never walked under web/src: dependencies, builds, caches.
var skippedDirs = map[string]bool{"node_modules": true, "dist": true, ".vite": true, ".cache": true, "coverage": true}

// SourcesHash is the one implementation of the front's source hash, under
// root (the repository root). The algorithm is frozen
// (TestSourcesHashPinned): the files are sourceFiles, web/tsconfig*.json and
// every file under web/src (skippedDirs and .DS_Store aside); their paths,
// relative to root with "/" separators, are sorted byte-wise; each gives one
// line "path\x00<sha256 hex of its content>\n"; the hash is the sha256 hex of
// the concatenated lines. No listed file present → NoSources.
func SourcesHash(root string) (string, error) {
	var paths []string
	for _, p := range sourceFiles {
		if ok, err := isFile(root, p); err != nil {
			return "", err
		} else if ok {
			paths = append(paths, p)
		}
	}
	tsconfigs, err := filepath.Glob(filepath.Join(root, "web", "tsconfig*.json"))
	if err != nil {
		return "", err
	}
	for _, full := range tsconfigs {
		if fi, err := os.Stat(full); err == nil && fi.Mode().IsRegular() {
			paths = append(paths, "web/"+filepath.Base(full))
		}
	}
	srcRoot := filepath.Join(root, "web", "src")
	err = filepath.WalkDir(srcRoot, func(p string, d fs.DirEntry, err error) error {
		if err != nil {
			if errors.Is(err, fs.ErrNotExist) && p == srcRoot {
				return fs.SkipAll
			}
			return err
		}
		if d.IsDir() {
			if p != srcRoot && skippedDirs[d.Name()] {
				return fs.SkipDir
			}
			return nil
		}
		if !d.Type().IsRegular() || d.Name() == ".DS_Store" {
			return nil
		}
		rel, err := filepath.Rel(root, p)
		if err != nil {
			return err
		}
		paths = append(paths, filepath.ToSlash(rel))
		return nil
	})
	if err != nil {
		return "", err
	}
	if len(paths) == 0 {
		return NoSources, nil
	}
	sort.Strings(paths) // byte-wise
	var b strings.Builder
	for _, p := range paths {
		content, err := os.ReadFile(filepath.Join(root, filepath.FromSlash(p)))
		if err != nil {
			return "", err
		}
		sum := sha256.Sum256(content)
		b.WriteString(path.Clean(p))
		b.WriteByte(0)
		b.WriteString(hex.EncodeToString(sum[:]))
		b.WriteByte('\n')
	}
	sum := sha256.Sum256([]byte(b.String()))
	return hex.EncodeToString(sum[:]), nil
}

func isFile(root, rel string) (bool, error) {
	fi, err := os.Stat(filepath.Join(root, filepath.FromSlash(rel)))
	if errors.Is(err, fs.ErrNotExist) {
		return false, nil
	}
	if err != nil {
		return false, err
	}
	return fi.Mode().IsRegular(), nil
}
