//go:build windows

package main

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"syscall"
	"time"
	"unsafe"

	"golang.org/x/sys/windows"
)

const probeLocalWindowsErrorInsufficientBuffer = 122

const (
	probeLocalSetupDIGCFPresent      = 0x00000002
	probeLocalSetupDIGCFAllClasses   = 0x00000004
	probeLocalSPDRPDevDesc           = 0x00000000
	probeLocalSPDRPClass             = 0x00000007
	probeLocalSPDRPFriendlyName      = 0x0000000C
	probeLocalDIFPropertyChange      = 0x00000012
	probeLocalDICSEnable             = 0x00000001
	probeLocalDICSDisable            = 0x00000002
	probeLocalDICSFlagGlobal         = 0x00000001
	probeLocalCMLocateDevNodePhantom = 0x00000001
	probeLocalCMProblemPhantom       = 45
	probeLocalCfgmgrCRSuccess        = 0x00000000
	probeLocalWindowsInvalidHandle   = ^uintptr(0)
)

var (
	probeLocalModSetupapi                           = windows.NewLazySystemDLL("setupapi.dll")
	probeLocalProcSetupDiGetClassDevsW              = probeLocalModSetupapi.NewProc("SetupDiGetClassDevsW")
	probeLocalProcSetupDiEnumDeviceInfo             = probeLocalModSetupapi.NewProc("SetupDiEnumDeviceInfo")
	probeLocalProcSetupDiGetDeviceInstanceIDW       = probeLocalModSetupapi.NewProc("SetupDiGetDeviceInstanceIdW")
	probeLocalProcSetupDiGetDeviceRegistryPropertyW = probeLocalModSetupapi.NewProc("SetupDiGetDeviceRegistryPropertyW")
	probeLocalProcSetupDiDestroyDeviceInfoList      = probeLocalModSetupapi.NewProc("SetupDiDestroyDeviceInfoList")
	probeLocalProcSetupDiSetClassInstallParamsW     = probeLocalModSetupapi.NewProc("SetupDiSetClassInstallParamsW")
	probeLocalProcSetupDiCallClassInstaller         = probeLocalModSetupapi.NewProc("SetupDiCallClassInstaller")

	probeLocalModCfgmgr32                 = windows.NewLazySystemDLL("cfgmgr32.dll")
	probeLocalProcCMGetDevNodeStatus      = probeLocalModCfgmgr32.NewProc("CM_Get_DevNode_Status")
	probeLocalProcCMLocateDevNodeW        = probeLocalModCfgmgr32.NewProc("CM_Locate_DevNodeW")
	probeLocalProcCMUninstallDevNode      = probeLocalModCfgmgr32.NewProc("CM_Uninstall_DevNode")
	probeLocalProcCMQueryAndRemoveSubTree = probeLocalModCfgmgr32.NewProc("CM_Query_And_Remove_SubTreeW")
)

type probeLocalSPDevinfoData struct {
	Size      uint32
	ClassGUID windows.GUID
	DevInst   uint32
	Reserved  uintptr
}

type probeLocalSPClassInstallHeader struct {
	Size            uint32
	InstallFunction uint32
}

type probeLocalSPPropchangeParams struct {
	ClassInstallHeader probeLocalSPClassInstallHeader
	StateChange        uint32
	Scope              uint32
	HwProfile          uint32
}

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
	return e.NetAdapterMatched
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
	allDevices, err := probeLocalSnapshotWindowsPnPDevices(probeLocalSetupDIGCFAllClasses)
	if err != nil {
		return nil, err
	}
	presentDevices, err := probeLocalSnapshotWindowsPnPDevices(probeLocalSetupDIGCFAllClasses | probeLocalSetupDIGCFPresent)
	if err != nil {
		return nil, err
	}

	presentMap := make(map[string]probeLocalWindowsPnPDevice, len(presentDevices))
	for _, device := range presentDevices {
		key := strings.ToUpper(strings.TrimSpace(device.InstanceID))
		if key == "" {
			continue
		}
		device.Present = true
		if strings.TrimSpace(device.Status) == "" {
			device.Status = "OK"
		}
		presentMap[key] = device
	}

	items := make([]probeLocalWindowsPnPDevice, 0, len(allDevices)+len(presentDevices))
	seen := make(map[string]struct{}, len(allDevices)+len(presentDevices))
	for _, device := range allDevices {
		key := strings.ToUpper(strings.TrimSpace(device.InstanceID))
		if key == "" {
			continue
		}
		if presentDevice, ok := presentMap[key]; ok {
			device.Present = true
			if strings.TrimSpace(device.FriendlyName) == "" {
				device.FriendlyName = presentDevice.FriendlyName
			}
			if strings.TrimSpace(device.Class) == "" {
				device.Class = presentDevice.Class
			}
			if strings.TrimSpace(device.Status) == "" {
				device.Status = presentDevice.Status
			}
			if strings.TrimSpace(device.Problem) == "" {
				device.Problem = presentDevice.Problem
			}
		} else {
			device.Present = false
			if strings.TrimSpace(device.Status) == "" {
				device.Status = "Unknown"
			}
			if strings.TrimSpace(device.Problem) == "" {
				device.Problem = "CM_PROB_PHANTOM"
			}
		}
		seen[key] = struct{}{}
		items = append(items, device)
	}
	for key, device := range presentMap {
		if _, ok := seen[key]; ok {
			continue
		}
		items = append(items, device)
	}
	return items, nil
}

func probeLocalSnapshotWindowsPnPDevices(flags uint32) ([]probeLocalWindowsPnPDevice, error) {
	handle, err := probeLocalOpenWindowsDeviceInfoSet(flags)
	if err != nil {
		return nil, err
	}
	defer probeLocalProcSetupDiDestroyDeviceInfoList.Call(handle)

	items := make([]probeLocalWindowsPnPDevice, 0, 16)
	for index := 0; ; index++ {
		var devInfo probeLocalSPDevinfoData
		devInfo.Size = uint32(unsafe.Sizeof(devInfo))
		ret, _, callErr := probeLocalProcSetupDiEnumDeviceInfo.Call(
			handle,
			uintptr(index),
			uintptr(unsafe.Pointer(&devInfo)),
		)
		if ret == 0 {
			if errors.Is(callErr, windows.ERROR_NO_MORE_ITEMS) {
				break
			}
			return nil, probeLocalWindowsSetupAPICallErr("SetupDiEnumDeviceInfo", callErr)
		}

		instanceID, err := probeLocalGetWindowsDeviceInstanceID(handle, &devInfo)
		if err != nil {
			return nil, err
		}
		friendlyName, err := probeLocalGetWindowsDeviceRegistryPropertyString(handle, &devInfo, probeLocalSPDRPFriendlyName)
		if err != nil {
			return nil, err
		}
		deviceDesc, err := probeLocalGetWindowsDeviceRegistryPropertyString(handle, &devInfo, probeLocalSPDRPDevDesc)
		if err != nil {
			return nil, err
		}
		className, err := probeLocalGetWindowsDeviceRegistryPropertyString(handle, &devInfo, probeLocalSPDRPClass)
		if err != nil {
			return nil, err
		}
		statusText, problemText := probeLocalGetWindowsDevNodeState(devInfo.DevInst)
		if strings.TrimSpace(friendlyName) == "" {
			friendlyName = deviceDesc
		}
		items = append(items, probeLocalWindowsPnPDevice{
			FriendlyName: strings.TrimSpace(friendlyName),
			InstanceID:   strings.TrimSpace(instanceID),
			Class:        strings.TrimSpace(className),
			Status:       strings.TrimSpace(statusText),
			Problem:      strings.TrimSpace(problemText),
		})
	}
	return items, nil
}

func probeLocalOpenWindowsDeviceInfoSet(flags uint32) (uintptr, error) {
	handle, _, callErr := probeLocalProcSetupDiGetClassDevsW.Call(0, 0, 0, uintptr(flags))
	if handle == probeLocalWindowsInvalidHandle {
		return 0, probeLocalWindowsSetupAPICallErr("SetupDiGetClassDevsW", callErr)
	}
	return handle, nil
}

func probeLocalGetWindowsDeviceInstanceID(handle uintptr, devInfo *probeLocalSPDevinfoData) (string, error) {
	bufLen := uint32(256)
	for attempt := 0; attempt < 3; attempt++ {
		buf := make([]uint16, bufLen)
		var required uint32
		ret, _, callErr := probeLocalProcSetupDiGetDeviceInstanceIDW.Call(
			handle,
			uintptr(unsafe.Pointer(devInfo)),
			uintptr(unsafe.Pointer(&buf[0])),
			uintptr(bufLen),
			uintptr(unsafe.Pointer(&required)),
		)
		if ret != 0 {
			return strings.TrimSpace(windows.UTF16ToString(buf)), nil
		}
		if errors.Is(callErr, syscall.ERROR_INSUFFICIENT_BUFFER) && required > bufLen {
			bufLen = required + 1
			continue
		}
		return "", probeLocalWindowsSetupAPICallErr("SetupDiGetDeviceInstanceIdW", callErr)
	}
	return "", errors.New("SetupDiGetDeviceInstanceIdW failed after retries")
}

func probeLocalGetWindowsDeviceRegistryPropertyString(handle uintptr, devInfo *probeLocalSPDevinfoData, property uint32) (string, error) {
	bufLen := uint32(256)
	for attempt := 0; attempt < 3; attempt++ {
		buf := make([]uint16, bufLen)
		var regDataType uint32
		var required uint32
		ret, _, callErr := probeLocalProcSetupDiGetDeviceRegistryPropertyW.Call(
			handle,
			uintptr(unsafe.Pointer(devInfo)),
			uintptr(property),
			uintptr(unsafe.Pointer(&regDataType)),
			uintptr(unsafe.Pointer(&buf[0])),
			uintptr(len(buf)*2),
			uintptr(unsafe.Pointer(&required)),
		)
		if ret != 0 {
			return strings.TrimSpace(windows.UTF16ToString(buf)), nil
		}
		if errors.Is(callErr, windows.ERROR_INVALID_DATA) || errors.Is(callErr, syscall.ERROR_NOT_FOUND) {
			return "", nil
		}
		if errors.Is(callErr, syscall.ERROR_INSUFFICIENT_BUFFER) && required > uint32(len(buf)*2) {
			bufLen = required/2 + 2
			continue
		}
		return "", probeLocalWindowsSetupAPICallErr("SetupDiGetDeviceRegistryPropertyW", callErr)
	}
	return "", errors.New("SetupDiGetDeviceRegistryPropertyW failed after retries")
}

func probeLocalGetWindowsDevNodeState(devInst uint32) (string, string) {
	var status uint32
	var problem uint32
	ret, _, _ := probeLocalProcCMGetDevNodeStatus.Call(
		uintptr(unsafe.Pointer(&status)),
		uintptr(unsafe.Pointer(&problem)),
		uintptr(devInst),
		0,
	)
	if ret != probeLocalCfgmgrCRSuccess {
		return "", ""
	}
	statusText := "OK"
	problemText := ""
	if problem != 0 {
		statusText = "Unknown"
		if problem == probeLocalCMProblemPhantom {
			problemText = "CM_PROB_PHANTOM"
		} else {
			problemText = fmt.Sprintf("%d", problem)
		}
	}
	return statusText, problemText
}

func probeLocalWindowsSetupAPICallErr(op string, callErr error) error {
	if callErr != nil && !errors.Is(callErr, syscall.Errno(0)) {
		return fmt.Errorf("%s failed: %s", op, formatProbeLocalWindowsCallErr(callErr))
	}
	return fmt.Errorf("%s failed", op)
}

func probeLocalWindowsCfgMgrCallErr(op string, ret uintptr) error {
	if ret == probeLocalCfgmgrCRSuccess {
		return nil
	}
	return fmt.Errorf("%s failed: code=%d", op, ret)
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

func recycleProbeLocalWindowsNetAdapter(interfaceIndex int) error {
	if interfaceIndex <= 0 {
		return errors.New("invalid wintun adapter interface index")
	}
	device, err := probeLocalResolveWindowsPnPDeviceForAdapter(interfaceIndex)
	if err != nil {
		return err
	}
	if strings.TrimSpace(device.InstanceID) == "" {
		return fmt.Errorf("wintun pnp device instance id is empty for interface index: %d", interfaceIndex)
	}
	if err := probeLocalChangeWindowsDeviceState(device.InstanceID, probeLocalDICSDisable); err != nil {
		return err
	}
	time.Sleep(320 * time.Millisecond)
	return probeLocalChangeWindowsDeviceState(device.InstanceID, probeLocalDICSEnable)
}

func probeLocalResolveWindowsPnPDeviceForAdapter(interfaceIndex int) (probeLocalWindowsPnPDevice, error) {
	adapters, err := listProbeLocalWindowsNetAdapters()
	if err != nil {
		return probeLocalWindowsPnPDevice{}, err
	}
	adapterName := ""
	for _, adapter := range adapters {
		if adapter.InterfaceIndex == interfaceIndex {
			adapterName = strings.TrimSpace(adapter.Name)
			break
		}
	}
	devices, err := listProbeLocalWindowsPnPDevices()
	if err != nil {
		return probeLocalWindowsPnPDevice{}, err
	}
	var fallback probeLocalWindowsPnPDevice
	for _, device := range devices {
		if !device.Present || !probeLocalWintunPnPDeviceMatches(device) {
			continue
		}
		if adapterName != "" && strings.EqualFold(strings.TrimSpace(device.FriendlyName), adapterName) {
			return device, nil
		}
		if strings.TrimSpace(fallback.InstanceID) == "" {
			fallback = device
		}
	}
	if strings.TrimSpace(fallback.InstanceID) != "" {
		return fallback, nil
	}
	return probeLocalWindowsPnPDevice{}, fmt.Errorf("present wintun pnp device not found for interface index: %d", interfaceIndex)
}

func probeLocalChangeWindowsDeviceState(instanceID string, stateChange uint32) error {
	handle, err := probeLocalOpenWindowsDeviceInfoSet(probeLocalSetupDIGCFAllClasses)
	if err != nil {
		return err
	}
	defer probeLocalProcSetupDiDestroyDeviceInfoList.Call(handle)

	devInfo, found, err := probeLocalFindWindowsDeviceInfoByInstanceID(handle, instanceID)
	if err != nil {
		return err
	}
	if !found {
		return fmt.Errorf("device instance not found: %s", strings.TrimSpace(instanceID))
	}
	params := probeLocalSPPropchangeParams{
		ClassInstallHeader: probeLocalSPClassInstallHeader{
			Size:            uint32(unsafe.Sizeof(probeLocalSPClassInstallHeader{})),
			InstallFunction: probeLocalDIFPropertyChange,
		},
		StateChange: stateChange,
		Scope:       probeLocalDICSFlagGlobal,
		HwProfile:   0,
	}
	ret, _, callErr := probeLocalProcSetupDiSetClassInstallParamsW.Call(
		handle,
		uintptr(unsafe.Pointer(&devInfo)),
		uintptr(unsafe.Pointer(&params)),
		unsafe.Sizeof(params),
	)
	if ret == 0 {
		return probeLocalWindowsSetupAPICallErr("SetupDiSetClassInstallParamsW", callErr)
	}
	ret, _, callErr = probeLocalProcSetupDiCallClassInstaller.Call(
		uintptr(probeLocalDIFPropertyChange),
		handle,
		uintptr(unsafe.Pointer(&devInfo)),
	)
	if ret == 0 {
		return probeLocalWindowsSetupAPICallErr("SetupDiCallClassInstaller", callErr)
	}
	return nil
}

func probeLocalFindWindowsDeviceInfoByInstanceID(handle uintptr, instanceID string) (probeLocalSPDevinfoData, bool, error) {
	cleanID := strings.TrimSpace(instanceID)
	for index := 0; ; index++ {
		var devInfo probeLocalSPDevinfoData
		devInfo.Size = uint32(unsafe.Sizeof(devInfo))
		ret, _, callErr := probeLocalProcSetupDiEnumDeviceInfo.Call(
			handle,
			uintptr(index),
			uintptr(unsafe.Pointer(&devInfo)),
		)
		if ret == 0 {
			if errors.Is(callErr, windows.ERROR_NO_MORE_ITEMS) {
				return probeLocalSPDevinfoData{}, false, nil
			}
			return probeLocalSPDevinfoData{}, false, probeLocalWindowsSetupAPICallErr("SetupDiEnumDeviceInfo", callErr)
		}
		currentInstanceID, err := probeLocalGetWindowsDeviceInstanceID(handle, &devInfo)
		if err != nil {
			return probeLocalSPDevinfoData{}, false, err
		}
		if strings.EqualFold(strings.TrimSpace(currentInstanceID), cleanID) {
			return devInfo, true, nil
		}
	}
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
		if err := probeLocalUninstallWindowsPhantomDevice(instanceID); err != nil {
			removeErr = errors.Join(removeErr, fmt.Errorf("remove phantom pnp device failed: instance=%s err=%w", instanceID, err))
			continue
		}
		removed++
	}
	if removeErr != nil {
		return removed, removeErr
	}
	return removed, nil
}

func probeLocalUninstallWindowsPhantomDevice(instanceID string) error {
	cleanID := strings.TrimSpace(instanceID)
	if cleanID == "" {
		return errors.New("empty device instance id")
	}
	instanceIDPtr, err := syscall.UTF16PtrFromString(cleanID)
	if err != nil {
		return fmt.Errorf("encode device instance id failed: %w", err)
	}
	var devInst uint32
	ret, _, _ := probeLocalProcCMLocateDevNodeW.Call(
		uintptr(unsafe.Pointer(&devInst)),
		uintptr(unsafe.Pointer(instanceIDPtr)),
		uintptr(probeLocalCMLocateDevNodePhantom),
	)
	if ret != probeLocalCfgmgrCRSuccess {
		return probeLocalWindowsCfgMgrCallErr("CM_Locate_DevNodeW", ret)
	}
	ret, _, _ = probeLocalProcCMUninstallDevNode.Call(uintptr(devInst), 0)
	if ret == probeLocalCfgmgrCRSuccess {
		return nil
	}
	ret, _, _ = probeLocalProcCMQueryAndRemoveSubTree.Call(uintptr(devInst), 0, 0, 0, 0)
	if ret != probeLocalCfgmgrCRSuccess {
		return probeLocalWindowsCfgMgrCallErr("CM_Query_And_Remove_SubTreeW", ret)
	}
	return nil
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
