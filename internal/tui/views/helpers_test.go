package views

import "errors"

// assertAnError is a test helper that creates a simple error.
func assertAnError(msg string) error {
	return errors.New(msg)
}
