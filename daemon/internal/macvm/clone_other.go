//go:build !darwin

package macvm

import "errors"

func cloneFile(string, string) error { return errors.New("macOS VMs run only on a Mac") }
