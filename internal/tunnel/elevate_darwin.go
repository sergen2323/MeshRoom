//go:build darwin

package tunnel

import (
	"fmt"
	"os/exec"
	"strings"
)

// launchElevated запускает помощника с правами администратора через
// системный диалог macOS (osascript "with administrator privileges").
func launchElevated(exe string) error {
	shell := fmt.Sprintf("%q -helper >/dev/null 2>&1 &", exe)
	script := fmt.Sprintf(
		`do shell script %q with administrator privileges with prompt "MeshRoom требуются права администратора, чтобы создать виртуальный сетевой адаптер."`,
		shell,
	)
	out, err := exec.Command("/usr/bin/osascript", "-e", script).CombinedOutput()
	if err != nil {
		msg := strings.TrimSpace(string(out))
		if strings.Contains(msg, "-128") { // пользователь нажал «Отменить»
			return fmt.Errorf("запрос прав администратора отменён")
		}
		return fmt.Errorf("osascript: %v: %s", err, msg)
	}
	return nil
}
