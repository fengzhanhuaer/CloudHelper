//go:build windows

package main

import (
	"errors"
	"fmt"
	"os"
	"strconv"
	"strings"
	"time"
)

const (
	probeLocalTUNAdapterName          = "Maple"
	probeLocalTUNAdapterDescription   = "Maple Virtual Network Adapter"
	probeLocalTUNTunnelType           = "Maple"
	probeLocalTUNAdapterRequestedGUID = "{6BA2B7A3-1C2D-4E63-9E3C-6F7A8B9C0D21}"
	probeLocalTUNRouteGatewayIPv4     = "198.18.0.1"
	probeLocalTUNRouteIPv4PrefixLen   = 15
)

var (
	probeLocalEnsureWintunLibrary            = ensureProbeEmbeddedWintunLibrary
	probeLocalResolveWintunPath              = resolveProbeWintunPath
	probeLocalDetectWintunAdapter            = detectProbeLocalWintunAdapter
	probeLocalFindWintunAdapter              = findProbeLocalWintunAdapter
	probeLocalFindWintunAdapterByLUID        = findProbeLocalWintunAdapterByLUID
	probeLocalCreateWintunAdapter            = createProbeLocalWintunAdapter
	probeLocalCloseWintunAdapter             = closeProbeLocalWintunAdapter
	probeLocalGetWintunAdapterLUIDFromHandle = getProbeLocalWintunAdapterLUIDFromHandle
	probeLocalEnsureWindowsInterfaceIPv4     = ensureProbeLocalWindowsInterfaceIPv4Address
	probeLocalConvertInterfaceLUIDToIndex    = convertProbeLocalInterfaceLUIDToIndex
	probeLocalIsWindowsAdmin                 = isWindowsAdmin
	probeLocalRelaunchAsAdminInstall         = relaunchAsAdminForProbeLocalTUNInstall
	probeLocalTUNInstallSleep                = time.Sleep
)

func installProbeLocalTUNDriver() error {
	steps := make([]string, 0, 24)
	steps = append(steps, "start: install_probe_local_tun_driver")
	logInstallSuccess := func() {
		if len(steps) > 0 {
			logProbeInfof("probe local tun install diagnostic steps: %s", strings.Join(steps, " | "))
		}
	}
	if err := probeLocalEnsureWintunLibrary(); err != nil {
		steps = append(steps, "ensure_wintun_library: failed")
		return newProbeLocalTUNInstallError(
			probeLocalTUNInstallCodeWintunLibraryMissing,
			"ensure_wintun_library",
			"请确认 probe_node/lib/wintun/amd64/wintun.dll 存在且可读",
			fmt.Errorf("prepare wintun library: %w", err),
			steps,
		)
	}
	steps = append(steps, "ensure_wintun_library: ok")

	if !probeLocalIsWindowsAdmin() {
		steps = append(steps, "permission: non_admin")
		if elevatedErr := installProbeLocalTUNDriverViaElevation(); elevatedErr != nil {
			var installErr *probeLocalTUNInstallError
			if errors.As(elevatedErr, &installErr) && installErr != nil {
				installErr.Diagnostic.Steps = append(append([]string(nil), steps...), installErr.Diagnostic.Steps...)
				return installErr
			}
			return newProbeLocalTUNInstallError(
				probeLocalTUNInstallCodeElevationRequired,
				"request_elevation",
				"请以管理员权限运行，或确认 UAC 弹窗已允许",
				elevatedErr,
				steps,
			)
		}
		steps = append(steps, "request_elevation: ok")
		logInstallSuccess()
		return nil
	}
	steps = append(steps, "permission: admin")

	if exists, err := probeLocalDetectWintunAdapter(); err == nil && exists {
		steps = append(steps, "detect_adapter_precheck: found")
		logInstallSuccess()
		return nil
	}
	steps = append(steps, "detect_adapter_precheck: not_found")

	libraryPath, err := probeLocalResolveWintunPath()
	if err != nil {
		steps = append(steps, "resolve_wintun_path: failed")
		return newProbeLocalTUNInstallError(
			probeLocalTUNInstallCodeWintunLibraryMissing,
			"resolve_wintun_path",
			"请确认 Wintun 库路径可解析",
			fmt.Errorf("resolve wintun library path: %w", err),
			steps,
		)
	}
	steps = append(steps, "resolve_wintun_path: ok")

	handle, err := probeLocalCreateWintunAdapter(libraryPath, probeLocalTUNAdapterName, probeLocalTUNTunnelType)
	if err != nil {
		steps = append(steps, "create_or_open_adapter: failed")
		return newProbeLocalTUNInstallError(
			probeLocalTUNInstallCodeAdapterCreateFailed,
			"create_or_open_adapter",
			"Wintun 适配器创建失败，请检查管理员权限与驱动状态",
			fmt.Errorf("create/open wintun adapter: %w", err),
			steps,
		)
	}
	steps = append(steps, "create_or_open_adapter: ok")
	handleLUID := uint64(0)
	if handle != 0 {
		if luid, luidErr := probeLocalGetWintunAdapterLUIDFromHandle(libraryPath, handle); luidErr == nil && luid > 0 {
			handleLUID = luid
			steps = append(steps, "resolve_adapter_luid_from_handle: ok")
		} else {
			steps = append(steps, "resolve_adapter_luid_from_handle: failed")
		}
		steps = append(steps, "close_adapter_handle: deferred")
		defer func() {
			if closeErr := probeLocalCloseWintunAdapter(libraryPath, handle); closeErr != nil {
				logProbeWarnf("close wintun adapter handle failed: %v", closeErr)
			}
		}()
	}

	createdOrOpened := handle != 0
	var (
		detectErr error
		detected  bool
	)
	for _, delay := range []time.Duration{0, 200 * time.Millisecond, 450 * time.Millisecond, 800 * time.Millisecond, 1200 * time.Millisecond, 1800 * time.Millisecond, 2500 * time.Millisecond, 3500 * time.Millisecond, 5000 * time.Millisecond, 6500 * time.Millisecond} {
		if delay > 0 {
			probeLocalTUNInstallSleep(delay)
		}
		exists, err := probeLocalDetectWintunAdapter()
		if err != nil {
			detectErr = err
			steps = append(steps, "detect_adapter_retry: error")
			continue
		}
		if exists {
			detected = true
			steps = append(steps, "detect_adapter_retry: found")
			break
		}
		steps = append(steps, "detect_adapter_retry: not_found")
	}
	if detected {
		steps = append(steps, "verify_adapter: found")
		logInstallSuccess()
		return nil
	}
	if detectErr != nil {
		steps = append(steps, "detect_adapter_retry: failed")
		if createdOrOpened {
			return newProbeLocalTUNInstallError(
				probeLocalTUNInstallCodeAdapterNotDetected,
				"verify_adapter",
				"句柄创建成功但系统仍不可见网卡，建议查看驱动服务与网卡枚举",
				fmt.Errorf("verify wintun adapter after install failed (adapter handle was created/opened but adapter is still not detectable): %w", detectErr),
				steps,
			)
		}
		return newProbeLocalTUNInstallError(
			probeLocalTUNInstallCodeAdapterNotDetected,
			"verify_adapter",
			"安装后未检测到网卡，请检查驱动与设备管理器",
			fmt.Errorf("verify wintun adapter after install: %w", detectErr),
			steps,
		)
	}
	if createdOrOpened {
		if handleLUID != 0 {
			for _, delay := range []time.Duration{0, 300 * time.Millisecond, 700 * time.Millisecond, 1200 * time.Millisecond, 1800 * time.Millisecond} {
				if delay > 0 {
					probeLocalTUNInstallSleep(delay)
				}
				if adapterByLUID, ok, findErr := probeLocalFindWintunAdapterByLUID(handleLUID); findErr == nil && ok {
					if adapterByLUID.InterfaceIndex > 0 {
						setProbeLocalWindowsRouteTargetEnv(adapterByLUID.InterfaceIndex)
						steps = append(steps, "verify_adapter: fallback_luid_adapter_found")
						logInstallSuccess()
						return nil
					}
				}
			}
			ifIndex, convertErr := probeLocalConvertInterfaceLUIDToIndex(handleLUID)
			if convertErr == nil && ifIndex > 0 {
				setProbeLocalWindowsRouteTargetEnv(ifIndex)
				steps = append(steps, "verify_adapter: fallback_luid_ifindex_ok")
				logInstallSuccess()
				return nil
			}
			steps = append(steps, "verify_adapter: fallback_luid_ifindex_failed")
			return newProbeLocalTUNInstallError(
				probeLocalTUNInstallCodeAdapterNotDetected,
				"verify_adapter",
				"句柄创建成功但系统枚举不可见，且 LUID 无法转换接口索引，请检查网卡子系统状态",
				fmt.Errorf("wintun adapter is not detected after install and luid->ifindex failed: luid=%d err=%w", handleLUID, firstProbeLocalTUNErr(convertErr, errors.New("invalid interface index"))),
				steps,
			)
		}
		steps = append(steps, "verify_adapter: created_handle_but_not_detected")
		return newProbeLocalTUNInstallError(
			probeLocalTUNInstallCodeAdapterNotDetected,
			"verify_adapter",
			"句柄创建成功但系统仍不可见网卡，建议重启网卡子系统后重试",
			errors.New("wintun adapter is not detected after install (adapter handle was created/opened but adapter is still not detectable)"),
			steps,
		)
	}
	steps = append(steps, "verify_adapter: not_detected")
	return newProbeLocalTUNInstallError(
		probeLocalTUNInstallCodeAdapterNotDetected,
		"verify_adapter",
		"安装后未检测到网卡，请检查驱动与系统策略",
		errors.New("wintun adapter is not detected after install"),
		steps,
	)
}

func installProbeLocalTUNDriverViaElevation() error {
	steps := []string{"stage: request_elevation"}
	err := probeLocalRelaunchAsAdminInstall()
	if err != nil && !errors.Is(err, ErrProbeLocalRelaunchAsAdmin) {
		return newProbeLocalTUNInstallError(
			probeLocalTUNInstallCodeElevationRequired,
			"request_elevation",
			"请确认 UAC 提示已允许，或直接以管理员身份运行",
			fmt.Errorf("request administrator elevation failed: %w", err),
			steps,
		)
	}
	steps = append(steps, "request_elevation: accepted")
	return nil
}

func setProbeLocalWindowsRouteTargetEnv(interfaceIndex int) {
	_ = os.Setenv("PROBE_LOCAL_TUN_GATEWAY", probeLocalTUNRouteGatewayIPv4)
	if interfaceIndex > 0 {
		_ = os.Setenv("PROBE_LOCAL_TUN_IF_INDEX", strconv.Itoa(interfaceIndex))
	}
}

func ensureProbeLocalWindowsRouteTargetConfigured() error {
	adapter, exists, err := probeLocalFindWintunAdapter()
	if err != nil {
		return err
	}
	if exists {
		if adapter.InterfaceIndex <= 0 {
			if adapterLUID, luidExists, luidErr := findProbeLocalWintunAdapterLUID(); luidErr == nil && luidExists {
				ifIndex, convertErr := probeLocalConvertInterfaceLUIDToIndex(adapterLUID)
				if convertErr == nil && ifIndex > 0 {
					adapter.InterfaceIndex = ifIndex
				}
			}
		}
		if adapter.InterfaceIndex > 0 {
			return ensureProbeLocalWindowsRouteTargetByInterfaceIndex(adapter.InterfaceIndex)
		}
	}

	ifIndex, fallbackErr := resolveProbeLocalWintunInterfaceIndexFallback()
	if fallbackErr != nil || ifIndex <= 0 {
		return fmt.Errorf("wintun adapter is not detected after install: %w", firstProbeLocalTUNErr(fallbackErr, errors.New("invalid wintun adapter interface index")))
	}
	return ensureProbeLocalWindowsRouteTargetByInterfaceIndex(ifIndex)
}

func resolveProbeLocalWintunInterfaceIndexFallback() (int, error) {
	if rawIfIndex := strings.TrimSpace(os.Getenv("PROBE_LOCAL_TUN_IF_INDEX")); rawIfIndex != "" {
		if ifIndex, parseErr := strconv.Atoi(rawIfIndex); parseErr == nil && ifIndex > 0 {
			return ifIndex, nil
		}
	}

	libraryPath, err := probeLocalResolveWintunPath()
	if err != nil {
		return 0, fmt.Errorf("resolve wintun library path failed: %w", err)
	}
	handle, err := probeLocalCreateWintunAdapter(libraryPath, probeLocalTUNAdapterName, probeLocalTUNTunnelType)
	if err != nil {
		return 0, fmt.Errorf("create/open wintun adapter for route target failed: %w", err)
	}
	if handle == 0 {
		return 0, errors.New("create/open wintun adapter for route target returned empty handle")
	}
	defer func() {
		if closeErr := probeLocalCloseWintunAdapter(libraryPath, handle); closeErr != nil {
			logProbeWarnf("close wintun adapter handle failed while resolving route target: %v", closeErr)
		}
	}()

	adapterLUID, luidErr := probeLocalGetWintunAdapterLUIDFromHandle(libraryPath, handle)
	if luidErr != nil || adapterLUID == 0 {
		return 0, fmt.Errorf("resolve adapter luid from handle failed: %w", firstProbeLocalTUNErr(luidErr, errors.New("invalid adapter luid")))
	}
	ifIndex, convertErr := probeLocalConvertInterfaceLUIDToIndex(adapterLUID)
	if convertErr != nil || ifIndex <= 0 {
		return 0, fmt.Errorf("convert adapter luid to interface index failed: luid=%d err=%w", adapterLUID, firstProbeLocalTUNErr(convertErr, errors.New("invalid interface index")))
	}
	return ifIndex, nil
}

func ensureProbeLocalWindowsRouteTargetByInterfaceIndex(interfaceIndex int) error {
	if interfaceIndex <= 0 {
		return errors.New("invalid wintun adapter interface index")
	}
	if err := probeLocalEnsureWindowsInterfaceIPv4(interfaceIndex, probeLocalTUNRouteGatewayIPv4, probeLocalTUNRouteIPv4PrefixLen); err != nil {
		return err
	}
	setProbeLocalWindowsRouteTargetEnv(interfaceIndex)
	return nil
}

func firstProbeLocalTUNErr(primary error, fallback error) error {
	if primary != nil {
		return primary
	}
	return fallback
}

func resetProbeLocalTUNInstallWindowsHooksForTest() {
	probeLocalEnsureWintunLibrary = ensureProbeEmbeddedWintunLibrary
	probeLocalResolveWintunPath = resolveProbeWintunPath
	probeLocalDetectWintunAdapter = detectProbeLocalWintunAdapter
	probeLocalFindWintunAdapter = findProbeLocalWintunAdapter
	probeLocalFindWintunAdapterByLUID = findProbeLocalWintunAdapterByLUID
	probeLocalCreateWintunAdapter = createProbeLocalWintunAdapter
	probeLocalCloseWintunAdapter = closeProbeLocalWintunAdapter
	probeLocalGetWintunAdapterLUIDFromHandle = getProbeLocalWintunAdapterLUIDFromHandle
	probeLocalEnsureWindowsInterfaceIPv4 = ensureProbeLocalWindowsInterfaceIPv4Address
	probeLocalConvertInterfaceLUIDToIndex = convertProbeLocalInterfaceLUIDToIndex
	probeLocalIsWindowsAdmin = isWindowsAdmin
	probeLocalRelaunchAsAdminInstall = relaunchAsAdminForProbeLocalTUNInstall
	probeLocalTUNInstallSleep = time.Sleep
}
