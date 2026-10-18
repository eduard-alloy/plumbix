// Package plumbix is the module root. It carries the build context from
// which a running server rebuilds the validator images it ships (WU-032,
// A32): the sources of internal/ and validators/ with the module files,
// so a deployed binary needs no source checkout to build them.
package plumbix

import "embed"

// BuildContext is the Docker build context of the shipped images: the
// module files and the Go sources their Dockerfiles copy. Test files and
// test data ride along; they cost little and keep the embed rule simple.
//
//go:embed go.mod go.sum internal validators
var BuildContext embed.FS
