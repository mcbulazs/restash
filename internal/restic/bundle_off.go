//go:build !restic_bundled

package restic

// Built without restic inside; Find looks for it on the PC instead.
var (
	bundledGz      []byte
	bundledVersion string
	bundledHash    string
)
