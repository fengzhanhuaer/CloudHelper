//go:build linux || android

package main

import (
	"bufio"
	"io"
	"net"
	"os"
	"path/filepath"
	"strconv"
	"strings"
)

func readNetworkTrafficCounters() (rxBytes, txBytes uint64, ok bool) {
	file, err := os.Open("/proc/net/dev")
	if err != nil {
		return 0, 0, false
	}
	defer file.Close()
	return sumPhysicalNetworkTraffic(file, func(name string) bool { return isPhysicalNetworkInterface(name, "/sys/class/net") })
}

func sumPhysicalNetworkTraffic(reader io.Reader, isPhysical func(string) bool) (rxBytes, txBytes uint64, ok bool) {
	scanner := bufio.NewScanner(reader)
	for scanner.Scan() {
		line := strings.TrimSpace(scanner.Text())
		name, counters, found := strings.Cut(line, ":")
		name = strings.TrimSpace(name)
		if !found || name == "lo" || !isPhysical(name) {
			continue
		}
		fields := strings.Fields(counters)
		if len(fields) < 9 {
			continue
		}
		rx, rxErr := strconv.ParseUint(fields[0], 10, 64)
		tx, txErr := strconv.ParseUint(fields[8], 10, 64)
		if rxErr != nil || txErr != nil {
			return 0, 0, false
		}
		rxBytes += rx
		txBytes += tx
	}
	if err := scanner.Err(); err != nil {
		return 0, 0, false
	}
	return rxBytes, txBytes, true
}

func isPhysicalNetworkInterface(name, sysClassNet string) bool {
	iface, err := net.InterfaceByName(name)
	if err != nil || iface.Flags&net.FlagUp == 0 {
		return false
	}
	_, err = os.Stat(filepath.Join(sysClassNet, name, "device"))
	return err == nil
}
