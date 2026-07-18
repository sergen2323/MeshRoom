//go:build linux

package tunnel

import (
	"fmt"
	"os/exec"
	"strings"
)

func tunName() string { return "meshroom0" }

func configureOS(ifName, myIP, subnet string) error {
	if out, err := exec.Command("ip", "addr", "add", myIP+"/24", "dev", ifName).CombinedOutput(); err != nil {
		return fmt.Errorf("ip addr: %v: %s", err, strings.TrimSpace(string(out)))
	}
	if out, err := exec.Command("ip", "link", "set", "up", "dev", ifName).CombinedOutput(); err != nil {
		return fmt.Errorf("ip link: %v: %s", err, strings.TrimSpace(string(out)))
	}
	return nil
}

func deconfigureOS(ifName, subnet string) {}
