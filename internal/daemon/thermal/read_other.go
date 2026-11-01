//go:build !darwin

package thermal

import "errors"

// Read is macOS only: the lid guard does not exist elsewhere.
func Read() (State, error) { return 0, errors.New("thermal state: macOS only") }
