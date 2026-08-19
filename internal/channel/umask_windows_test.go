//go:build windows

package channel

// Windows has no umask. The test that uses this is skipped there; this exists
// so the package still compiles for the windows targets goreleaser builds.
func syscallUmask(int) int { return 0 }
