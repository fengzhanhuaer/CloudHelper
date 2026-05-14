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
	probeLocalProcDeleteUnicastIPAddressEntryNet     = probeLocalModIphlpapiNet.NewProc("DeleteUnicastIpAddressEntry")
	probeLocalProcSetUnicastIPAddressEntryNet        = probeLocalModIphlpapiNet.NewProc("SetUnicastIpAddressEntry")
	probeLocalProcInitializeUnicastIPAddressEntryNet = probeLocalModIphlpapiNet.NewProc("InitializeUnicastIpAddressEntry")
	probeLocalProcConvertInterfaceLuidToIndexNet     = probeLocalModIphlpapiNet.NewProc("ConvertInterfaceLuidToIndex")
	probeLocalProcCreateIpForwardEntry2Net           = probeLocalModIphlpapiNet.NewProc("CreateIpForwardEntry2")
	probeLocalProcDeleteIpForwardEntry2Net           = probeLocalModIphlpapiNet.NewProc("DeleteIpForwardEntry2")
	probeLocalProcGetIpForwardTable2Net              = probeLocalModIphlpapiNet.NewProc("GetIpForwardTable2")
	probeLocalProcInitializeIpForwardEntryNet        = probeLocalModIphlpapiNet.NewProc("InitializeIpForwardEntry")
	probeLocalProcSetIpForwardEntry2Net              = probeLocalModIphlpapiNet.NewProc("SetIpForwardEntry2")
	probeLocalProcFreeMibTableNet                    = probeLocalModIphlpapiNet.NewProc("FreeMibTable")
	probeLocalProcSetInterfaceDnsSettingsNet         = probeLocalModIphlpapiNet.NewProc("SetInterfaceDnsSettings")

	probeLocalCreateWindowsRouteEntry          = ensureProbeLocalWindowsRouteNative
	probeLocalDeleteWindowsRouteEntry          = deleteProbeLocalWindowsRouteNative
	probeLocalResolveWindowsPrimaryEgressRoute = resolveProbeLocalWindowsPrimaryEgressRouteTarget
	probeLocalSnapshotWindowsIPv4Routes        = snapshotProbeLocalWindowsIPv4Routes
	probeLocalSetWindowsInterfaceDNS           = setProbeLocalWindowsInterfaceDNS
	probeLocalFindWindowsAdapterByIfIndex      = windowsFindAdapterByIfIndex
	probeLocalUpsertWindowsInterfaceIPv4       = upsertProbeLocalWindowsInterfaceIPv4Address
	probeLocalDeleteWindowsInterfaceIPv4       = deleteProbeLocalWindowsInterfaceIPv4Address
	probeLocalCallSetUnicastIPAddressEntry      = probeLocalCallSetUnicastIPAddressEntryDefault
	probeLocalCallCreateUnicastIPAddressEntry   = probeLocalCallCreateUnicastIPAddressEntryDefault

	probeLocalConvertInterfaceLUIDToIndexNative = convertProbeLocalInterfaceLUIDToIndexNative
	probeLocalListNetAdaptersForLUIDLookup      = listProbeLocalWindowsNetAdapters
	probeLocalNetapiSleep                       = time.Sleep
)

func probeLocalCallSetUnicastIPAddressEntryDefault(row *probeLocalMIBUnicastIPAddressRow) (uintptr, error) {
	ret, _, callErr := probeLocalProcSetUnicastIPAddressEntryNet.Call(uintptr(unsafe.Pointer(row)))
	return ret, callErr
}

func probeLocalCallCreateUnicastIPAddressEntryDefault(row *probeLocalMIBUnicastIPAddressRow) (uintptr, error) {
	ret, _, callErr := probeLocalProcCreateUnicastIPAddressEntryNet.Call(uintptr(unsafe.Pointer(row)))
	return ret, callErr
}

type probeLocalSockaddrInet struct {
	Family uint16
	Port   uint16
	Data   [24]byte
}

type probeLocalIPAddressPrefix struct {
	Prefix       probeLocalSockaddrInet
	PrefixLength uint8
	Pad          [3]byte
}

type probeLocalMIBIPForwardRow2 struct {
	InterfaceLuid     uint64
	InterfaceIndex    uint32
	DestinationPrefix probeLocalIPAddressPrefix
	NextHop           probeLocalSockaddrInet
	SitePrefixLength  uint8
	Pad               [3]byte
	ValidLifetime     uint32
	PreferredLifetime uint32
	Metric            uint32
	Protocol          uint32
	Loopback          uint8
	Autoconfigure     uint8
	Publish           uint8
	Immortal          uint8
	Age               uint32
	Origin            uint32
}

type probeLocalMIBIPForwardTable2Header struct {
	NumEntries uint32
	Pad        uint32
}

type probeLocalDNSInterfaceSettings struct {
	Version             uint32
	Flags               uint64
	Domain              *uint16
	NameServer          *uint16
	SearchList          *uint16
	RegistrationEnabled uint32
	RegisterAdapterName uint32
	EnableLLMNR         uint32
	QueryAdapterName    uint32
	ProfileNameServer   *uint16
}

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

	adapter, err := probeLocalFindWindowsAdapterByIfIndex(interfaceIndex)
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
	if parsedDNS := net.ParseIP(strings.TrimSpace(probeLocalTUNInterfaceIPv4)).To4(); parsedDNS != nil {
		cleanDNS = parsedDNS.String()
	}
	adapter, err := probeLocalFindWindowsAdapterByIfIndex(interfaceIndex)
	if err != nil {
		return err
	}
	if err := probeLocalUpsertWindowsInterfaceIPv4(interfaceIndex, cleanIP, prefixLength); err != nil {
		return err
	}
	if strings.TrimSpace(adapter.AdapterGUID) == "" {
		return errors.New("adapter guid is empty")
	}
	if err := probeLocalSetWindowsInterfaceDNS(adapter.AdapterGUID, []string{cleanDNS}); err != nil {
		return err
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
		adapter, listErr := probeLocalFindWindowsAdapterByIfIndex(interfaceIndex)
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
	if err := probeLocalDeleteWindowsInterfaceIPv4(interfaceIndex, ip4.String()); err != nil {
		if !isProbeLocalIgnorableDeleteIPv4Err(err) {
			return err
		}
		logProbeWarnf("probe local tun ignored delete ipv4 repair error before recreate: if=%d ip=%s err=%v", interfaceIndex, ip4.String(), err)
	}
	return probeLocalUpsertWindowsInterfaceIPv4(interfaceIndex, ip4.String(), prefixLength)
}

func isProbeLocalIgnorableDeleteIPv4Err(err error) bool {
	if err == nil {
		return false
	}
	text := strings.ToLower(strings.TrimSpace(err.Error()))
	return strings.Contains(text, "deleteunicastipaddressentry failed: code=87") ||
		strings.Contains(text, "deleteunicastipaddressentry failed: error_invalid_parameter") ||
		strings.Contains(text, "deleteunicastipaddressentry failed: invalid parameter")
}

func upsertProbeLocalWindowsInterfaceIPv4Address(interfaceIndex int, ipText string, prefixLength int) error {
	ip4 := net.ParseIP(strings.TrimSpace(ipText)).To4()
	if ip4 == nil {
		return errors.New("invalid ipv4 address")
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
	ret, callErr := probeLocalCallSetUnicastIPAddressEntry(&row)
	if ret == 0 {
		return nil
	}
	if ret != uintptr(windows.ERROR_NOT_FOUND) && ret != uintptr(windows.ERROR_INVALID_PARAMETER) {
		return probeLocalWindowsNetapiCallErr("SetUnicastIpAddressEntry", ret, callErr)
	}
	ret, callErr = probeLocalCallCreateUnicastIPAddressEntry(&row)
	if ret != 0 && ret != uintptr(windows.ERROR_OBJECT_ALREADY_EXISTS) {
		return probeLocalWindowsNetapiCallErr("CreateUnicastIpAddressEntry", ret, callErr)
	}
	return nil
}

func deleteProbeLocalWindowsInterfaceIPv4Address(interfaceIndex int, ipText string) error {
	ip4 := net.ParseIP(strings.TrimSpace(ipText)).To4()
	if ip4 == nil {
		return errors.New("invalid ipv4 address")
	}
	var row probeLocalMIBUnicastIPAddressRow
	probeLocalProcInitializeUnicastIPAddressEntryNet.Call(uintptr(unsafe.Pointer(&row)))
	row.Address[0] = byte(windows.AF_INET)
	row.Address[1] = byte(windows.AF_INET >> 8)
	copy(row.Address[4:8], ip4)
	row.InterfaceIndex = uint32(interfaceIndex)
	ret, _, callErr := probeLocalProcDeleteUnicastIPAddressEntryNet.Call(uintptr(unsafe.Pointer(&row)))
	if ret == 0 || ret == uintptr(windows.ERROR_NOT_FOUND) {
		return nil
	}
	return probeLocalWindowsNetapiCallErr("DeleteUnicastIpAddressEntry", ret, callErr)
}

type windowsAdapterInfo struct {
	InterfaceIndex int
	Name           string
	Description    string
	AdapterGUID    string
	IPv4Addrs      []string
	DNSServers     []string
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
		adapterName := strings.TrimSpace(windows.BytePtrToString(curr.AdapterName))
		if adapterName != "" {
			item.AdapterGUID = "{" + strings.Trim(adapterName, "{}") + "}"
		}
		for uni := curr.FirstUnicastAddress; uni != nil; uni = uni.Next {
			ip := uni.Address.IP()
			if ip == nil || ip.To4() == nil {
				continue
			}
			item.IPv4Addrs = append(item.IPv4Addrs, ip.To4().String())
		}
		for dns := curr.FirstDnsServerAddress; dns != nil; dns = dns.Next {
			ip := dns.Address.IP()
			if ip == nil || ip.To4() == nil {
				continue
			}
			item.DNSServers = append(item.DNSServers, ip.To4().String())
		}
		item.IPv4Addrs = dedupeProbeLocalIPv4Strings(item.IPv4Addrs)
		item.DNSServers = dedupeProbeLocalIPv4Strings(item.DNSServers)
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

func encodeProbeLocalSockaddrInetIPv4(ipText string) (probeLocalSockaddrInet, error) {
	ip4 := net.ParseIP(strings.TrimSpace(ipText)).To4()
	if ip4 == nil {
		return probeLocalSockaddrInet{}, fmt.Errorf("invalid ipv4 address: %s", ipText)
	}
	var addr probeLocalSockaddrInet
	addr.Family = uint16(windows.AF_INET)
	copy(addr.Data[0:4], ip4)
	return addr, nil
}

func decodeProbeLocalSockaddrInetIPv4(addr probeLocalSockaddrInet) string {
	if addr.Family != uint16(windows.AF_INET) {
		return ""
	}
	ip4 := net.IPv4(addr.Data[0], addr.Data[1], addr.Data[2], addr.Data[3]).To4()
	if ip4 == nil {
		return ""
	}
	return ip4.String()
}

func probeLocalIPv4PrefixLengthFromMask(maskText string) (int, error) {
	ip4 := net.ParseIP(strings.TrimSpace(maskText)).To4()
	if ip4 == nil {
		return 0, fmt.Errorf("invalid ipv4 mask: %s", maskText)
	}
	ones, bits := net.IPMask(ip4).Size()
	if bits != 32 || ones < 0 {
		return 0, fmt.Errorf("invalid ipv4 mask: %s", maskText)
	}
	return ones, nil
}

func probeLocalWindowsNetapiCallErr(op string, ret uintptr, callErr error) error {
	if ret == 0 {
		return nil
	}
	if callErr != nil && !errors.Is(callErr, syscall.Errno(0)) {
		return fmt.Errorf("%s failed: %w", op, callErr)
	}
	return fmt.Errorf("%s failed: code=%d", op, ret)
}

func ensureProbeLocalWindowsRouteNative(routeDef probeLocalWindowsRouteDef) (bool, error) {
	prefixLength, err := probeLocalIPv4PrefixLengthFromMask(routeDef.Mask)
	if err != nil {
		return false, err
	}
	prefixAddr, err := encodeProbeLocalSockaddrInetIPv4(routeDef.Prefix)
	if err != nil {
		return false, err
	}
	nextHopAddr, err := encodeProbeLocalSockaddrInetIPv4(routeDef.Gateway)
	if err != nil {
		return false, err
	}

	var row probeLocalMIBIPForwardRow2
	probeLocalProcInitializeIpForwardEntryNet.Call(uintptr(unsafe.Pointer(&row)))
	row.InterfaceIndex = uint32(routeDef.IfIndex)
	row.DestinationPrefix.Prefix = prefixAddr
	row.DestinationPrefix.PrefixLength = uint8(prefixLength)
	row.NextHop = nextHopAddr
	row.SitePrefixLength = uint8(prefixLength)
	row.Metric = uint32(probeLocalWindowsRouteMetric)

	ret, _, callErr := probeLocalProcCreateIpForwardEntry2Net.Call(uintptr(unsafe.Pointer(&row)))
	if ret == 0 {
		return true, nil
	}
	if ret != uintptr(windows.ERROR_OBJECT_ALREADY_EXISTS) {
		return false, probeLocalWindowsNetapiCallErr("CreateIpForwardEntry2", ret, callErr)
	}
	ret, _, callErr = probeLocalProcSetIpForwardEntry2Net.Call(uintptr(unsafe.Pointer(&row)))
	if ret != 0 {
		return false, probeLocalWindowsNetapiCallErr("SetIpForwardEntry2", ret, callErr)
	}
	return false, nil
}

func deleteProbeLocalWindowsRouteNative(routeDef probeLocalWindowsRouteDef) error {
	prefixLength, err := probeLocalIPv4PrefixLengthFromMask(routeDef.Mask)
	if err != nil {
		return err
	}
	prefixAddr, err := encodeProbeLocalSockaddrInetIPv4(routeDef.Prefix)
	if err != nil {
		return err
	}
	nextHopAddr, err := encodeProbeLocalSockaddrInetIPv4(routeDef.Gateway)
	if err != nil {
		return err
	}
	var row probeLocalMIBIPForwardRow2
	row.InterfaceIndex = uint32(routeDef.IfIndex)
	row.DestinationPrefix.Prefix = prefixAddr
	row.DestinationPrefix.PrefixLength = uint8(prefixLength)
	row.NextHop = nextHopAddr
	ret, _, callErr := probeLocalProcDeleteIpForwardEntry2Net.Call(uintptr(unsafe.Pointer(&row)))
	if ret == 0 || ret == uintptr(windows.ERROR_NOT_FOUND) {
		return nil
	}
	return probeLocalWindowsNetapiCallErr("DeleteIpForwardEntry2", ret, callErr)
}

func resolveProbeLocalWindowsPrimaryEgressRouteTarget(excludedIfIndex int) (probeLocalWindowsDirectBypassRouteTarget, error) {
	var tablePtr uintptr
	ret, _, callErr := probeLocalProcGetIpForwardTable2Net.Call(uintptr(windows.AF_INET), uintptr(unsafe.Pointer(&tablePtr)))
	if ret != 0 {
		return probeLocalWindowsDirectBypassRouteTarget{}, probeLocalWindowsNetapiCallErr("GetIpForwardTable2", ret, callErr)
	}
	if tablePtr == 0 {
		return probeLocalWindowsDirectBypassRouteTarget{}, errors.New("GetIpForwardTable2 returned empty table")
	}
	defer probeLocalProcFreeMibTableNet.Call(tablePtr)

	header := (*probeLocalMIBIPForwardTable2Header)(unsafe.Pointer(tablePtr))
	rowsBase := tablePtr + unsafe.Sizeof(probeLocalMIBIPForwardTable2Header{})
	rows := unsafe.Slice((*probeLocalMIBIPForwardRow2)(unsafe.Pointer(rowsBase)), int(header.NumEntries))

	best := probeLocalWindowsDirectBypassRouteTarget{}
	bestMetric := uint32(^uint32(0))
	for _, row := range rows {
		if int(row.InterfaceIndex) <= 0 || int(row.InterfaceIndex) == excludedIfIndex {
			continue
		}
		if row.DestinationPrefix.PrefixLength != 0 {
			continue
		}
		prefixIP := decodeProbeLocalSockaddrInetIPv4(row.DestinationPrefix.Prefix)
		if prefixIP != "0.0.0.0" {
			continue
		}
		nextHop := decodeProbeLocalSockaddrInetIPv4(row.NextHop)
		if nextHop == "" || nextHop == "0.0.0.0" {
			continue
		}
		metric := row.Metric
		if best.InterfaceIndex == 0 || metric < bestMetric || (metric == bestMetric && int(row.InterfaceIndex) < best.InterfaceIndex) {
			best = probeLocalWindowsDirectBypassRouteTarget{InterfaceIndex: int(row.InterfaceIndex), NextHop: nextHop}
			bestMetric = metric
		}
	}
	if best.InterfaceIndex <= 0 || strings.TrimSpace(best.NextHop) == "" {
		return probeLocalWindowsDirectBypassRouteTarget{}, errors.New("usable ipv4 default route not found")
	}
	return best, nil
}

func probeLocalResolveWindowsPrimaryDNSServers(excludedIfIndex int) ([]string, error) {
	adapter, err := probeLocalResolveWindowsPrimaryDNSAdapter(excludedIfIndex)
	if err != nil {
		return nil, err
	}
	return dedupeProbeLocalIPv4Strings(adapter.DNSServers), nil
}

func probeLocalResolveWindowsPrimaryDNSAdapter(excludedIfIndex int) (windowsAdapterInfo, error) {
	routeTarget, err := probeLocalResolveWindowsPrimaryEgressRoute(excludedIfIndex)
	if err != nil {
		return windowsAdapterInfo{}, err
	}
	adapter, err := probeLocalFindWindowsAdapterByIfIndex(routeTarget.InterfaceIndex)
	if err != nil {
		return windowsAdapterInfo{}, err
	}
	return adapter, nil
}

func snapshotProbeLocalWindowsIPv4Routes() (string, error) {
	var tablePtr uintptr
	ret, _, callErr := probeLocalProcGetIpForwardTable2Net.Call(uintptr(windows.AF_INET), uintptr(unsafe.Pointer(&tablePtr)))
	if ret != 0 {
		return "", probeLocalWindowsNetapiCallErr("GetIpForwardTable2", ret, callErr)
	}
	if tablePtr == 0 {
		return "", errors.New("GetIpForwardTable2 returned empty table")
	}
	defer probeLocalProcFreeMibTableNet.Call(tablePtr)
	header := (*probeLocalMIBIPForwardTable2Header)(unsafe.Pointer(tablePtr))
	return fmt.Sprintf("ipv4_routes=%d", header.NumEntries), nil
}

func setProbeLocalWindowsInterfaceDNS(interfaceGUID string, dnsServers []string) error {
	cleanGUID := strings.TrimSpace(interfaceGUID)
	if cleanGUID == "" {
		return errors.New("empty interface guid")
	}
	guid, err := windows.GUIDFromString(cleanGUID)
	if err != nil {
		return fmt.Errorf("invalid interface guid: %w", err)
	}
	items := make([]string, 0, len(dnsServers))
	for _, item := range dnsServers {
		ip4 := net.ParseIP(strings.TrimSpace(item)).To4()
		if ip4 == nil {
			continue
		}
		items = append(items, ip4.String())
	}
	if len(items) == 0 {
		return errors.New("empty dns servers")
	}
	nameServerPtr, err := syscall.UTF16PtrFromString(strings.Join(items, ","))
	if err != nil {
		return fmt.Errorf("encode dns servers failed: %w", err)
	}
	settings := probeLocalDNSInterfaceSettings{
		Version:             1,
		Flags:               0x0002 | 0x0008 | 0x0010,
		NameServer:          nameServerPtr,
		RegistrationEnabled: 0,
		RegisterAdapterName: 0,
	}
	ret, _, callErr := probeLocalProcSetInterfaceDnsSettingsNet.Call(uintptr(unsafe.Pointer(&guid)), uintptr(unsafe.Pointer(&settings)))
	if ret != 0 {
		return probeLocalWindowsNetapiCallErr("SetInterfaceDnsSettings", ret, callErr)
	}
	return nil
}
