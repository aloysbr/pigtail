//go:build linux

package powersrc

import "errors"

// ReadLevel is macOS only: the lid guard does not exist on Linux.
func ReadLevel() (Level, error) { return Level{}, errors.New("battery level: macOS only") }
