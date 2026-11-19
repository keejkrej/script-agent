module github.com/keejkrej/script-agent

go 1.27.3

toolchain go1.27.4

require (
	github.com/keejkrej/pi-go v0.0.0
	github.com/keejkrej/quickpy v0.0.0-20261119183116-9ef902956e91
)

require (
	github.com/tetratelabs/wazero v1.12.0 // indirect
	golang.org/x/sys v0.48.0 // indirect
	golang.org/x/text v0.43.0 // indirect
)

// The pi-go runtime is source-vendored under third_party/pi-go so the build is
// portable and does not depend on a machine-local checkout. See
// third_party/pi-go/PROVENANCE.md.
replace github.com/keejkrej/pi-go => ./third_party/pi-go
