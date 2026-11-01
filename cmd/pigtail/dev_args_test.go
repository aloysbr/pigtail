package main

import (
	"strings"
	"testing"

	"github.com/spf13/cobra"
)

func TestDevAndSbRefuseAPositionalArgument(t *testing.T) {
	t.Chdir(t.TempDir())
	for name, build := range map[string]func() *cobra.Command{"dev": devCmd, "sb": sbCmd} {
		err := runStaticCmd(t, build, "ws_cf8935")
		if err == nil || !strings.Contains(err.Error(), "takes no argument") || !strings.Contains(err.Error(), "pigtail start") {
			t.Errorf("%s: err = %v, want a usage error pointing to pigtail start", name, err)
		}
	}
}
