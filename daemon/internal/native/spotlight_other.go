//go:build !darwin

package native

import "io"

// Spotlight is for Macs.
func Spotlight(noIndex bool) SpotlightReport { return SpotlightReport{NoIndex: noIndex} }

// SpotlightAdvice is for Macs.
func SpotlightAdvice(io.Writer, bool) {}
