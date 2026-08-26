//go:build linux && linux_router

package main

import (
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"time"
)

var (
	probeLinuxRouterInterfacesPath           = "/etc/network/interfaces"
	probeLinuxRouterNetworkLookPath          = exec.LookPath
	probeLinuxRouterScheduleNetworkReconnect = func(delay time.Duration, reconnect func()) {
		time.AfterFunc(delay, reconnect)
	}
)

func init() {
	probeLinuxRouterPlatformRestoreInterfaceAuto = restoreProbeLinuxRouterInterfaceAuto
}

func restoreProbeLinuxRouterInterfaceAuto(configuredInterface string) (string, bool, error) {
	interfaceName, err := resolveProbeLinuxRouterInterface(configuredInterface, probeRouteLinuxTUNDeviceName())
	if err != nil {
		return "", false, err
	}
	for _, command := range []string{"ifquery", "ifdown", "ifup"} {
		if _, err := probeLinuxRouterNetworkLookPath(command); err != nil {
			return interfaceName, false, fmt.Errorf("%s is required to restore automatic network configuration: %w", command, err)
		}
	}

	original, err := os.ReadFile(probeLinuxRouterInterfacesPath)
	if err != nil {
		return interfaceName, false, fmt.Errorf("read network interfaces configuration: %w", err)
	}
	updated, changed, err := rewriteProbeLinuxRouterInterfaceAuto(original, interfaceName)
	if err != nil {
		return interfaceName, false, err
	}
	if !changed {
		return interfaceName, false, nil
	}

	info, err := os.Stat(probeLinuxRouterInterfacesPath)
	if err != nil {
		return interfaceName, false, fmt.Errorf("stat network interfaces configuration: %w", err)
	}
	backupPath := probeLinuxRouterInterfacesPath + ".cloudhelper-before-auto"
	if err := writeProbeLinuxRouterNetworkFile(backupPath, original, 0o600); err != nil {
		return interfaceName, false, fmt.Errorf("back up network interfaces configuration: %w", err)
	}
	if err := writeProbeLinuxRouterNetworkFile(probeLinuxRouterInterfacesPath, updated, info.Mode().Perm()); err != nil {
		return interfaceName, false, fmt.Errorf("write automatic network interfaces configuration: %w", err)
	}
	if output, queryErr := probeLinuxRouterRunCommand(5*time.Second, "ifquery", interfaceName); queryErr != nil {
		restoreErr := writeProbeLinuxRouterNetworkFile(probeLinuxRouterInterfacesPath, original, info.Mode().Perm())
		return interfaceName, false, errors.Join(fmt.Errorf("validate automatic network configuration: %w (output: %s)", queryErr, strings.TrimSpace(output)), restoreErr)
	}

	probeLinuxRouterScheduleNetworkReconnect(1500*time.Millisecond, func() {
		if _, downErr := probeLinuxRouterRunCommand(20*time.Second, "ifdown", interfaceName); downErr != nil {
			logProbeWarnf("restore router interface %s auto configuration: ifdown failed: %v", interfaceName, downErr)
		}
		if _, upErr := probeLinuxRouterRunCommand(30*time.Second, "ifup", interfaceName); upErr != nil {
			logProbeErrorf("restore router interface %s auto configuration: ifup failed: %v", interfaceName, upErr)
			return
		}
		logProbeInfof("router interface %s restored to automatic IPv4 configuration", interfaceName)
	})
	return interfaceName, true, nil
}

func rewriteProbeLinuxRouterInterfaceAuto(original []byte, interfaceName string) ([]byte, bool, error) {
	if !probeLinuxRouterInterfacePattern.MatchString(interfaceName) || interfaceName == "auto" {
		return nil, false, errors.New("resolved router interface is invalid")
	}
	newline := "\n"
	text := string(original)
	if strings.Contains(text, "\r\n") {
		newline = "\r\n"
		text = strings.ReplaceAll(text, "\r\n", "\n")
	}
	trailingNewline := strings.HasSuffix(text, "\n")
	text = strings.TrimSuffix(text, "\n")
	lines := strings.Split(text, "\n")
	output := make([]string, 0, len(lines))
	found := false

	for index := 0; index < len(lines); {
		fields := strings.Fields(lines[index])
		if len(fields) < 4 || fields[0] != "iface" || fields[1] != interfaceName || fields[2] != "inet" {
			output = append(output, lines[index])
			index++
			continue
		}
		if found {
			return nil, false, fmt.Errorf("multiple IPv4 stanzas found for router interface %s", interfaceName)
		}
		found = true
		indent := lines[index][:len(lines[index])-len(strings.TrimLeft(lines[index], " \t"))]
		output = append(output, indent+"iface "+interfaceName+" inet dhcp")
		index++
		for index < len(lines) && !isProbeLinuxRouterInterfacesTopLevel(lines[index]) {
			optionFields := strings.Fields(lines[index])
			if len(optionFields) == 0 || !isProbeLinuxRouterStaticIPv4Option(optionFields[0]) {
				output = append(output, lines[index])
			}
			index++
		}
	}
	if !found {
		return nil, false, fmt.Errorf("IPv4 stanza for router interface %s was not found in %s", interfaceName, probeLinuxRouterInterfacesPath)
	}

	updatedText := strings.Join(output, "\n")
	if trailingNewline {
		updatedText += "\n"
	}
	if newline == "\r\n" {
		updatedText = strings.ReplaceAll(updatedText, "\n", "\r\n")
	}
	updated := []byte(updatedText)
	return updated, string(updated) != string(original), nil
}

func isProbeLinuxRouterInterfacesTopLevel(line string) bool {
	if line == "" || strings.HasPrefix(line, " ") || strings.HasPrefix(line, "\t") {
		return false
	}
	fields := strings.Fields(line)
	if len(fields) == 0 || strings.HasPrefix(fields[0], "#") {
		return false
	}
	switch fields[0] {
	case "auto", "allow-auto", "allow-hotplug", "iface", "mapping", "source", "source-directory":
		return true
	default:
		return false
	}
}

func isProbeLinuxRouterStaticIPv4Option(option string) bool {
	switch strings.ToLower(strings.TrimSpace(option)) {
	case "address", "broadcast", "gateway", "netmask", "pointopoint":
		return true
	default:
		return false
	}
}

func writeProbeLinuxRouterNetworkFile(path string, content []byte, mode os.FileMode) error {
	directory := filepath.Dir(path)
	temporary, err := os.CreateTemp(directory, "."+filepath.Base(path)+".cloudhelper-*")
	if err != nil {
		return err
	}
	temporaryPath := temporary.Name()
	defer os.Remove(temporaryPath)
	if mode == 0 {
		mode = 0o600
	}
	if err := temporary.Chmod(mode); err != nil {
		temporary.Close()
		return err
	}
	if _, err := temporary.Write(content); err != nil {
		temporary.Close()
		return err
	}
	if err := temporary.Sync(); err != nil {
		temporary.Close()
		return err
	}
	if err := temporary.Close(); err != nil {
		return err
	}
	return os.Rename(temporaryPath, path)
}
