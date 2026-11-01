// Command webhash writes or checks the web front's source hash
// (webui.SourcesHash) for `make web` and `make web-check`.
//
//	go run ./internal/webui/cmd/webhash -write internal/webui/dist/SOURCES.sha256
//	go run ./internal/webui/cmd/webhash -check internal/webui/dist/SOURCES.sha256
package main

import (
	"flag"
	"fmt"
	"os"
	"strings"

	"github.com/aloysbr/pigtail/internal/webui"
)

func main() {
	root := flag.String("root", ".", "repository root")
	write := flag.String("write", "", "write the hash to this file")
	check := flag.String("check", "", "fail unless this file holds the current hash")
	flag.Parse()
	if (*write == "") == (*check == "") {
		fmt.Fprintln(os.Stderr, "webhash: give exactly one of -write or -check")
		os.Exit(2)
	}
	sum, err := webui.SourcesHash(*root)
	if err != nil {
		fmt.Fprintln(os.Stderr, "webhash:", err)
		os.Exit(1)
	}
	if *write != "" {
		if err := os.WriteFile(*write, []byte(sum+"\n"), 0o644); err != nil {
			fmt.Fprintln(os.Stderr, "webhash:", err)
			os.Exit(1)
		}
		return
	}
	b, err := os.ReadFile(*check)
	if err != nil {
		fmt.Fprintf(os.Stderr, "webhash: %v — run `make web` then commit internal/webui/dist\n", err)
		os.Exit(1)
	}
	if got := strings.TrimSpace(string(b)); got != sum {
		fmt.Fprintf(os.Stderr, "webhash: internal/webui/dist was built from other sources (%s, sources %s): run `make web` then commit internal/webui/dist\n", got, sum)
		os.Exit(1)
	}
}
