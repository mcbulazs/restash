//go:build restic_bundled

package restic

import _ "embed"

// The restic release for this OS, gzipped, with its version and the SHA-256
// of the unpacked program. Put there by scripts/fetch-restic.sh.
var (
	//go:embed bundle/restic.gz
	bundledGz []byte
	//go:embed bundle/version
	bundledVersion string
	//go:embed bundle/sha256
	bundledHash string
)
