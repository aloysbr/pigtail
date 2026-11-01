package reference

import (
	"context"

	stp "github.com/aloysbr/pigtail/internal/setup"
)

func run() error { return stp.Kickstart(context.Background(), nil) }
