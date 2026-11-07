package agent

import (
	"fmt"
	"os/exec"
)

// secureDir lets only SYSTEM and Administrators into the agent's folder
// (it holds the PC's key). The SIDs work in any Windows language.
func secureDir(dir string) error {
	out, err := exec.Command("icacls", dir, "/inheritance:r",
		"/grant:r", "*S-1-5-18:(OI)(CI)F", "/grant:r", "*S-1-5-32-544:(OI)(CI)F").CombinedOutput()
	if err != nil {
		return fmt.Errorf("locking %s: %v: %s", dir, err, out)
	}
	return nil
}
