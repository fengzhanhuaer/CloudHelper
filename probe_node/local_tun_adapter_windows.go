//go:build windows

package main

import (
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"syscall"
	"unsafe"

	"golang.org/x/sys/windows"
)

const probeLocalWindowsErrorInsufficientBuffer = 122

type probeLocalWindowsNetAdapter struct {
	InterfaceIndex       int
	InterfaceLUID        uint64
	Name                 string
	InterfaceDescription string
	AdapterGUID          string
}

type probeLocalWintunVisibilityEvidence struct {
	NetAdapter             probeLocalWindowsNetAdapter
	NetAdapterMatched      bool
	PresentPnPMatched      bool
	PhantomPnPMatched      bool
	MatchedPnPStatus       string
	MatchedPnPProblem      string
	MatchedPnPInstanceID   string
	MatchedPnPFriendlyName string
}

func (e probeLocalWintunVisibilityEvidence) isJointlyVisible() bool {
	return e.NetAdapterMatched && e.PresentPnPMatched
}

func (e probeLocalWintunVisibilityEvidence) isPhantomOnly() bool {
	return e.PhantomPnPMatched && !e.PresentPnPMatched
}

func detectProbeLocalWintunAdapter() (bool, error) {
	evidence, err := inspectProbeLocalWintunVisibility()
	if err != nil {
		return false, err
	}
	return evidence.isJointlyVisible(), nil
}

func inspectProbeLocalWintunVisibility() (probeLocalWintunVisibilityEvidence, error) {
	evidence := probeLocalWintunVisibilityEvidence{}
	adapter, exists, adapterErr := findProbeLocalWintunAdapter()
	if exists {
		evidence.NetAdapterMatched = true
		evidence.NetAdapter = adapter
	}
	devices, pnpErr := listProbeLocalWindowsPnPDevices()
	for _, device := range devices {
		if !probeLocalWintunPnPDeviceMatches(device) {
			continue
		}
		if device.Present {
			evidence.PresentPnPMatched = true
			evidence.MatchedPnPStatus = strings.TrimSpace(device.Status)
			evidence.MatchedPnPProblem = strings.TrimSpace(device.Problem)
			evidence.MatchedPnPFriendlyName = strings.TrimSpace(device.FriendlyName)
			evidence.MatchedPnPInstanceID = strings.TrimSpace(device.InstanceID)
			break
		}
		if probeLocalPnPDeviceIsPhantom(device) {
			evidence.PhantomPnPMatched = true
			evidence.MatchedPnPStatus = strings.TrimSpace(device.Status)
			evidence.MatchedPnPProblem = strings.TrimSpace(device.Problem)
			evidence.MatchedPnPFriendlyName = strings.TrimSpace(device.FriendlyName)
			evidence.MatchedPnPInstanceID = strings.TrimSpace(device.InstanceID)
		}
	}
	if adapterErr != nil && pnpErr != nil {
		return evidence, errors.Join(adapterErr, pnpErr)
	}
	if adapterErr != nil {
		return evidence, adapterErr
	}
	if pnpErr != nil {
		return evidence, pnpErr
	}
	return evidence, nil
}

func findProbeLocalWintunAdapter() (probeLocalWindowsNetAdapter, bool, error) {
	adapters, err := listProbeLocalWindowsNetAdapters()
	if err != nil {
		return probeLocalWindowsNetAdapter{}, false, err
	}
	for _, adapter := range adapters {
		if probeLocalWintunAdapterMatchesWithGUID(adapter.Name, adapter.InterfaceDescription, adapter.AdapterGUID) {
			return adapter, true, nil
		}
	}
	return probeLocalWindowsNetAdapter{}, false, nil
}

func findProbeLocalWintunAdapterByLUID(luid uint64) (probeLocalWindowsNetAdapter, bool, error) {
	if luid == 0 {
		return probeLocalWindowsNetAdapter{}, false, nil
	}
	adapters, err := listProbeLocalWindowsNetAdapters()
	if err != nil {
		return probeLocalWindowsNetAdapter{}, false, err
	}
	for _, adapter := range adapters {
		if adapter.InterfaceLUID == luid {
			return adapter, true, nil
		}
	}
	return probeLocalWindowsNetAdapter{}, false, nil
}

func findProbeLocalWintunAdapterLUID() (uint64, bool, error) {
	adapter, exists, err := findProbeLocalWintunAdapter()
	if err != nil {
		return 0, false, err
	}
	if !exists {
		return 0, false, nil
	}
	if adapter.InterfaceLUID == 0 {
		return 0, false, nil
	}
	return adapter.InterfaceLUID, true, nil
}

func probeLocalWintunAdapterMatches(name, description string) bool {
	return probeLocalWintunAdapterMatchesWithGUID(name, description, "")
}

func probeLocalWintunAdapterMatchesWithGUID(name, description, adapterGUID string) bool {
	cleanName := strings.TrimSpace(name)
	cleanDesc := strings.TrimSpace(description)
	if strings.EqualFold(cleanName, probeLocalTUNAdapterName) || strings.HasPrefix(strings.ToLower(cleanName), strings.ToLower(probeLocalTUNAdapterName)+" ") {
		return true
	}
	if strings.EqualFold(cleanDesc, probeLocalTUNAdapterDescription) {
		return true
	}
	reqGUID := strings.ToLower(strings.Trim(strings.TrimSpace(probeLocalTUNAdapterRequestedGUID), "{}"))
	curGUID := strings.ToLower(strings.Trim(strings.TrimSpace(adapterGUID), "{}"))
	if reqGUID != "" && curGUID != "" && reqGUID == curGUID {
		return true
	}
	return false
}

func listProbeLocalWindowsNetAdapters() ([]probeLocalWindowsNetAdapter, error) {
	flags := uint32(windows.GAA_FLAG_INCLUDE_PREFIX)
	var size uint32 = 15 * 1024
	buf := make([]byte, size)
	for i := 0; i < 3; i++ {
		first := (*windows.IpAdapterAddresses)(unsafe.Pointer(&buf[0]))
		errCode := windows.GetAdaptersAddresses(windows.AF_UNSPEC, flags, 0, first, &size)
		if errCode == nil {
			return parseProbeLocalWindowsNetAdapters(first), nil
		}
		if errors.Is(errCode, syscall.Errno(probeLocalWindowsErrorInsufficientBuffer)) {
			buf = make([]byte, size)
			continue
		}
		return nil, errCode
	}
	return nil, errors.New("GetAdaptersAddresses failed after retries")
}

func parseProbeLocalWindowsNetAdapters(first *windows.IpAdapterAddresses) []probeLocalWindowsNetAdapter {
	items := make([]probeLocalWindowsNetAdapter, 0)
	for curr := first; curr != nil; curr = curr.Next {
		item := probeLocalWindowsNetAdapter{
			InterfaceIndex:       int(curr.IfIndex),
			InterfaceLUID:        curr.Luid,
			Name:                 strings.TrimSpace(windows.UTF16PtrToString(curr.FriendlyName)),
			InterfaceDescription: strings.TrimSpace(windows.UTF16PtrToString(curr.Description)),
		}
		adapterName := strings.TrimSpace(windows.BytePtrToString(curr.AdapterName))
		if adapterName != "" {
			item.AdapterGUID = "{" + strings.Trim(adapterName, "{}") + "}"
		}
		items = append(items, item)
	}
	return items
}

func createProbeLocalWintunAdapter(libraryPath, adapterName, tunnelType string) (uintptr, error) {
	return createProbeLocalWintunAdapterWithIdentity(libraryPath, adapterName, tunnelType, strings.TrimSpace(probeLocalTUNAdapterRequestedGUID), true)
}

func createProbeLocalWintunAdapterFresh(libraryPath, adapterName, tunnelType string) (uintptr, error) {
	guid, guidErr := windows.GenerateGUID()
	if guidErr != nil {
		return 0, fmt.Errorf("generate fresh adapter guid failed: %w", guidErr)
	}
	freshName := strings.TrimSpace(adapterName)
	if freshName == "" {
		freshName = probeLocalTUNAdapterName
	}
	freshName = fmt.Sprintf("%s %s", freshName, strings.ToUpper(strings.Trim(guid.String(), "{}"))[:8])
	return createProbeLocalWintunAdapterWithIdentity(libraryPath, freshName, tunnelType, guid.String(), false)
}

func createProbeLocalWintunAdapterWithIdentity(libraryPath, adapterName, tunnelType, requestedGUID string, allowOpen bool) (uintptr, error) {
	path := strings.TrimSpace(libraryPath)
	if path == "" {
		return 0, errors.New("empty wintun.dll path")
	}
	if _, err := os.Stat(path); err != nil {
		return 0, fmt.Errorf("wintun.dll is not ready at %s: %w", path, err)
	}
	if absPath, err := filepath.Abs(path); err == nil {
		path = absPath
	}

	name := strings.TrimSpace(adapterName)
	if name == "" {
		return 0, errors.New("empty tun adapter name")
	}
	kind := strings.TrimSpace(tunnelType)
	if kind == "" {
		kind = name
	}

	wintunDLL := syscall.NewLazyDLL(path)
	createProc := wintunDLL.NewProc("WintunCreateAdapter")
	openProc := wintunDLL.NewProc("WintunOpenAdapter")
	if err := wintunDLL.Load(); err != nil {
		return 0, fmt.Errorf("failed to load wintun.dll: %w", err)
	}

	namePtr, err := syscall.UTF16PtrFromString(name)
	if err != nil {
		return 0, fmt.Errorf("invalid tun adapter name: %w", err)
	}
	kindPtr, err := syscall.UTF16PtrFromString(kind)
	if err != nil {
		return 0, fmt.Errorf("invalid tun tunnel type: %w", err)
	}

	guidArg := uintptr(0)
	if guidText := strings.TrimSpace(requestedGUID); guidText != "" {
		reqGUID, parseErr := windows.GUIDFromString(guidText)
		if parseErr != nil {
			return 0, fmt.Errorf("invalid requested adapter guid: %w", parseErr)
		}
		guidArg = uintptr(unsafe.Pointer(&reqGUID))
	}

	handle, _, createErr := createProc.Call(
		uintptr(unsafe.Pointer(namePtr)),
		uintptr(unsafe.Pointer(kindPtr)),
		guidArg,
	)
	if handle != 0 {
		return handle, nil
	}

	if allowOpen {
		openHandle, _, openErr := openProc.Call(uintptr(unsafe.Pointer(namePtr)))
		if openHandle != 0 {
			return openHandle, nil
		}
		if openErr != nil && !errors.Is(openErr, syscall.Errno(0)) {
			if errors.Is(openErr, syscall.ERROR_ACCESS_DENIED) {
				return 0, errors.New("WintunOpenAdapter access denied, please run probe as administrator")
			}
			return 0, fmt.Errorf("WintunOpenAdapter failed: %s", formatProbeLocalWindowsCallErr(openErr))
		}
		if createErr == nil || errors.Is(createErr, syscall.Errno(0)) {
			return 0, fmt.Errorf("wintun adapter creation/open returned empty adapter handle (create=%s open=%s)", formatProbeLocalWindowsCallErr(createErr), formatProbeLocalWindowsCallErr(openErr))
		}
	}

	if createErr != nil && !errors.Is(createErr, syscall.Errno(0)) {
		if errors.Is(createErr, syscall.ERROR_ACCESS_DENIED) {
			return 0, errors.New("WintunCreateAdapter access denied, please run probe as administrator")
		}
		return 0, fmt.Errorf("WintunCreateAdapter failed: %s", formatProbeLocalWindowsCallErr(createErr))
	}
	if allowOpen {
		return 0, fmt.Errorf("wintun adapter creation/open returned empty adapter handle (create=%s open=errno=0)", formatProbeLocalWindowsCallErr(createErr))
	}
	return 0, fmt.Errorf("wintun adapter creation returned empty adapter handle (create=%s)", formatProbeLocalWindowsCallErr(createErr))
}

func closeProbeLocalWintunAdapter(libraryPath string, handle uintptr) error {
	if handle == 0 {
		return nil
	}
	path := strings.TrimSpace(libraryPath)
	if path == "" {
		return errors.New("empty wintun.dll path while closing adapter")
	}
	if absPath, err := filepath.Abs(path); err == nil {
		path = absPath
	}

	wintunDLL := syscall.NewLazyDLL(path)
	closeProc := wintunDLL.NewProc("WintunCloseAdapter")
	if err := wintunDLL.Load(); err != nil {
		return fmt.Errorf("failed to load wintun.dll for close: %w", err)
	}
	_, _, callErr := closeProc.Call(handle)
	if callErr != nil && !errors.Is(callErr, syscall.Errno(0)) {
		return fmt.Errorf("WintunCloseAdapter call failed: %w", callErr)
	}
	return nil
}

func getProbeLocalWintunAdapterLUIDFromHandle(libraryPath string, handle uintptr) (uint64, error) {
	if handle == 0 {
		return 0, errors.New("empty wintun adapter handle")
	}
	path := strings.TrimSpace(libraryPath)
	if path == "" {
		return 0, errors.New("empty wintun.dll path")
	}
	if absPath, err := filepath.Abs(path); err == nil {
		path = absPath
	}
	wintunDLL := syscall.NewLazyDLL(path)
	getLUIDProc := wintunDLL.NewProc("WintunGetAdapterLUID")
	if err := wintunDLL.Load(); err != nil {
		return 0, fmt.Errorf("failed to load wintun.dll for luid query: %w", err)
	}
	var luid uint64
	_, _, callErr := getLUIDProc.Call(
		handle,
		uintptr(unsafe.Pointer(&luid)),
	)
	if callErr != nil && !errors.Is(callErr, syscall.Errno(0)) {
		return 0, fmt.Errorf("WintunGetAdapterLUID call failed: %w", callErr)
	}
	if luid == 0 {
		return 0, errors.New("WintunGetAdapterLUID returned zero")
	}
	return luid, nil
}

type probeLocalWindowsPnPDevice struct {
	FriendlyName string `json:"friendly_name"`
	InstanceID   string `json:"instance_id"`
	Class        string `json:"class"`
	Status       string `json:"status"`
	Present      bool   `json:"present"`
	Problem      string `json:"problem"`
}

func listProbeLocalWindowsPnPDevices() ([]probeLocalWindowsPnPDevice, error) {
	script := "$ErrorActionPreference='Stop'; $items=Get-PnpDevice -PresentOnly:$false | ForEach-Object { [PSCustomObject]@{ friendly_name=[string]$_.FriendlyName; instance_id=[string]$_.InstanceId; class=[string]$_.Class; status=[string]$_.Status; present=([bool]$_.Present); problem=if ($null -eq $_.Problem) { '' } else { [string]$_.Problem } } }; $items | ConvertTo-Json -Compress"
	cmd := exec.Command("powershell", "-NoProfile", "-NonInteractive", "-Command", script)
	output, err := cmd.CombinedOutput()
	if err != nil {
		return nil, fmt.Errorf("query pnp devices failed: %w (%s)", err, strings.TrimSpace(string(output)))
	}
	text := strings.TrimSpace(string(output))
	if text == "" || strings.EqualFold(text, "null") {
		return nil, nil
	}
	var list []probeLocalWindowsPnPDevice
	if strings.HasPrefix(text, "[") {
		if unmarshalErr := json.Unmarshal([]byte(text), &list); unmarshalErr != nil {
			return nil, fmt.Errorf("decode pnp devices json array failed: %w", unmarshalErr)
		}
		return list, nil
	}
	var single probeLocalWindowsPnPDevice
	if unmarshalErr := json.Unmarshal([]byte(text), &single); unmarshalErr != nil {
		return nil, fmt.Errorf("decode pnp devices json object failed: %w", unmarshalErr)
	}
	return []probeLocalWindowsPnPDevice{single}, nil
}

func probeLocalWintunPnPDeviceMatches(device probeLocalWindowsPnPDevice) bool {
	friendly := strings.TrimSpace(device.FriendlyName)
	instanceID := strings.TrimSpace(device.InstanceID)
	className := strings.TrimSpace(device.Class)
	if probeLocalWintunAdapterMatchesWithGUID(friendly, friendly, "") {
		return true
	}
	lowerFriendly := strings.ToLower(friendly)
	lowerInstance := strings.ToLower(instanceID)
	if strings.Contains(lowerFriendly, "wintun") || strings.Contains(lowerFriendly, "wireguard") || strings.Contains(lowerFriendly, "cloudhelper") {
		return true
	}
	if strings.Contains(lowerInstance, "wintun") || strings.Contains(lowerInstance, "wireguard") || strings.Contains(lowerInstance, "cloudhelper") {
		return true
	}
	if strings.EqualFold(className, "Net") && (strings.Contains(lowerFriendly, "tunnel") || strings.Contains(lowerInstance, "wintun")) {
		return true
	}
	return false
}

func probeLocalPnPDeviceIsPhantom(device probeLocalWindowsPnPDevice) bool {
	if device.Present {
		return false
	}
	problemText := strings.TrimSpace(strings.ToUpper(device.Problem))
	if strings.Contains(problemText, "PHANTOM") || strings.EqualFold(problemText, "45") {
		return true
	}
	statusText := strings.TrimSpace(strings.ToUpper(device.Status))
	if statusText == "UNKNOWN" {
		return true
	}
	return false
}

func removeProbeLocalPhantomWintunDevices() (int, error) {
	devices, err := listProbeLocalWindowsPnPDevices()
	if err != nil {
		return 0, err
	}
	instanceIDs := make([]string, 0, len(devices))
	seen := make(map[string]struct{}, len(devices))
	for _, device := range devices {
		if !probeLocalWintunPnPDeviceMatches(device) || !probeLocalPnPDeviceIsPhantom(device) {
			continue
		}
		instanceID := strings.TrimSpace(device.InstanceID)
		if instanceID == "" {
			continue
		}
		key := strings.ToUpper(instanceID)
		if _, ok := seen[key]; ok {
			continue
		}
		seen[key] = struct{}{}
		instanceIDs = append(instanceIDs, instanceID)
	}
	removed := 0
	var removeErr error
	for _, instanceID := range instanceIDs {
		cmd := exec.Command("pnputil", "/remove-device", instanceID)
		output, err := cmd.CombinedOutput()
		if err != nil {
			removeErr = errors.Join(removeErr, fmt.Errorf("remove phantom pnp device failed: instance=%s err=%w output=%s", instanceID, err, strings.TrimSpace(string(output))))
			continue
		}
		removed++
	}
	if removeErr != nil {
		return removed, removeErr
	}
	return removed, nil
}

func formatProbeLocalWindowsCallErr(err error) string {
	if err == nil {
		return "nil"
	}
	if errors.Is(err, syscall.Errno(0)) {
		return "errno=0"
	}
	var errno syscall.Errno
	if errors.As(err, &errno) {
		if errno == 0 {
			return "errno=0"
		}
		message := strings.TrimSpace(errno.Error())
		if message == "" || strings.EqualFold(message, "unknown error") {
			return fmt.Sprintf("errno=%d", uint32(errno))
		}
		return fmt.Sprintf("errno=%d (%s)", uint32(errno), message)
	}
	message := strings.TrimSpace(err.Error())
	if message == "" {
		return fmt.Sprintf("%T", err)
	}
	return message
}
