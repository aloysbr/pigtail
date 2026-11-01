//go:build !darwin

package clamshell

import "errors"

// Closed is macOS only: the lid guard does not exist elsewhere.
func Closed() (bool, error) { return false, errors.New("clamshell state: macOS only") }
