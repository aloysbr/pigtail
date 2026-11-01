package main

import (
	"fmt"
	"net/url"
	"os/exec"
	"runtime"
	"strings"

	"github.com/spf13/cobra"
)

const defaultHTTPAddr = "127.0.0.1:7800"

// boardURL builds the board URL from a daemon HTTP address (defaulting when
// empty) for one view: a scope name, or all. The board is public, so the scope
// travels in the query, not in a header.
func boardURL(httpAddr, view string) string {
	if httpAddr == "" {
		httpAddr = defaultHTTPAddr
	}
	return "http://" + httpAddr + "/board?" + url.Values{"scope": {view}}.Encode()
}

// boardView is the view `pigtail board` opens: --scope when given, else the
// caller's own scope. resolveScope has already refused any view a non-master
// cell may not open; outside a cell, all resolves to the master's identity.
func boardView() (string, error) {
	sc, err := callerScope()
	if err != nil {
		return "", err
	}
	if o := strings.ToLower(strings.TrimSpace(scopeOverride())); o != "" {
		return o, nil
	}
	return sc, nil
}

// openBrowser tries to open url in the default browser; returns its error.
func openBrowser(url string) error {
	var cmd string
	var args []string
	switch runtime.GOOS {
	case "darwin":
		cmd = "open"
	case "windows":
		cmd, args = "rundll32", []string{"url.dll,FileProtocolHandler"}
	default:
		cmd = "xdg-open"
	}
	args = append(args, url)
	return exec.Command(cmd, args...).Start()
}

func boardCmd() *cobra.Command {
	var httpAddr string
	cmd := &cobra.Command{
		Use:   "board",
		Short: "Open the read-only subject board of your scope in your browser (--scope all from the master cell)",
		RunE: func(_ *cobra.Command, _ []string) error {
			view, err := boardView()
			if err != nil {
				return err
			}
			u := boardURL(httpAddr, view)
			fmt.Println(u)
			if err := openBrowser(u); err != nil {
				// Non-fatal: print the URL so the user can open it manually.
				fmt.Printf("(could not auto-open a browser: %v)\n", err)
			}
			return nil
		},
	}
	cmd.Flags().StringVar(&httpAddr, "http", defaultHTTPAddr, "daemon HTTP loopback address")
	return cmd
}
