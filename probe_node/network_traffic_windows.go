//go:build windows

package main

import (
	"unsafe"

	"golang.org/x/sys/windows"
)

func readNetworkTrafficCounters() (rxBytes, txBytes uint64, ok bool) {
	var table *windows.MibIfTable2
	if err := windows.GetIfTable2Ex(windows.MibIfTableNormal, &table); err != nil || table == nil {
		return 0, 0, false
	}
	defer windows.FreeMibTable(unsafe.Pointer(table))
	rows := unsafe.Slice(&table.Table[0], int(table.NumEntries))
	for _, row := range rows {
		const hardwareInterfaceFlag = 0x01
		if row.InterfaceAndOperStatusFlags&hardwareInterfaceFlag == 0 || row.Type == windows.IF_TYPE_SOFTWARE_LOOPBACK || row.OperStatus != windows.IfOperStatusUp {
			continue
		}
		rxBytes += row.InOctets
		txBytes += row.OutOctets
	}
	return rxBytes, txBytes, true
}
