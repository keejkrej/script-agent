// Package integration contains end-to-end tests for the script-agent native
// binary and its public library API.
//
// The tests are deliberately not unit tests: they build the real
// CGO_ENABLED=0 binary, execute real Python (QuickPy WASM), JavaScript
// (QuickJS WASM), and Bash (host process) scripts, and verify the observable
// contract from the outside. They are opt-in because they compile WASM, build
// binaries, and spawn processes:
//
//	go test -tags scriptagent_integration ./integration
//
// See README.md in this directory for the full command list, the recorded
// results, and the binary metrics.
package integration
