//go:build windows

package main

import (
	"encoding/binary"
	"errors"
	"fmt"
	"net"
	"strings"
	"syscall"
	"time"
	"unsafe"

	"golang.org/x/sys/windows"
)

const probeLocalWindowsNetapiErrorInsufficientBuffer = 122

// mibUnicastIPAddressRow 对齐 Windows MIB_UNICASTIPADDRESS_ROW（x64）布局。
type probeLocalMIBUnicastIPAddressRow struct {
	Address            [28]byte
	Pad0               [4]byte
	InterfaceLuid      uint64
	InterfaceIndex     uint32
	PrefixOrigin       uint32
	SuffixOrigin       uint32
	ValidLifetime      uint32
	PreferredLifetime  uint32
	OnLinkPrefixLength uint8
	SkipAsSource       uint8
	Pad1               [2]byte
	DadState           uint32
	ScopeID            uint32
	CreationTimeStamp  int64
}

var (
	probeLocalModIphlpapiNet                         = windows.NewLazySystemDLL("iphlpapi.dll")
	probeLocalProcCreateUnicastIPAddressEntryNet     = probeLocalModIphlpapiNet.NewProc("CreateUnicastIpAddressEntry")
	probeLocalProcInitializeUnicastIPAddressEntryNet = probeLocalModIphlpapiNet.NewProc("InitializeUnicastIpAddressEntry")
	probeLocalProcConvertInterfaceLuidToIndexNet     = probeLocalModIphlpapiNet.NewProc("ConvertInterfaceLuidToIndex")
)

func ensureProbeLocalWindowsInterfaceIPv4Address(interfaceIndex int, ipText string, prefixLength int) error {
	if interfaceIndex <= 0 {
		return errors.New("invalid interface index")
	}
	if prefixLength < 0 || prefixLength > 32 {
		return errors.New("invalid ipv4 prefix length")
	}
	ip4 := net.ParseIP(strings.TrimSpace(ipText)).To4()
	if ip4 == nil {
		return errors.New("invalid ipv4 address")
	}

	adapter, err := windowsFindAdapterByIfIndex(interfaceIndex)
	if err == nil {
		for _, existing := range adapter.IPv4Addrs {
			if strings.EqualFold(strings.TrimSpace(existing), ip4.String()) {
				return waitProbeLocalWindowsInterfaceIPv4Bindable(interfaceIndex, ip4, 5*time.Second)
			}
		}
	}

	var row probeLocalMIBUnicastIPAddressRow
	probeLocalProcInitializeUnicastIPAddressEntryNet.Call(uintptr(unsafe.Pointer(&row)))
	row.Address[0] = byte(windows.AF_INET)
	row.Address[1] = byte(windows.AF_INET >> 8)
	copy(row.Address[4:8], ip4)
	row.InterfaceIndex = uint32(interfaceIndex)
	row.OnLinkPrefixLength = uint8(prefixLength)
	row.ValidLifetime = 0xFFFFFFFF
	row.PreferredLifetime = 0xFFFFFFFF

	ret, _, callErr := probeLocalProcCreateUnicastIPAddressEntryNet.Call(uintptr(unsafe.Pointer(&row)))
	if ret != 0 && ret != uintptr(windows.ERROR_OBJECT_ALREADY_EXISTS) {
		if callErr != nil && !errors.Is(callErr, syscall.Errno(0)) {
			return fmt.Errorf("CreateUnicastIpAddressEntry failed: %w", callErr)
		}
		return fmt.Errorf("CreateUnicastIpAddressEntry failed: code=%d", ret)
	}
	return waitProbeLocalWindowsInterfaceIPv4Bindable(interfaceIndex, ip4, 5*time.Second)
}

func waitProbeLocalWindowsInterfaceIPv4Bindable(interfaceIndex int, ip4 net.IP, timeout time.Duration) error {
	cleanIP := strings.TrimSpace(ip4.String())
	if cleanIP == "" {
		return errors.New("invalid ipv4 address")
	}
	if timeout <= 0 {
		timeout = 5 * time.Second
	}
	deadline := time.Now().Add(timeout)
	listenAddr := net.JoinHostPort(cleanIP, "0")
	for {
		ipPresent := false
		adapter, listErr := windowsFindAdapterByIfIndex(interfaceIndex)
		if listErr == nil {
			for _, existing := range adapter.IPv4Addrs {
				if strings.EqualFold(strings.TrimSpace(existing), cleanIP) {
					ipPresent = true
					break
				}
			}
		}
		if ipPresent {
			conn, bindErr := net.ListenPacket("udp4", listenAddr)
			if bindErr == nil {
				_ = conn.Close()
				return nil
			}
			if !isProbeLocalListenAddrNotAvailableError(bindErr) {
				return bindErr
			}
		}
		if time.Now().After(deadline) {
			return fmt.Errorf("ipv4 address not bindable in time: if=%d ip=%s", interfaceIndex, cleanIP)
		}
		time.Sleep(100 * time.Millisecond)
	}
}

func isProbeLocalListenAddrNotAvailableError(err error) bool {
	if err == nil {
		return false
	}
	var errno syscall.Errno
	if errors.As(err, &errno) {
		if errno == syscall.EADDRNOTAVAIL || errno == syscall.Errno(10049) {
			return true
		}
	}
	message := strings.ToLower(strings.TrimSpace(err.Error()))
	if strings.Contains(message, "cannot assign requested address") || strings.Contains(message, "requested address is not valid") {
		return true
	}
	return false
}

type windowsAdapterInfo struct {
	InterfaceIndex int
	Name           string
	Description    string
	IPv4Addrs      []string
}

func windowsFindAdapterByIfIndex(interfaceIndex int) (windowsAdapterInfo, error) {
	if interfaceIndex <= 0 {
		return windowsAdapterInfo{}, errors.New("invalid interface index")
	}
	items, err := windowsListAdaptersIPv4()
	if err != nil {
		return windowsAdapterInfo{}, err
	}
	for _, item := range items {
		if item.InterfaceIndex == interfaceIndex {
			return item, nil
		}
	}
	return windowsAdapterInfo{}, fmt.Errorf("adapter not found for interface index: %d", interfaceIndex)
}

func windowsListAdaptersIPv4() ([]windowsAdapterInfo, error) {
	flags := uint32(windows.GAA_FLAG_INCLUDE_PREFIX)
	var size uint32 = 15 * 1024
	buf := make([]byte, size)
	for i := 0; i < 3; i++ {
		first := (*windows.IpAdapterAddresses)(unsafe.Pointer(&buf[0]))
		errCode := windows.GetAdaptersAddresses(windows.AF_INET, flags, 0, first, &size)
		if errCode == nil {
			return parseWindowsAdapterInfos(first), nil
		}
		if errors.Is(errCode, syscall.Errno(probeLocalWindowsNetapiErrorInsufficientBuffer)) {
			buf = make([]byte, size)
			continue
		}
		return nil, errCode
	}
	return nil, errors.New("GetAdaptersAddresses failed after retries")
}

func parseWindowsAdapterInfos(first *windows.IpAdapterAddresses) []windowsAdapterInfo {
	items := make([]windowsAdapterInfo, 0)
	for curr := first; curr != nil; curr = curr.Next {
		item := windowsAdapterInfo{
			InterfaceIndex: int(curr.IfIndex),
			Name:           strings.TrimSpace(windows.UTF16PtrToString(curr.FriendlyName)),
			Description:    strings.TrimSpace(windows.UTF16PtrToString(curr.Description)),
		}
		for uni := curr.FirstUnicastAddress; uni != nil; uni = uni.Next {
			ip := uni.Address.IP()
			if ip == nil || ip.To4() == nil {
				continue
			}
			item.IPv4Addrs = append(item.IPv4Addrs, ip.To4().String())
		}
		item.IPv4Addrs = dedupeProbeLocalIPv4Strings(item.IPv4Addrs)
		items = append(items, item)
	}
	return items
}

func dedupeProbeLocalIPv4Strings(items []string) []string {
	out := make([]string, 0, len(items))
	seen := make(map[string]struct{}, len(items))
	for _, raw := range items {
		ip4 := net.ParseIP(strings.TrimSpace(raw)).To4()
		if ip4 == nil {
			continue
		}
		key := ip4.String()
		if _, ok := seen[key]; ok {
			continue
		}
		seen[key] = struct{}{}
		out = append(out, key)
	}
	return out
}

func convertProbeLocalInterfaceLUIDToIndex(luid uint64) (int, error) {
	if luid == 0 {
		return 0, errors.New("invalid interface luid")
	}
	var ifIndex uint32
	ret, _, callErr := probeLocalProcConvertInterfaceLuidToIndexNet.Call(
		uintptr(unsafe.Pointer(&luid)),
		uintptr(unsafe.Pointer(&ifIndex)),
	)
	if ret != 0 {
		if callErr != nil && !errors.Is(callErr, syscall.Errno(0)) {
			return 0, callErr
		}
		return 0, syscall.Errno(ret)
	}
	if ifIndex == 0 {
		return 0, errors.New("ConvertInterfaceLuidToIndex returned zero")
	}
	return int(ifIndex), nil
}

func ipv4ToUint32(ip net.IP) (uint32, bool) {
	ip4 := ip.To4()
	if ip4 == nil {
		return 0, false
	}
	return binary.LittleEndian.Uint32(ip4), true
}
