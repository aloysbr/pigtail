package prepush

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
)

// minPackages is the floor under which Load distrusts what go list read: the
// module has well over a hundred packages, so a near-empty graph is a broken
// listing, never a small module.
const minPackages = 50

// goListFields are the only fields Load asks go list for.
const goListFields = "ImportPath,ForTest,Dir,Deps,EmbedFiles,TestEmbedFiles,XTestEmbedFiles,Module,Error,DepsErrors"

type goListPkg struct {
	ImportPath      string
	ForTest         string
	Dir             string
	Deps            []string
	EmbedFiles      []string
	TestEmbedFiles  []string
	XTestEmbedFiles []string
	Module          *struct{ Path, Dir string }
	Error           *struct{ Err string }
	DepsErrors      []struct{ Err string }
}

// Load lists the module rooted at root, test variants included. An exit of
// go list other than 0, an undecodable stream or fewer than minPackages
// packages is an error: the caller then tests everything (D11).
func Load(root string) (Graph, error) {
	cmd := exec.Command("go", "list", "-e", "-test", "-json="+goListFields, "./...")
	cmd.Dir = root
	var stderr bytes.Buffer
	cmd.Stderr = &stderr
	out, err := cmd.Output()
	if err != nil {
		return Graph{}, fmt.Errorf("go list: %w: %s", err, strings.TrimSpace(stderr.String()))
	}
	return decode(bytes.NewReader(out), root)
}

func decode(r io.Reader, root string) (Graph, error) {
	absRoot, err := filepath.Abs(root)
	if err != nil {
		return Graph{}, err
	}
	g := Graph{Exists: func(rel string) bool {
		_, err := os.Lstat(filepath.Join(absRoot, filepath.FromSlash(rel)))
		return err == nil
	}}
	modDir := absRoot
	dec := json.NewDecoder(r)
	plain := 0
	for {
		var p goListPkg
		if err := dec.Decode(&p); errors.Is(err, io.EOF) {
			break
		} else if err != nil {
			return Graph{}, fmt.Errorf("decode go list: %w", err)
		}
		if p.Module != nil && g.Module == "" {
			g.Module = p.Module.Path
			if p.Module.Dir != "" {
				modDir = p.Module.Dir
			}
		}
		rel, err := filepath.Rel(modDir, p.Dir)
		if err != nil || strings.HasPrefix(rel, "..") {
			return Graph{}, fmt.Errorf("package %s lies outside the module (%s)", p.ImportPath, p.Dir)
		}
		pkg := Pkg{ImportPath: p.ImportPath, Dir: filepath.ToSlash(rel), Deps: p.Deps, ForTest: p.ForTest}
		pkg.Embeds = append(append(append(pkg.Embeds, p.EmbedFiles...), p.TestEmbedFiles...), p.XTestEmbedFiles...)
		var errs []string
		if p.Error != nil {
			errs = append(errs, p.Error.Err)
		}
		for _, e := range p.DepsErrors {
			errs = append(errs, e.Err)
		}
		pkg.Error = strings.Join(errs, "\n")
		if p.ForTest == "" && !isTestMain(pkg) {
			plain++
		}
		g.Pkgs = append(g.Pkgs, pkg)
	}
	if plain < minPackages {
		return Graph{}, fmt.Errorf("go list read nothing: %d packages, want at least %d", plain, minPackages)
	}
	return g, nil
}
