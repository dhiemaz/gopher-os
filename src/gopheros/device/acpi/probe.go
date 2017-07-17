package acpi

import "gopheros/device"

var (
	// ProbeFuncs is a slice of device probe functions
	// that is used by the hal package to probe for ACPI support.
	ProbeFuncs []device.ProbeFn
)
