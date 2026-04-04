//go:build windows

package backend

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
	"golang.org/x/sys/windows/registry"
)

const (
	windowsErrorInsufficientBuffer = 122
	windowsErrorAlreadyExists      = 183
	windowsRouteProtoNetMgmt       = 3
	windowsRouteTypeDirect         = 3 // MIB_IPROUTE_TYPE_DIRECT  — next hop IS the destination (on-link)
	windowsRouteTypeIndirect       = 4 // MIB_IPROUTE_TYPE_INDIRECT — next hop is a gateway
)

type mibIPForwardRow struct {
	ForwardDest      uint32
	ForwardMask      uint32
	ForwardPolicy    uint32
	ForwardNextHop   uint32
	ForwardIfIndex   uint32
	ForwardType      uint32
	ForwardProto     uint32
	ForwardAge       uint32
	ForwardNextHopAS uint32
	ForwardMetric1   uint32
	ForwardMetric2   uint32
	ForwardMetric3   uint32
	ForwardMetric4   uint32
	ForwardMetric5   uint32
}

type mibIPForwardTable struct {
	NumEntries uint32
	Table      [1]mibIPForwardRow
}

var (
	modIphlpapiNet                          = windows.NewLazySystemDLL("iphlpapi.dll")
	procGetIpForwardTableNet                = modIphlpapiNet.NewProc("GetIpForwardTable")
	procCreateIpForwardEntryNet             = modIphlpapiNet.NewProc("CreateIpForwardEntry")
	procDeleteIpForwardEntryNet             = modIphlpapiNet.NewProc("DeleteIpForwardEntry")
	procGetBestRouteNet                     = modIphlpapiNet.NewProc("GetBestRoute")
	procCreateUnicastIpAddressEntryNet      = modIphlpapiNet.NewProc("CreateUnicastIpAddressEntry")
	procInitializeUnicastIpAddressEntryNet  = modIphlpapiNet.NewProc("InitializeUnicastIpAddressEntry")
	procInitializeIpForwardEntryNet         = modIphlpapiNet.NewProc("InitializeIpForwardEntry")
	procCreateIpForwardEntry2Net            = modIphlpapiNet.NewProc("CreateIpForwardEntry2")
	procDeleteIpForwardEntry2Net            = modIphlpapiNet.NewProc("DeleteIpForwardEntry2")
	procGetIpForwardTable2Net               = modIphlpapiNet.NewProc("GetIpForwardTable2")
	procFreeMibTableNet                     = modIphlpapiNet.NewProc("FreeMibTable")
	procConvertInterfaceIndexToLuidNet      = modIphlpapiNet.NewProc("ConvertInterfaceIndexToLuid")
)

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
		if errors.Is(errCode, syscall.Errno(windowsErrorInsufficientBuffer)) {
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
			InterfaceIndex:       int(curr.IfIndex),
			Name:                 strings.TrimSpace(windows.UTF16PtrToString(curr.FriendlyName)),
			InterfaceDescription: strings.TrimSpace(windows.UTF16PtrToString(curr.Description)),
		}
		adapterName := strings.TrimSpace(windows.BytePtrToString(curr.AdapterName))
		if adapterName != "" {
			item.AdapterGUID = "{" + strings.Trim(adapterName, "{}") + "}"
		}
		for dns := curr.FirstDnsServerAddress; dns != nil; dns = dns.Next {
			ip := dns.Address.IP()
			if ip == nil || ip.To4() == nil {
				continue
			}
			item.PreviousDNSServers = append(item.PreviousDNSServers, ip.To4().String())
		}
		item.PreviousDNSServers = dedupeIPv4Strings(item.PreviousDNSServers)
		for uni := curr.FirstUnicastAddress; uni != nil; uni = uni.Next {
			ip := uni.Address.IP()
			if ip == nil || ip.To4() == nil {
				continue
			}
			item.IPv4Addrs = append(item.IPv4Addrs, ip.To4().String())
		}
		item.IPv4Addrs = dedupeIPv4Strings(item.IPv4Addrs)
		items = append(items, item)
	}
	return items
}

func windowsFindAdapterByNameOrDescription(name, description string) (windowsAdapterInfo, error) {
	items, err := windowsListAdaptersIPv4()
	if err != nil {
		return windowsAdapterInfo{}, err
	}
	cleanName := strings.TrimSpace(name)
	cleanDesc := strings.TrimSpace(description)
	for _, item := range items {
		if cleanName != "" && strings.EqualFold(strings.TrimSpace(item.Name), cleanName) {
			return item, nil
		}
		if cleanDesc != "" && strings.EqualFold(strings.TrimSpace(item.InterfaceDescription), cleanDesc) {
			return item, nil
		}
	}
	return windowsAdapterInfo{}, fmt.Errorf("tun adapter not found: %s", cleanName)
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

func windowsSetInterfaceIPv4DNSServers(interfaceIndex int, servers []string) error {
	adapter, err := windowsFindAdapterByIfIndex(interfaceIndex)
	if err != nil {
		return err
	}
	guid := strings.TrimSpace(adapter.AdapterGUID)
	if guid == "" {
		return fmt.Errorf("adapter guid is empty for interface index: %d", interfaceIndex)
	}
	path := `SYSTEM\CurrentControlSet\Services\Tcpip\Parameters\Interfaces\` + guid
	key, err := registry.OpenKey(registry.LOCAL_MACHINE, path, registry.SET_VALUE)
	if err != nil {
		return err
	}
	defer key.Close()

	cleanServers := dedupeIPv4Strings(servers)
	nameServer := strings.Join(cleanServers, ",")
	if err := key.SetStringValue("NameServer", nameServer); err != nil {
		return err
	}
	return nil
}

// mibUnicastIPAddressRow mirrors MIB_UNICASTIPADDRESS_ROW from netioapi.h.
//
// Memory layout on x64 (Windows SDK):
//   Offset  0: SOCKADDR_INET Address         — 28 bytes (size of SOCKADDR_IN6)
//   Offset 28: [4]byte pad                   —  4 bytes to align NET_LUID to 8
//   Offset 32: NET_LUID  InterfaceLuid       —  8 bytes
//   Offset 40: NET_IFINDEX InterfaceIndex    —  4 bytes (ULONG)
//   Offset 44: NL_PREFIX_ORIGIN PrefixOrigin —  4 bytes
//   Offset 48: NL_SUFFIX_ORIGIN SuffixOrigin —  4 bytes
//   Offset 52: ULONG ValidLifetime           —  4 bytes
//   Offset 56: ULONG PreferredLifetime       —  4 bytes
//   Offset 60: UINT8 OnLinkPrefixLength      —  1 byte
//   Offset 61: BOOLEAN SkipAsSource          —  1 byte
//   Offset 62: [2]byte pad                   —  2 bytes to align DadState to 4
//   Offset 64: NL_DAD_STATE DadState         —  4 bytes
//   Offset 68: SCOPE_ID ScopeId              —  4 bytes
//   Offset 72: LARGE_INTEGER CreationTimeStamp— 8 bytes
//   Total: 80 bytes
type mibUnicastIPAddressRow struct {
	Address            [28]byte // SOCKADDR_INET (SOCKADDR_IN6 is the largest member = 28 bytes)
	_pad0              [4]byte  // align InterfaceLuid to 8
	InterfaceLuid      uint64
	InterfaceIndex     uint32
	PrefixOrigin       uint32
	SuffixOrigin       uint32
	ValidLifetime      uint32
	PreferredLifetime  uint32
	OnLinkPrefixLength uint8
	SkipAsSource       uint8
	_pad1              [2]byte // align DadState to 4
	DadState           uint32
	ScopeId            uint32
	CreationTimeStamp  int64
}

type windowsIPAddressPrefix struct {
	Prefix       [28]byte
	PrefixLength uint8
	_pad         [3]byte
}

type mibIPForwardRow2 struct {
	InterfaceLUID        uint64
	InterfaceIndex       uint32
	DestinationPrefix    windowsIPAddressPrefix
	NextHop              [28]byte
	SitePrefixLength     uint8
	_pad0                [3]byte
	ValidLifetime        uint32
	PreferredLifetime    uint32
	Metric               uint32
	Protocol             uint32
	Loopback             uint8
	AutoconfigureAddress uint8
	Publish              uint8
	Immortal             uint8
	Age                  uint32
	Origin               uint32
}

type mibIPForwardTable2 struct {
	NumEntries uint32
	Table      [1]mibIPForwardRow2
}

type windowsIPv4RouteEntry struct {
	InterfaceIndex int
	Prefix         string
	Destination    net.IP
	PrefixLength   int
	NextHop        net.IP
	Metric         uint32
	Protocol       uint32
	Loopback       bool
	Autoconf       bool
}

func windowsEnsureInterfaceIPv4Address(interfaceIndex int, ipText string, prefixLength int) error {
	if interfaceIndex <= 0 {
		return errors.New("invalid interface index")
	}
	ip4 := net.ParseIP(strings.TrimSpace(ipText)).To4()
	if ip4 == nil {
		return errors.New("invalid ipv4 address")
	}
	// Check whether the address is already present to avoid a redundant syscall.
	// Even if the address already exists, still wait until it is bindable to avoid
	// racing with Windows interface/address convergence.
	adapter, err := windowsFindAdapterByIfIndex(interfaceIndex)
	if err == nil {
		for _, existing := range adapter.IPv4Addrs {
			if strings.EqualFold(strings.TrimSpace(existing), ip4.String()) {
				return waitForWindowsInterfaceIPv4Bindable(interfaceIndex, ip4, 5*time.Second)
			}
		}
	}

	// Use CreateUnicastIpAddressEntry (Vista+) which accepts an interface index
	// directly.  The legacy AddIPAddress API expects an NTE context handle, NOT
	// the interface index, so passing the index there yields ERROR_INVALID_PARAMETER
	// (code=87).
	var row mibUnicastIPAddressRow
	// InitializeUnicastIpAddressEntry fills in the mandatory default fields.
	procInitializeUnicastIpAddressEntryNet.Call(uintptr(unsafe.Pointer(&row)))

	// Encode the IPv4 address as a SOCKADDR_IN inside the SOCKADDR_INET union.
	// SOCKADDR_IN layout: sin_family (2) | sin_port (2) | sin_addr (4) | padding (8)
	row.Address[0] = byte(windows.AF_INET)
	row.Address[1] = byte(windows.AF_INET >> 8)
	// sin_port = 0 (bytes 2-3 already zero from initializer)
	copy(row.Address[4:8], ip4) // sin_addr

	row.InterfaceIndex = uint32(interfaceIndex)
	row.OnLinkPrefixLength = uint8(prefixLength)
	// Use well-known infinite lifetimes (0xFFFFFFFF) so the address persists
	// until the adapter is reset; InitializeUnicastIpAddressEntry already sets
	// these, but be explicit for clarity.
	row.ValidLifetime = 0xFFFFFFFF
	row.PreferredLifetime = 0xFFFFFFFF

	ret, _, callErr := procCreateUnicastIpAddressEntryNet.Call(uintptr(unsafe.Pointer(&row)))
	if ret != 0 && ret != windowsErrorAlreadyExists {
		if callErr != nil && !errors.Is(callErr, syscall.Errno(0)) {
			return fmt.Errorf("CreateUnicastIpAddressEntry failed: %w", callErr)
		}
		return fmt.Errorf("CreateUnicastIpAddressEntry failed: code=%d", ret)
	}

	return waitForWindowsInterfaceIPv4Bindable(interfaceIndex, ip4, 5*time.Second)
}

func waitForWindowsInterfaceIPv4Bindable(interfaceIndex int, ip4 net.IP, timeout time.Duration) error {
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
			if !isListenAddrNotAvailableError(bindErr) {
				return bindErr
			}
		}
		if time.Now().After(deadline) {
			return fmt.Errorf("ipv4 address not bindable in time: if=%d ip=%s", interfaceIndex, cleanIP)
		}
		time.Sleep(100 * time.Millisecond)
	}
}

func windowsDetectPrimaryIPv4Route() (windowsRouteInfo, error) {
	probeTargets := []string{"223.5.5.5", "119.29.29.29"}
	var lastErr error
	for _, probeIP := range probeTargets {
		dest, ok := ipv4ToUint32(net.ParseIP(probeIP).To4())
		if !ok {
			lastErr = errors.New("build best route destination failed")
			continue
		}
		var row mibIPForwardRow
		ret, _, callErr := procGetBestRouteNet.Call(uintptr(dest), uintptr(0), uintptr(unsafe.Pointer(&row)))
		if ret != 0 {
			if callErr != nil && !errors.Is(callErr, syscall.Errno(0)) {
				lastErr = fmt.Errorf("GetBestRoute failed via %s: %w", probeIP, callErr)
				continue
			}
			lastErr = fmt.Errorf("GetBestRoute failed via %s: code=%d", probeIP, ret)
			continue
		}
		nextHop := strings.TrimSpace(uint32ToIPv4(row.ForwardNextHop))
		if row.ForwardIfIndex == 0 || nextHop == "" {
			lastErr = fmt.Errorf("usable ipv4 default route not found via %s", probeIP)
			continue
		}
		// Some Windows environments legitimately return on-link default route with
		// next hop 0.0.0.0. Treat it as usable as long as interface index is valid.
		if net.ParseIP(nextHop).To4() == nil {
			lastErr = fmt.Errorf("usable ipv4 default route not found via %s", probeIP)
			continue
		}
		return windowsRouteInfo{InterfaceIndex: int(row.ForwardIfIndex), NextHop: nextHop}, nil
	}
	if lastErr != nil {
		return windowsRouteInfo{}, lastErr
	}
	return windowsRouteInfo{}, errors.New("usable ipv4 default route not found")
}

func windowsDetectPrimaryIPv4RouteExcludingInterface(excludedIfIndex int) (windowsRouteInfo, error) {
	egress, err := windowsDetectPrimaryIPv4Route()
	if excludedIfIndex <= 0 {
		return egress, err
	}
	if err == nil && egress.InterfaceIndex != excludedIfIndex {
		return egress, nil
	}

	rows, listErr := windowsListIPForwardRows()
	if listErr != nil {
		if err != nil {
			return windowsRouteInfo{}, err
		}
		return windowsRouteInfo{}, listErr
	}

	bestFound := false
	var bestRow mibIPForwardRow
	for _, row := range rows {
		if row.ForwardDest != 0 || row.ForwardMask != 0 {
			continue
		}
		if row.ForwardIfIndex == 0 || int(row.ForwardIfIndex) == excludedIfIndex {
			continue
		}
		nextHop := strings.TrimSpace(uint32ToIPv4(row.ForwardNextHop))
		if net.ParseIP(nextHop).To4() == nil {
			continue
		}
		if !bestFound || row.ForwardMetric1 < bestRow.ForwardMetric1 {
			bestRow = row
			bestFound = true
		}
	}
	if !bestFound {
		if err != nil {
			return windowsRouteInfo{}, err
		}
		return windowsRouteInfo{}, fmt.Errorf("usable ipv4 default route not found (excluding if=%d)", excludedIfIndex)
	}
	return windowsRouteInfo{
		InterfaceIndex: int(bestRow.ForwardIfIndex),
		NextHop:        strings.TrimSpace(uint32ToIPv4(bestRow.ForwardNextHop)),
	}, nil
}

func windowsEnsureIPv4Route(prefix string, interfaceIndex int, nextHop string, metric uint32) error {
	cleanPrefix := strings.TrimSpace(prefix)
	cleanHop := strings.TrimSpace(nextHop)
	if cleanPrefix == "" {
		return errors.New("empty route prefix")
	}
	if interfaceIndex <= 0 {
		return errors.New("invalid route interface")
	}
	if cleanHop == "" {
		return errors.New("empty route next hop")
	}
	_, network, err := net.ParseCIDR(cleanPrefix)
	if err != nil || network == nil {
		return fmt.Errorf("invalid ipv4 cidr prefix: %s", cleanPrefix)
	}
	destIP := network.IP.To4()
	if destIP == nil {
		return fmt.Errorf("invalid ipv4 cidr prefix: %s", cleanPrefix)
	}
	hopIP := net.ParseIP(cleanHop).To4()
	if hopIP == nil {
		return errors.New("invalid route next hop")
	}
	if err := windowsDeleteIPv4Routes2(cleanPrefix, uint32(interfaceIndex), hopIP); err != nil {
		return err
	}
	if err := windowsDeleteIPv4RoutesLegacy(cleanPrefix, uint32(interfaceIndex), hopIP); err != nil {
		return err
	}

	var row mibIPForwardRow2
	procInitializeIpForwardEntryNet.Call(uintptr(unsafe.Pointer(&row)))
	row.InterfaceIndex = uint32(interfaceIndex)
	if luid, err := windowsConvertInterfaceIndexToLuid(row.InterfaceIndex); err == nil {
		row.InterfaceLUID = luid
	}
	setWindowsRawSockaddrInetIPv4(&row.DestinationPrefix.Prefix, destIP)
	ones, _ := network.Mask.Size()
	row.DestinationPrefix.PrefixLength = uint8(ones)
	setWindowsRawSockaddrInetIPv4(&row.NextHop, hopIP)
	row.Metric = metric

	destText := destIP.String()
	ret, _, callErr := procCreateIpForwardEntry2Net.Call(uintptr(unsafe.Pointer(&row)))
	if ret != 0 {
		if callErr != nil && !errors.Is(callErr, syscall.Errno(0)) {
			return fmt.Errorf("CreateIpForwardEntry2 failed: prefix=%s if=%d next_hop=%s dest=%s metric=%d ret=%d err=%w", cleanPrefix, interfaceIndex, cleanHop, destText, metric, ret, callErr)
		}
		return fmt.Errorf("CreateIpForwardEntry2 failed: prefix=%s if=%d next_hop=%s dest=%s metric=%d code=%d", cleanPrefix, interfaceIndex, cleanHop, destText, metric, ret)
	}
	return nil
}

func windowsConvertInterfaceIndexToLuid(interfaceIndex uint32) (uint64, error) {
	var luid uint64
	ret, _, callErr := procConvertInterfaceIndexToLuidNet.Call(uintptr(interfaceIndex), uintptr(unsafe.Pointer(&luid)))
	if ret != 0 {
		if callErr != nil && !errors.Is(callErr, syscall.Errno(0)) {
			return 0, callErr
		}
		return 0, syscall.Errno(ret)
	}
	return luid, nil
}

func setWindowsRawSockaddrInetIPv4(target *[28]byte, ip4 net.IP) {
	for i := range target {
		target[i] = 0
	}
	target[0] = byte(windows.AF_INET)
	target[1] = byte(windows.AF_INET >> 8)
	copy(target[4:8], ip4.To4())
}

func windowsRawSockaddrInetIPv4(addr [28]byte) net.IP {
	if uint16(addr[0])|uint16(addr[1])<<8 != windows.AF_INET {
		return nil
	}
	return net.IPv4(addr[4], addr[5], addr[6], addr[7]).To4()
}

func windowsDeleteIPv4Route(prefix string, interfaceIndex int, nextHop string, strictNextHop bool) error {
	cleanPrefix := strings.TrimSpace(prefix)
	if cleanPrefix == "" || interfaceIndex <= 0 {
		return nil
	}
	dest, mask, err := parseIPv4CIDRToDestMask(cleanPrefix)
	if err != nil {
		return nil
	}
	if strictNextHop {
		hop, ok := ipv4ToUint32(net.ParseIP(strings.TrimSpace(nextHop)).To4())
		if !ok {
			return nil
		}
		return windowsDeleteIPv4Routes(dest, mask, uint32(interfaceIndex), &hop)
	}
	return windowsDeleteIPv4Routes(dest, mask, uint32(interfaceIndex), nil)
}

func windowsDeleteIPv4Routes(dest, mask uint32, ifIndex uint32, nextHop *uint32) error {
	prefix := net.IPv4(byte(dest>>24), byte(dest>>16), byte(dest>>8), byte(dest)).String()
	maskIP := net.IPv4(byte(mask>>24), byte(mask>>16), byte(mask>>8), byte(mask))
	ones, _ := net.IPMask(maskIP.To4()).Size()
	cidr := fmt.Sprintf("%s/%d", prefix, ones)
	var hopIP net.IP
	if nextHop != nil {
		hopIP = net.IPv4(byte(*nextHop>>24), byte(*nextHop>>16), byte(*nextHop>>8), byte(*nextHop)).To4()
	}
	if err := windowsDeleteIPv4Routes2(cidr, ifIndex, hopIP); err != nil {
		return err
	}
	return windowsDeleteIPv4RoutesLegacy(cidr, ifIndex, hopIP)
}

func windowsDeleteIPv4Routes2(prefix string, ifIndex uint32, hopIP net.IP) error {
	rows, err := windowsListIPForwardRows2()
	if err != nil {
		return err
	}
	_, network, parseErr := net.ParseCIDR(strings.TrimSpace(prefix))
	if parseErr != nil || network == nil {
		return parseErr
	}
	ones, _ := network.Mask.Size()
	dest := network.IP.To4()
	if dest == nil {
		return nil
	}

	var allErr error
	for _, row := range rows {
		if row.InterfaceIndex != ifIndex {
			continue
		}
		rowDest := windowsRawSockaddrInetIPv4(row.DestinationPrefix.Prefix)
		if rowDest == nil || !rowDest.Equal(dest) {
			continue
		}
		if int(row.DestinationPrefix.PrefixLength) != ones {
			continue
		}
		if hopIP != nil {
			rowHop := windowsRawSockaddrInetIPv4(row.NextHop)
			if rowHop == nil || !rowHop.Equal(hopIP.To4()) {
				continue
			}
		}
		ret, _, callErr := procDeleteIpForwardEntry2Net.Call(uintptr(unsafe.Pointer(&row)))
		if ret != 0 {
			if callErr != nil && !errors.Is(callErr, syscall.Errno(0)) {
				allErr = errors.Join(allErr, fmt.Errorf("DeleteIpForwardEntry2 failed: %w", callErr))
			} else {
				allErr = errors.Join(allErr, fmt.Errorf("DeleteIpForwardEntry2 failed: code=%d", ret))
			}
		}
	}
	return allErr
}

func windowsDeleteIPv4RoutesLegacy(prefix string, ifIndex uint32, hopIP net.IP) error {
	dest, mask, err := parseIPv4CIDRToDestMask(prefix)
	if err != nil {
		return err
	}
	rows, err := windowsListIPForwardRows()
	if err != nil {
		return err
	}
	var hopUint *uint32
	if hopIP != nil {
		if value, ok := ipv4ToUint32(hopIP.To4()); ok {
			hopUint = &value
		}
	}
	var allErr error
	for _, row := range rows {
		if row.ForwardIfIndex != ifIndex || row.ForwardDest != dest || row.ForwardMask != mask {
			continue
		}
		if hopUint != nil && row.ForwardNextHop != *hopUint {
			continue
		}
		ret, _, callErr := procDeleteIpForwardEntryNet.Call(uintptr(unsafe.Pointer(&row)))
		if ret != 0 {
			if callErr != nil && !errors.Is(callErr, syscall.Errno(0)) {
				allErr = errors.Join(allErr, fmt.Errorf("DeleteIpForwardEntry failed: %w", callErr))
			} else {
				allErr = errors.Join(allErr, fmt.Errorf("DeleteIpForwardEntry failed: code=%d", ret))
			}
		}
	}
	return allErr
}

func windowsListIPForwardRows2() ([]mibIPForwardRow2, error) {
	var tablePtr uintptr
	ret, _, callErr := procGetIpForwardTable2Net.Call(uintptr(windows.AF_INET), uintptr(unsafe.Pointer(&tablePtr)))
	if ret != 0 {
		if callErr != nil && !errors.Is(callErr, syscall.Errno(0)) {
			return nil, fmt.Errorf("GetIpForwardTable2 failed: %w", callErr)
		}
		return nil, fmt.Errorf("GetIpForwardTable2 failed: code=%d", ret)
	}
	if tablePtr == 0 {
		return []mibIPForwardRow2{}, nil
	}
	defer procFreeMibTableNet.Call(tablePtr)
	tbl := (*mibIPForwardTable2)(unsafe.Pointer(tablePtr))
	count := int(tbl.NumEntries)
	if count <= 0 {
		return []mibIPForwardRow2{}, nil
	}
	rows := make([]mibIPForwardRow2, 0, count)
	rowSize := unsafe.Sizeof(mibIPForwardRow2{})
	base := unsafe.Pointer(&tbl.Table[0])
	for idx := 0; idx < count; idx++ {
		row := *(*mibIPForwardRow2)(unsafe.Add(base, uintptr(idx)*rowSize))
		rows = append(rows, row)
	}
	return rows, nil
}

func windowsListIPv4RouteEntries2() ([]windowsIPv4RouteEntry, error) {
	rows, err := windowsListIPForwardRows2()
	if err != nil {
		return nil, err
	}
	entries := make([]windowsIPv4RouteEntry, 0, len(rows))
	for _, row := range rows {
		destination := windowsRawSockaddrInetIPv4(row.DestinationPrefix.Prefix)
		if destination == nil {
			continue
		}
		nextHop := windowsRawSockaddrInetIPv4(row.NextHop)
		prefixLength := int(row.DestinationPrefix.PrefixLength)
		entries = append(entries, windowsIPv4RouteEntry{
			InterfaceIndex: int(row.InterfaceIndex),
			Prefix:         fmt.Sprintf("%s/%d", destination.String(), prefixLength),
			Destination:    destination,
			PrefixLength:   prefixLength,
			NextHop:        nextHop,
			Metric:         row.Metric,
			Protocol:       row.Protocol,
			Loopback:       row.Loopback != 0,
			Autoconf:       row.AutoconfigureAddress != 0,
		})
	}
	return entries, nil
}

func windowsShouldPreserveIPv4StaticRoute(entry windowsIPv4RouteEntry, tunAdapterIfIndex int) bool {
	if entry.PrefixLength == 0 {
		return true
	}
	if entry.Loopback || isIPv4InCIDR(entry.Destination, "127.0.0.0/8") {
		return true
	}
	if isIPv4InCIDR(entry.Destination, "169.254.0.0/16") {
		return true
	}
	if entry.NextHop != nil && entry.NextHop.Equal(net.IPv4zero) {
		if tunAdapterIfIndex > 0 && entry.InterfaceIndex == tunAdapterIfIndex {
			return false
		}
		return true
	}
	return false
}

func windowsClearIPv4StaticRoutesForTUN() error {
	tunAdapterIfIndex := 0
	if adapter, err := windowsFindAdapterByNameOrDescription(tunAdapterName, tunAdapterDescription); err == nil {
		tunAdapterIfIndex = adapter.InterfaceIndex
	}
	entries, err := windowsListIPv4RouteEntries2()
	if err != nil {
		return err
	}
	var allErr error
	for _, entry := range entries {
		if entry.InterfaceIndex <= 0 || entry.Protocol != windowsRouteProtoNetMgmt {
			continue
		}
		if windowsShouldPreserveIPv4StaticRoute(entry, tunAdapterIfIndex) {
			continue
		}
		nextHopText := "0.0.0.0"
		if entry.NextHop != nil {
			nextHopText = entry.NextHop.String()
		}
		if err := windowsDeleteIPv4Route(entry.Prefix, entry.InterfaceIndex, nextHopText, true); err != nil {
			allErr = errors.Join(allErr, err)
		}
	}
	return allErr
}

func isIPv4InCIDR(ip net.IP, cidr string) bool {
	ip4 := ip.To4()
	if ip4 == nil {
		return false
	}
	_, network, err := net.ParseCIDR(strings.TrimSpace(cidr))
	if err != nil || network == nil {
		return false
	}
	return network.Contains(ip4)
}

func windowsListIPForwardRows() ([]mibIPForwardRow, error) {
	var size uint32 = 16 * 1024
	buf := make([]byte, size)
	for i := 0; i < 3; i++ {
		ret, _, callErr := procGetIpForwardTableNet.Call(
			uintptr(unsafe.Pointer(&buf[0])),
			uintptr(unsafe.Pointer(&size)),
			uintptr(1),
		)
		if ret == 0 {
			tbl := (*mibIPForwardTable)(unsafe.Pointer(&buf[0]))
			count := int(tbl.NumEntries)
			if count <= 0 {
				return []mibIPForwardRow{}, nil
			}
			rows := make([]mibIPForwardRow, 0, count)
			rowSize := unsafe.Sizeof(mibIPForwardRow{})
			base := unsafe.Pointer(&tbl.Table[0])
			for idx := 0; idx < count; idx++ {
				row := *(*mibIPForwardRow)(unsafe.Add(base, uintptr(idx)*rowSize))
				rows = append(rows, row)
			}
			return rows, nil
		}
		if ret == windowsErrorInsufficientBuffer {
			buf = make([]byte, size)
			continue
		}
		if callErr != nil && !errors.Is(callErr, syscall.Errno(0)) {
			return nil, fmt.Errorf("GetIpForwardTable failed: %w", callErr)
		}
		return nil, fmt.Errorf("GetIpForwardTable failed: code=%d", ret)
	}
	return nil, errors.New("GetIpForwardTable failed after retries")
}

func parseIPv4CIDRToDestMask(prefix string) (uint32, uint32, error) {
	_, network, err := net.ParseCIDR(strings.TrimSpace(prefix))
	if err != nil || network == nil {
		return 0, 0, fmt.Errorf("invalid ipv4 cidr prefix: %s", prefix)
	}
	ip4 := network.IP.To4()
	if ip4 == nil {
		return 0, 0, fmt.Errorf("invalid ipv4 cidr prefix: %s", prefix)
	}
	mask := net.IP(network.Mask).To4()
	if mask == nil {
		return 0, 0, fmt.Errorf("invalid ipv4 cidr mask: %s", prefix)
	}
	dest, ok := ipv4ToUint32(ip4)
	if !ok {
		return 0, 0, errors.New("convert ipv4 dest failed")
	}
	maskUint, ok := ipv4ToUint32(mask)
	if !ok {
		return 0, 0, errors.New("convert ipv4 mask failed")
	}
	return dest, maskUint, nil
}

func ipv4MaskUint32(prefixLength int) (uint32, error) {
	if prefixLength < 0 || prefixLength > 32 {
		return 0, errors.New("invalid prefix length")
	}
	mask := net.CIDRMask(prefixLength, 32)
	maskIP := net.IP(mask).To4()
	if maskIP == nil {
		return 0, errors.New("invalid prefix mask")
	}
	value, ok := ipv4ToUint32(maskIP)
	if !ok {
		return 0, errors.New("convert prefix mask failed")
	}
	return value, nil
}

func ipv4ToUint32(ip net.IP) (uint32, bool) {
	ip4 := ip.To4()
	if ip4 == nil {
		return 0, false
	}
	// Windows MIB_IPFORWARDROW IPv4 fields are DWORD values interpreted in host order.
	// On little-endian Windows, using LittleEndian here keeps compatibility with
	// GetBestRoute / GetIpForwardTable / DeleteIpForwardEntry legacy structures.
	return binary.LittleEndian.Uint32(ip4), true
}

func uint32ToIPv4(value uint32) string {
	var b [4]byte
	binary.LittleEndian.PutUint32(b[:], value)
	return net.IPv4(b[0], b[1], b[2], b[3]).String()
}

func dedupeIPv4Strings(items []string) []string {
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
