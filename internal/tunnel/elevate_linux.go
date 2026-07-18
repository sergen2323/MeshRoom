//go:build linux

package tunnel

import (
	"fmt"
	"os/exec"
)

// launchElevated запускает помощника через pkexec (PolicyKit).
func launchElevated(exe string) error {
	cmd := exec.Command("pkexec", exe, "-helper")
	if err := cmd.Start(); err != nil {
		return fmt.Errorf("pkexec: %w", err)
	}
	go cmd.Wait()
	return nil
}
