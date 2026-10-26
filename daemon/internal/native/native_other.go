//go:build !darwin

package native

import (
	"context"
	"errors"
	"io"
)

var errNotMac = errors.New("macOS jobs run on a Mac")

func inspect(string) Report {
	return Report{Platform: "not a Mac", Problem: "macOS jobs run only on an Apple Silicon Mac"}
}

func keep(context.Context, string, func(string, ...any)) error { return errNotMac }

// Kept is for Macs.
func Kept() bool { return false }

// Setup is for Macs.
func Setup(context.Context, SetupOptions) error { return errNotMac }

// Remove is for Macs.
func Remove(context.Context, SetupOptions) error { return errNotMac }

// Serve is for Macs.
func Serve(context.Context, io.Reader, io.Writer) int { return 1 }
