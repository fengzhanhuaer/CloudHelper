//go:build windows

package backend

import (
	"encoding/binary"
	"errors"
	"fmt"
	"net"
	"strings"
	"syscall"
	"unsafe"

	"golang.org/x/sys/windows"
	"golang.org/x/sys/windows/registry"
)

const (
	windowsErrorInsufficientBuffer = 122
	windowsRouteProtoNetMgmt       = 3
	windowsRouteTypeIndirect       = 4
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
	modIphlpapiNet                 = windows.NewLazySystemDLL("iphlpapi.dll")
	procGetIpForwardTableNet       = modIphlpapiNet.NewProc("GetIpForwardTable")
	procCreateIpForwardEntryNet    = modIphlpapiNet.NewProc("CreateIpForwardEntry")
	procDeleteIpForwardEntryNet    = modIphlpapiNet.NewProc("DeleteIpForwardEntry")
	procGetBestRouteNet            = modIphlpapiNet.NewProc("GetBestRoute")
	procAddIPAddressNet            = modIphlpapiNet.NewProc("AddIPAddress")
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

func windowsEnsureInterfaceIPv4Address(interfaceIndex int, ipText string, prefixLength int) error {
	if interfaceIndex <= 0 {
		return errors.New("invalid interface index")
	}
	ip4 := net.ParseIP(strings.TrimSpace(ipText)).To4()
	if ip4 == nil {
		return errors.New("invalid ipv4 address")
	}
	adapter, err := windowsFindAdapterByIfIndex(interfaceIndex)
	if err == nil {
		for _, existing := range adapter.IPv4Addrs {
			if strings.EqualFold(strings.TrimSpace(existing), ip4.String()) {
				return nil
			}
		}
	}
	maskValue, err := ipv4MaskUint32(prefixLength)
	if err != nil {
		return err
	}
	addrValue, ok := ipv4ToUint32(ip4)
	if !ok {
		return errors.New("convert ipv4 address failed")
	}
	var nteContext uint32
	var nteInstance uint32
	ret, _, callErr := procAddIPAddressNet.Call(
		uintptr(addrValue),
		uintptr(maskValue),
		uintptr(uint32(interfaceIndex)),
		uintptr(unsafe.Pointer(&nteContext)),
		uintptr(unsafe.Pointer(&nteInstance)),
	)
	if ret != 0 {
		if callErr != nil && !errors.Is(callErr, syscall.Errno(0)) {
			return fmt.Errorf("AddIPAddress failed: %w", callErr)
		}
		return fmt.Errorf("AddIPAddress failed: code=%d", ret)
	}
	return nil
}

func windowsDetectPrimaryIPv4Route() (windowsRouteInfo, error) {
	dest, ok := ipv4ToUint32(net.ParseIP("1.1.1.1").To4())
	if !ok {
		return windowsRouteInfo{}, errors.New("build best route destination failed")
	}
	var row mibIPForwardRow
	ret, _, callErr := procGetBestRouteNet.Call(uintptr(dest), uintptr(0), uintptr(unsafe.Pointer(&row)))
	if ret != 0 {
		if callErr != nil && !errors.Is(callErr, syscall.Errno(0)) {
			return windowsRouteInfo{}, fmt.Errorf("GetBestRoute failed: %w", callErr)
		}
		return windowsRouteInfo{}, fmt.Errorf("GetBestRoute failed: code=%d", ret)
	}
	nextHop := uint32ToIPv4(row.ForwardNextHop)
	if row.ForwardIfIndex == 0 || strings.TrimSpace(nextHop) == "" || nextHop == "0.0.0.0" {
		return windowsRouteInfo{}, errors.New("usable ipv4 default route not found")
	}
	return windowsRouteInfo{InterfaceIndex: int(row.ForwardIfIndex), NextHop: nextHop}, nil
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
	dest, mask, err := parseIPv4CIDRToDestMask(cleanPrefix)
	if err != nil {
		return err
	}
	hop, ok := ipv4ToUint32(net.ParseIP(cleanHop).To4())
	if !ok {
		return errors.New("invalid route next hop")
	}
	if err := windowsDeleteIPv4Routes(dest, mask, uint32(interfaceIndex), &hop); err != nil {
		return err
	}
	row := mibIPForwardRow{
		ForwardDest:    dest,
		ForwardMask:    mask,
		ForwardNextHop: hop,
		ForwardIfIndex: uint32(interfaceIndex),
		ForwardType:    windowsRouteTypeIndirect,
		ForwardProto:   windowsRouteProtoNetMgmt,
		ForwardMetric1: metric,
	}
	ret, _, callErr := procCreateIpForwardEntryNet.Call(uintptr(unsafe.Pointer(&row)))
	if ret != 0 {
		if callErr != nil && !errors.Is(callErr, syscall.Errno(0)) {
			return fmt.Errorf("CreateIpForwardEntry failed: %w", callErr)
		}
		return fmt.Errorf("CreateIpForwardEntry failed: code=%d", ret)
	}
	return nil
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
	rows, err := windowsListIPForwardRows()
	if err != nil {
		return err
	}
	var allErr error
	for _, row := range rows {
		if row.ForwardIfIndex != ifIndex || row.ForwardDest != dest || row.ForwardMask != mask {
			continue
		}
		if nextHop != nil && row.ForwardNextHop != *nextHop {
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
	return binary.BigEndian.Uint32(ip4), true
}

func uint32ToIPv4(value uint32) string {
	var b [4]byte
	binary.BigEndian.PutUint32(b[:], value)
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
