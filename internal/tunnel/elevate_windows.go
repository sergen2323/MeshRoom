//go:build windows

package tunnel

import (
	"fmt"
	"os/exec"
	"syscall"
)

// launchElevated запускает помощника с правами администратора через UAC.
func launchElevated(exe string) error {
	cmd := exec.Command("powershell", "-NoProfile", "-Command",
		fmt.Sprintf(`Start-Process -FilePath '%s' -ArgumentList '-helper' -Verb RunAs -WindowStyle Hidden`, exe))
	cmd.SysProcAttr = &syscall.SysProcAttr{HideWindow: true}
	if out, err := cmd.CombinedOutput(); err != nil {
		return fmt.Errorf("elevation failed (отменён UAC?): %v: %s", err, string(out))
	}
	return nil
}
