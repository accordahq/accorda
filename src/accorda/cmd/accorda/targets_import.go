// Package main — target driver registration.
//
// This file imports every target driver package so its init function registers
// its TargetBuilder with the targets registry (internal/targets/builder.go).
// The command layer builds targets through the registry without importing the
// concrete drivers anywhere else, so adding a new target type is a one-line
// import here plus the driver package itself.
package main

import (
	_ "accorda/internal/targets/compose" // register compose target builder
	_ "accorda/internal/targets/image"   // register image target builder
)
