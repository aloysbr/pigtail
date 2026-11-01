// Command affected prints what the pre-push hook must test for the files a
// push touches. Usage: affected -files <file> [-root <dir>].
//
// The file holds one module-relative path per line, or the single line `*`
// when the hook found no diff base. Stdout is either `full <reason>`, or a
// `make <target>…` line, then optionally `go-test <import path>…` and one
// `go-test-run <import path> <regex>` per walking test (internal/prepush).
// Exit 2 when go list fails: the caller then runs the whole make check.
//
// It never runs git: the hook computes the pushed files.
package main

import (
	"bufio"
	"flag"
	"fmt"
	"os"

	"github.com/aloysbr/pigtail/internal/prepush"
)

func main() {
	files := flag.String("files", "", "file listing the pushed paths, one per line")
	root := flag.String("root", ".", "module root")
	flag.Parse()
	if *files == "" {
		fmt.Fprintln(os.Stderr, "affected: -files is required")
		os.Exit(2)
	}
	paths, err := readLines(*files)
	if err != nil {
		fmt.Fprintln(os.Stderr, "affected:", err)
		os.Exit(2)
	}
	g, err := prepush.Load(*root)
	if err != nil {
		fmt.Fprintln(os.Stderr, "affected:", err)
		os.Exit(2)
	}
	for _, l := range prepush.Plan(paths, g).Lines() {
		fmt.Println(l)
	}
}

func readLines(name string) ([]string, error) {
	f, err := os.Open(name)
	if err != nil {
		return nil, err
	}
	defer func() { _ = f.Close() }()
	var out []string
	sc := bufio.NewScanner(f)
	for sc.Scan() {
		if l := sc.Text(); l != "" {
			out = append(out, l)
		}
	}
	return out, sc.Err()
}
