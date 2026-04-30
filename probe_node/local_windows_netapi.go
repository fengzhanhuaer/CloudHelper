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

	probeLocalConvertInterfaceLUIDToIndexNative = convertProbeLocalInterfaceLUIDToIndexNative
	probeLocalListNetAdaptersForLUIDLookup      = listProbeLocalWindowsNetAdapters
	probeLocalNetapiSleep                       = time.Sleep
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
	cleanIP := ip4.String()

	adapter, err := windowsFindAdapterByIfIndex(interfaceIndex)
	if err == nil {
		for _, existing := range adapter.IPv4Addrs {
			if strings.EqualFold(strings.TrimSpace(existing), cleanIP) {
				if bindErr := waitProbeLocalWindowsInterfaceIPv4Bindable(interfaceIndex, ip4, 5*time.Second); bindErr != nil {
					return bindErr
				}
				return ensureProbeLocalWindowsInterfaceIPv4StaticProfile(interfaceIndex, cleanIP, prefixLength)
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
	if bindErr := waitProbeLocalWindowsInterfaceIPv4Bindable(interfaceIndex, ip4, 5*time.Second); bindErr != nil {
		return bindErr
	}
	return ensureProbeLocalWindowsInterfaceIPv4StaticProfile(interfaceIndex, cleanIP, prefixLength)
}

func ensureProbeLocalWindowsInterfaceIPv4StaticProfile(interfaceIndex int, ipText string, prefixLength int) error {
	if interfaceIndex <= 0 {
		return errors.New("invalid interface index")
	}
	ip4 := net.ParseIP(strings.TrimSpace(ipText)).To4()
	if ip4 == nil {
		return errors.New("invalid ipv4 address")
	}
	if prefixLength <= 0 || prefixLength > 32 {
		prefixLength = probeLocalTUNRouteIPv4PrefixLen
	}
	cleanIP := ip4.String()
	cleanDNS := cleanIP
	cleanGateway := ""
	if parsedDNS := net.ParseIP(strings.TrimSpace(probeLocalTUNRouteGatewayIPv4)).To4(); parsedDNS != nil {
		cleanDNS = parsedDNS.String()
		cleanGateway = parsedDNS.String()
	}

	adapterAlias := ""
	if adapter, adapterErr := windowsFindAdapterByIfIndex(interfaceIndex); adapterErr == nil {
		adapterAlias = strings.TrimSpace(adapter.Name)
	}
	if adapterAlias != "" {
		maskText, maskErr := probeLocalIPv4MaskFromPrefix(prefixLength)
		if maskErr != nil {
			return maskErr
		}
		_, _ = runProbeLocalCommand(6*time.Second, "netsh", "interface", "ipv4", "delete", "dnsservers", fmt.Sprintf(`name="%s"`, adapterAlias), "all")
		gatewayArg := "gateway=none"
		if cleanGateway != "" {
			gatewayArg = fmt.Sprintf("gateway=%s", cleanGateway)
		}
		if output, err := runProbeLocalCommand(8*time.Second, "netsh", "interface", "ipv4", "set", "address", fmt.Sprintf(`name="%s"`, adapterAlias), "source=static", fmt.Sprintf("address=%s", cleanIP), fmt.Sprintf("mask=%s", maskText), gatewayArg, "store=persistent"); err != nil {
			return fmt.Errorf("apply static tun ipv4 address by netsh failed: %w", firstProbeLocalTUNErr(err, errors.New(strings.TrimSpace(output))))
		}
		if output, err := runProbeLocalCommand(8*time.Second, "netsh", "interface", "ipv4", "set", "dnsservers", fmt.Sprintf(`name="%s"`, adapterAlias), "source=static", fmt.Sprintf("address=%s", cleanDNS), "register=none", "validate=no"); err != nil {
			return fmt.Errorf("apply static tun dns by netsh failed: %w", firstProbeLocalTUNErr(err, errors.New(strings.TrimSpace(output))))
		}
	}

	script := fmt.Sprintf(`$ErrorActionPreference='Stop'; $idx=%d; $ip='%s'; $prefix=%d; $dns=@('%s'); Set-NetIPInterface -InterfaceIndex $idx -AddressFamily IPv4 -Dhcp Disabled -DadTransmits 0 -ErrorAction SilentlyContinue | Out-Null; $existing=Get-NetIPAddress -InterfaceIndex $idx -AddressFamily IPv4 -ErrorAction SilentlyContinue | Where-Object { $_.IPAddress -eq $ip } | Select-Object -First 1; if (-not $existing) { New-NetIPAddress -InterfaceIndex $idx -IPAddress $ip -PrefixLength $prefix -AddressFamily IPv4 -SkipAsSource $false -PolicyStore PersistentStore -ErrorAction SilentlyContinue | Out-Null }; Set-NetIPAddress -InterfaceIndex $idx -IPAddress $ip -PrefixLength $prefix -AddressFamily IPv4 -SkipAsSource $false -ErrorAction SilentlyContinue | Out-Null; Set-DnsClientServerAddress -InterfaceIndex $idx -ServerAddresses $dns -ErrorAction Stop | Out-Null; $ready=Get-NetIPAddress -InterfaceIndex $idx -AddressFamily IPv4 -ErrorAction SilentlyContinue | Where-Object { $_.IPAddress -eq $ip } | Select-Object -First 1; if (-not $ready) { throw 'tun ipv4 profile missing after static apply' }; $dnsCurrent=(Get-DnsClientServerAddress -InterfaceIndex $idx -AddressFamily IPv4 -ErrorAction SilentlyContinue).ServerAddresses; if (-not $dnsCurrent -or $dnsCurrent.Count -lt 1) { throw 'tun dns server missing after static apply' }; if (($dnsCurrent[0].Trim()) -ne $dns[0]) { throw ('tun dns mismatch after static apply current=' + $dnsCurrent[0] + ' expect=' + $dns[0]) }`, interfaceIndex, cleanIP, prefixLength, cleanDNS)
	if output, err := runProbeLocalCommand(8*time.Second, "powershell", "-NoProfile", "-NonInteractive", "-ExecutionPolicy", "Bypass", "-Command", script); err != nil {
		return fmt.Errorf("apply static tun ipv4 profile failed: %w", firstProbeLocalTUNErr(err, errors.New(strings.TrimSpace(output))))
	}
	return nil
}

func probeLocalIPv4MaskFromPrefix(prefixLength int) (string, error) {
	if prefixLength < 0 || prefixLength > 32 {
		return "", fmt.Errorf("invalid ipv4 prefix length: %d", prefixLength)
	}
	mask := net.CIDRMask(prefixLength, 32)
	if len(mask) != net.IPv4len {
		return "", fmt.Errorf("invalid ipv4 mask length for prefix: %d", prefixLength)
	}
	return net.IP(mask).String(), nil
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
			if repairErr := probeLocalRepairWindowsInterfaceIPv4Address(interfaceIndex, cleanIP, 15); repairErr == nil {
				time.Sleep(220 * time.Millisecond)
				conn2, bindErr2 := net.ListenPacket("udp4", listenAddr)
				if bindErr2 == nil {
					_ = conn2.Close()
					return nil
				}
				if !isProbeLocalListenAddrNotAvailableError(bindErr2) {
					return bindErr2
				}
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

func probeLocalRepairWindowsInterfaceIPv4Address(interfaceIndex int, ipText string, prefixLength int) error {
	if interfaceIndex <= 0 {
		return errors.New("invalid interface index")
	}
	ip4 := net.ParseIP(strings.TrimSpace(ipText)).To4()
	if ip4 == nil {
		return errors.New("invalid ipv4 address")
	}
	if prefixLength <= 0 || prefixLength > 32 {
		prefixLength = 15
	}
	script := fmt.Sprintf(`$ErrorActionPreference='Stop'; $idx=%d; $ip='%s'; $prefix=%d; Remove-NetIPAddress -InterfaceIndex $idx -AddressFamily IPv4 -IPAddress $ip -Confirm:$false -ErrorAction SilentlyContinue; Start-Sleep -Milliseconds 120; $existing=Get-NetIPAddress -InterfaceIndex $idx -AddressFamily IPv4 -ErrorAction SilentlyContinue | Where-Object { $_.IPAddress -eq $ip } | Select-Object -First 1; if (-not $existing) { New-NetIPAddress -InterfaceIndex $idx -IPAddress $ip -PrefixLength $prefix -AddressFamily IPv4 -SkipAsSource $false -PolicyStore ActiveStore -ErrorAction SilentlyContinue | Out-Null }; Set-NetIPAddress -InterfaceIndex $idx -IPAddress $ip -PrefixLength $prefix -AddressFamily IPv4 -SkipAsSource $false -ErrorAction SilentlyContinue | Out-Null`, interfaceIndex, ip4.String(), prefixLength)
	_, err := runProbeLocalCommand(8*time.Second, "powershell", "-NoProfile", "-NonInteractive", "-ExecutionPolicy", "Bypass", "-Command", script)
	if err != nil {
		return err
	}
	return nil
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
	ifIndex, nativeErr := probeLocalConvertInterfaceLUIDToIndexNative(luid)
	if nativeErr == nil && ifIndex > 0 {
		return ifIndex, nil
	}

	primaryErr := firstProbeLocalTUNErr(nativeErr, errors.New("ConvertInterfaceLuidToIndex returned zero"))
	var lookupErr error
	for _, delay := range []time.Duration{0, 200 * time.Millisecond, 450 * time.Millisecond, 800 * time.Millisecond, 1200 * time.Millisecond} {
		if delay > 0 {
			probeLocalNetapiSleep(delay)
		}
		ifIndexByList, err := lookupProbeLocalInterfaceIndexByLUID(luid)
		if err == nil && ifIndexByList > 0 {
			return ifIndexByList, nil
		}
		lookupErr = err
	}
	return 0, fmt.Errorf("convert interface luid to index failed: %w", errors.Join(primaryErr, lookupErr))
}

func convertProbeLocalInterfaceLUIDToIndexNative(luid uint64) (int, error) {
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

func lookupProbeLocalInterfaceIndexByLUID(luid uint64) (int, error) {
	adapters, err := probeLocalListNetAdaptersForLUIDLookup()
	if err != nil {
		return 0, err
	}
	for _, adapter := range adapters {
		if adapter.InterfaceLUID == luid && adapter.InterfaceIndex > 0 {
			return adapter.InterfaceIndex, nil
		}
	}
	return 0, fmt.Errorf("adapter not found for interface luid: %d", luid)
}

func ipv4ToUint32(ip net.IP) (uint32, bool) {
	ip4 := ip.To4()
	if ip4 == nil {
		return 0, false
	}
	return binary.LittleEndian.Uint32(ip4), true
}
