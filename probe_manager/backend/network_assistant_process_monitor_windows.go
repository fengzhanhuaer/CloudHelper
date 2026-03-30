//go:build windows

package backend

import (
	"strings"
	"syscall"
	"unsafe"

	"golang.org/x/sys/windows"
)

// Windows MIB_TCPROW2 structure
type mibTCPRow2 struct {
	State      uint32
	LocalAddr  uint32
	LocalPort  uint32
	RemoteAddr uint32
	RemotePort uint32
	PID        uint32
	OffloadState uint32
}

// Windows MIB_TCPTABLE2 structure (variable length)
type mibTCPTable2 struct {
	NumEntries uint32
	Table      [1]mibTCPRow2
}

// Windows MIB_UDPROW_OWNER_PID structure
type mibUDPRowOwnerPID struct {
	LocalAddr uint32
	LocalPort uint32
	PID       uint32
}

// Windows MIB_UDPTABLE_OWNER_PID structure (variable length)
type mibUDPTableOwnerPID struct {
	NumEntries uint32
	Table      [1]mibUDPRowOwnerPID
}

var (
	modiphlpapi             = windows.NewLazySystemDLL("iphlpapi.dll")
	procGetTcpTable2        = modiphlpapi.NewProc("GetTcpTable2")
	procGetExtendedUDPTable = modiphlpapi.NewProc("GetExtendedUdpTable")
	modpsapi                = windows.NewLazySystemDLL("psapi.dll")
	procEnumProcesses       = windows.NewLazySystemDLL("kernel32.dll").NewProc("K32EnumProcesses")
)

// portToPIDTCP 通过源端口查找 TCP 连接对应的 PID。
func portToPIDTCP(port uint16) uint32 {
	var size uint32 = 65536
	buf := make([]byte, size)
	for i := 0; i < 3; i++ {
		ret, _, _ := procGetTcpTable2.Call(
			uintptr(unsafe.Pointer(&buf[0])),
			uintptr(unsafe.Pointer(&size)),
			1, // bOrder = TRUE (sorted)
		)
		if ret == 0 { // NO_ERROR
			break
		}
		if ret == 122 { // ERROR_INSUFFICIENT_BUFFER
			buf = make([]byte, size)
			continue
		}
		return 0
	}

	table := (*mibTCPTable2)(unsafe.Pointer(&buf[0]))
	count := table.NumEntries
	if count == 0 {
		return 0
	}
	// 解析 rows
	rowSize := unsafe.Sizeof(mibTCPRow2{})
	base := uintptr(unsafe.Pointer(&table.Table[0]))
	for i := uint32(0); i < count; i++ {
		row := (*mibTCPRow2)(unsafe.Pointer(base + uintptr(i)*rowSize))
		// localPort 在网络字节序中是大端存储在低16位
		lp := uint16(row.LocalPort>>8) | uint16(row.LocalPort<<8)
		if lp == port {
			return row.PID
		}
	}
	return 0
}

// portToPIDUDP 通过源端口查找 UDP 连接对应的 PID。
func portToPIDUDP(port uint16) uint32 {
	const UDP_TABLE_OWNER_PID = 1
	var size uint32 = 65536
	buf := make([]byte, size)
	for i := 0; i < 3; i++ {
		ret, _, _ := procGetExtendedUDPTable.Call(
			uintptr(unsafe.Pointer(&buf[0])),
			uintptr(unsafe.Pointer(&size)),
			1,                         // bOrder
			2,                         // AF_INET
			UDP_TABLE_OWNER_PID,       // TableClass
			0,
		)
		if ret == 0 {
			break
		}
		if ret == 122 {
			buf = make([]byte, size)
			continue
		}
		return 0
	}

	table := (*mibUDPTableOwnerPID)(unsafe.Pointer(&buf[0]))
	count := table.NumEntries
	if count == 0 {
		return 0
	}
	rowSize := unsafe.Sizeof(mibUDPRowOwnerPID{})
	base := uintptr(unsafe.Pointer(&table.Table[0]))
	for i := uint32(0); i < count; i++ {
		row := (*mibUDPRowOwnerPID)(unsafe.Pointer(base + uintptr(i)*rowSize))
		lp := uint16(row.LocalPort>>8) | uint16(row.LocalPort<<8)
		if lp == port {
			return row.PID
		}
	}
	return 0
}

// pidToProcessName 通过 PID 获取进程名（仅文件名部分，如 chrome.exe）。
func pidToProcessName(pid uint32) string {
	handle, err := windows.OpenProcess(windows.PROCESS_QUERY_LIMITED_INFORMATION, false, pid)
	if err != nil {
		return ""
	}
	defer windows.CloseHandle(handle)

	var buf [syscall.MAX_PATH]uint16
	size := uint32(len(buf))
	if err := windows.QueryFullProcessImageName(handle, 0, &buf[0], &size); err != nil {
		return ""
	}
	full := syscall.UTF16ToString(buf[:size])
	// 只取文件名部分
	for i := len(full) - 1; i >= 0; i-- {
		if full[i] == '\\' || full[i] == '/' {
			return full[i+1:]
		}
	}
	return full
}

// pidNameByPort 根据端口号查找对应进程名；isUDP 为 true 时查 UDP 表。
func pidNameByPort(port uint16, isUDP bool) string {
	var pid uint32
	if isUDP {
		pid = portToPIDUDP(port)
	} else {
		pid = portToPIDTCP(port)
		if pid == 0 {
			// 回退到 UDP 查（DNS 查询走 UDP）
			pid = portToPIDUDP(port)
		}
	}
	if pid == 0 {
		return ""
	}
	return pidToProcessName(pid)
}

// listRunningProcessesPlatform 枚举运行中进程，返回去重后的列表。
func listRunningProcessesPlatform() []NetworkProcessInfo {
	var pids [4096]uint32
	var needed uint32
	if err := windows.EnumProcesses(pids[:], &needed); err != nil {
		return nil
	}
	count := needed / 4
	var result []NetworkProcessInfo
	for i := uint32(0); i < count; i++ {
		pid := pids[i]
		if pid == 0 {
			continue
		}
		handle, err := windows.OpenProcess(
			windows.PROCESS_QUERY_LIMITED_INFORMATION,
			false,
			pid,
		)
		if err != nil {
			continue
		}
		var buf [syscall.MAX_PATH]uint16
		size := uint32(len(buf))
		var exePath, name string
		if qErr := windows.QueryFullProcessImageName(handle, 0, &buf[0], &size); qErr == nil {
			exePath = syscall.UTF16ToString(buf[:size])
			name = exePath
			for i2 := len(exePath) - 1; i2 >= 0; i2-- {
				if exePath[i2] == '\\' || exePath[i2] == '/' {
					name = exePath[i2+1:]
					break
				}
			}
		}
		windows.CloseHandle(handle)
		if name == "" {
			continue
		}
		result = append(result, NetworkProcessInfo{
			PID:     pid,
			Name:    name,
			ExePath: exePath,
		})
	}
	return deduplicateProcessList(result)
}

// exePathByPID 获取进程完整路径。
func exePathByPID(pid uint32) string {
	handle, err := windows.OpenProcess(windows.PROCESS_QUERY_LIMITED_INFORMATION, false, pid)
	if err != nil {
		return ""
	}
	defer windows.CloseHandle(handle)
	var buf [syscall.MAX_PATH]uint16
	size := uint32(len(buf))
	if err := windows.QueryFullProcessImageName(handle, 0, &buf[0], &size); err != nil {
		return ""
	}
	return strings.ReplaceAll(syscall.UTF16ToString(buf[:size]), "\\", "/")
}
