//go:build windows

package main

import (
	"errors"
	"fmt"
	"os"
	"strconv"
	"strings"
	"sync"
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
	probeLocalInspectWintunVisibility        = inspectProbeLocalWintunVisibility
	probeLocalRemovePhantomWintunDevices     = removeProbeLocalPhantomWintunDevices
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
	probeLocalRetainWintunAdapterHandle      = retainProbeLocalWintunAdapterHandle
)

var probeLocalRetainedWintunAdapterState = struct {
	mu          sync.Mutex
	libraryPath string
	handle      uintptr
}{}

func installProbeLocalTUNDriver() error {
	steps := make([]string, 0, 24)
	steps = append(steps, "start: install_probe_local_tun_driver")
	observation := newProbeLocalTUNInstallObservation()
	const (
		reasonCodeSuccess         = "TUN_INSTALL_SUCCEEDED"
		reasonCodeSuccessNotReady = "TUN_INSTALL_SUCCEEDED_NOT_READY"
	)
	logInstallSuccess := func() {
		if len(steps) > 0 {
			logProbeInfof("probe local tun install diagnostic steps: %s", strings.Join(steps, " | "))
		}
	}
	captureDriverPackageEvidence := func(path string) {
		cleanPath := strings.TrimSpace(path)
		if cleanPath == "" {
			return
		}
		observation.Driver.PackagePath = cleanPath
		if info, statErr := os.Stat(cleanPath); statErr == nil && info != nil && !info.IsDir() {
			observation.Driver.PackageExists = true
		} else {
			observation.Driver.PackageExists = false
		}
	}
	setSuccessObservation := func(reason string) {
		observation.Final.Success = true
		observation.Final.ReasonCode = reasonCodeSuccess
		observation.Final.Reason = strings.TrimSpace(reason)
		observation.Diagnostic = probeLocalTUNInstallObservationDiagnostic{}
		setProbeLocalTUNInstallObservation(observation)
	}
	setSuccessNotReadyObservation := func(reasonCode string, reason string, rawErr error) {
		rawText := ""
		if rawErr != nil {
			rawText = strings.TrimSpace(rawErr.Error())
		}
		cleanCode := strings.TrimSpace(reasonCode)
		if cleanCode == "" {
			cleanCode = reasonCodeSuccessNotReady
		}
		observation.Final.Success = true
		observation.Final.ReasonCode = cleanCode
		observation.Final.Reason = strings.TrimSpace(reason)
		observation.Diagnostic.Code = cleanCode
		observation.Diagnostic.RawError = rawText
		setProbeLocalTUNInstallObservation(observation)
	}
	setFailureObservation := func(code string, reason string, rawErr error) {
		rawText := ""
		if rawErr != nil {
			rawText = strings.TrimSpace(rawErr.Error())
		}
		observation.Final.Success = false
		observation.Final.ReasonCode = strings.TrimSpace(code)
		observation.Final.Reason = strings.TrimSpace(reason)
		observation.Diagnostic.Code = strings.TrimSpace(code)
		observation.Diagnostic.RawError = rawText
		if observation.Create.Called && observation.Create.RawError == "" {
			observation.Create.RawError = rawText
		}
		setProbeLocalTUNInstallObservation(observation)
	}
	failInstall := func(code string, stage string, hint string, cause error) error {
		setFailureObservation(code, hint, cause)
		return newProbeLocalTUNInstallError(
			code,
			stage,
			hint,
			cause,
			steps,
			observation,
		)
	}

	if err := probeLocalEnsureWintunLibrary(); err != nil {
		steps = append(steps, "ensure_wintun_library: failed")
		return failInstall(
			probeLocalTUNInstallCodeWintunLibraryMissing,
			"ensure_wintun_library",
			"请确认 probe_node/lib/wintun/amd64/wintun.dll 存在且可读",
			fmt.Errorf("prepare wintun library: %w", err),
		)
	}
	steps = append(steps, "ensure_wintun_library: ok")
	if packagePath, pathErr := probeLocalResolveWintunPath(); pathErr == nil {
		captureDriverPackageEvidence(packagePath)
	}

	if !probeLocalIsWindowsAdmin() {
		steps = append(steps, "permission: non_admin")
		libraryPathForElevationWait := ""
		if path, resolveErr := probeLocalResolveWintunPath(); resolveErr == nil {
			libraryPathForElevationWait = strings.TrimSpace(path)
			captureDriverPackageEvidence(libraryPathForElevationWait)
			steps = append(steps, "resolve_wintun_path_for_elevation_wait: ok")
		} else {
			steps = append(steps, "resolve_wintun_path_for_elevation_wait: failed")
		}
		if elevatedErr := installProbeLocalTUNDriverViaElevation(); elevatedErr != nil {
			var installErr *probeLocalTUNInstallError
			if errors.As(elevatedErr, &installErr) && installErr != nil {
				installErr.Diagnostic.Steps = append(append([]string(nil), steps...), installErr.Diagnostic.Steps...)
				if installObs, ok := installErr.InstallObservation(); ok {
					observation = installObs
				}
				if observation.Final.ReasonCode == "" {
					observation.Final.ReasonCode = strings.TrimSpace(installErr.Diagnostic.Code)
				}
				observation.Final.Success = false
				if observation.Final.Reason == "" {
					observation.Final.Reason = strings.TrimSpace(installErr.Diagnostic.Hint)
				}
				if observation.Diagnostic.Code == "" {
					observation.Diagnostic.Code = strings.TrimSpace(installErr.Diagnostic.Code)
				}
				if observation.Diagnostic.RawError == "" {
					observation.Diagnostic.RawError = strings.TrimSpace(firstProbeLocalTUNErr(errors.New(strings.TrimSpace(installErr.Diagnostic.RawError)), errors.New(strings.TrimSpace(installErr.Diagnostic.Details))).Error())
				}
				setProbeLocalTUNInstallObservation(observation)
				installErr.Observation = cloneProbeLocalTUNInstallObservationPointer(&observation)
				return installErr
			}
			return failInstall(
				probeLocalTUNInstallCodeElevationRequired,
				"request_elevation",
				"请以管理员权限运行，或确认 UAC 弹窗已允许",
				elevatedErr,
			)
		}
		steps = append(steps, "request_elevation: accepted")
		steps = append(steps, "await_adapter_visibility_after_elevation: begin")
		var elevationDetectErr error
		elevationPhantomOnly := false
		lastElevationEvidence := probeLocalWintunVisibilityEvidence{}
		for _, delay := range []time.Duration{0, 200 * time.Millisecond, 450 * time.Millisecond, 800 * time.Millisecond, 1200 * time.Millisecond, 1800 * time.Millisecond, 2500 * time.Millisecond, 3500 * time.Millisecond, 5000 * time.Millisecond, 6500 * time.Millisecond} {
			if delay > 0 {
				probeLocalTUNInstallSleep(delay)
			}
			evidence, detectErr := probeLocalInspectWintunVisibility()
			if detectErr != nil {
				elevationDetectErr = detectErr
				steps = append(steps, "await_adapter_visibility_after_elevation: error")
				continue
			}
			if evidence.NetAdapterMatched && evidence.NetAdapter.InterfaceIndex > 0 {
				observation.Visibility.IfIndexResolved = true
				observation.Visibility.IfIndexValue = evidence.NetAdapter.InterfaceIndex
				setProbeLocalWindowsRouteTargetEnv(evidence.NetAdapter.InterfaceIndex)
			}
			if evidence.isPhantomOnly() {
				elevationPhantomOnly = true
				lastElevationEvidence = evidence
				steps = append(steps, "await_adapter_visibility_after_elevation: phantom_only")
			}
			if evidence.isJointlyVisible() {
				observation.Visibility.DetectVisible = true
				steps = append(steps, "await_adapter_visibility_after_elevation: found")
				if libraryPathForElevationWait != "" {
					observation.Create.Called = true
					handle, openErr := probeLocalCreateWintunAdapter(libraryPathForElevationWait, probeLocalTUNAdapterName, probeLocalTUNTunnelType)
					if openErr == nil && handle != 0 {
						observation.Create.HandleNonZero = true
						probeLocalRetainWintunAdapterHandle(libraryPathForElevationWait, handle)
						steps = append(steps, "await_adapter_visibility_after_elevation: retain_handle_opened")
						setSuccessObservation("提权后检测到 TUN 适配器可见并已保持句柄")
						logInstallSuccess()
						return nil
					}
					if openErr != nil {
						observation.Create.RawError = strings.TrimSpace(openErr.Error())
						steps = append(steps, "await_adapter_visibility_after_elevation: retain_handle_open_failed")
					}
				}
				setSuccessObservation("提权后检测到 TUN 适配器可见")
				logInstallSuccess()
				return nil
			}
			steps = append(steps, "await_adapter_visibility_after_elevation: not_found")
		}
		steps = append(steps, "await_adapter_visibility_after_elevation: timeout")
		if elevationPhantomOnly {
			return failInstall(
				probeLocalTUNInstallCodeAdapterPhantomOnly,
				"await_adapter_visibility_after_elevation",
				"检测到 CloudHelper 仅存在 Phantom PnP 节点，未形成 present PnP + NetAdapter 联合可见，请以管理员重试并清理幽灵设备",
				fmt.Errorf("phantom-only adapter detected after elevation wait: %s", formatProbeLocalWintunVisibilityEvidence(lastElevationEvidence)),
			)
		}
		return failInstall(
			probeLocalTUNInstallCodeElevationTimeout,
			"await_adapter_visibility_after_elevation",
			"已触发管理员安装，但等待网卡可见超时，请确认 UAC 子进程已完成并检查设备管理器",
			fmt.Errorf("adapter is still not detectable after elevation request: %w", firstProbeLocalTUNErr(elevationDetectErr, errors.New("adapter not detected before timeout"))),
		)
	}
	steps = append(steps, "permission: admin")

	precheckEvidence, precheckErr := probeLocalInspectWintunVisibility()
	if precheckErr != nil {
		steps = append(steps, "detect_adapter_precheck: error")
		return failInstall(
			probeLocalTUNInstallCodeAdapterNotDetected,
			"detect_adapter_precheck",
			"无法确认现有 TUN 网卡状态，已禁止重建，请检查系统设备状态后重试",
			fmt.Errorf("inspect existing wintun adapter before create failed: %w", precheckErr),
		)
	}
	if precheckEvidence.NetAdapterMatched && precheckEvidence.NetAdapter.InterfaceIndex > 0 {
		observation.Visibility.IfIndexResolved = true
		observation.Visibility.IfIndexValue = precheckEvidence.NetAdapter.InterfaceIndex
		setProbeLocalWindowsRouteTargetEnv(precheckEvidence.NetAdapter.InterfaceIndex)
	}
	if precheckEvidence.isJointlyVisible() {
		observation.Visibility.DetectVisible = true
		steps = append(steps, "detect_adapter_precheck: found")
		setSuccessObservation("系统中已检测到 TUN 适配器（present PnP + NetAdapter）")
		logInstallSuccess()
		return nil
	}
	if precheckEvidence.NetAdapterMatched || precheckEvidence.PresentPnPMatched || precheckEvidence.PhantomPnPMatched {
		steps = append(steps, "detect_adapter_precheck: existing_target_instance")
		if precheckEvidence.isPhantomOnly() {
			removedPhantoms, removePhantomErr := probeLocalRemovePhantomWintunDevices()
			if removePhantomErr != nil {
				steps = append(steps, "detect_adapter_precheck: remove_phantom_failed")
			} else if removedPhantoms > 0 {
				steps = append(steps, "detect_adapter_precheck: remove_phantom_ok")
			}
		}
		if precheckEvidence.NetAdapterMatched {
			ifIndex := precheckEvidence.NetAdapter.InterfaceIndex
			if ifIndex <= 0 && precheckEvidence.NetAdapter.InterfaceLUID > 0 {
				if resolvedIfIndex, convertErr := probeLocalConvertInterfaceLUIDToIndex(precheckEvidence.NetAdapter.InterfaceLUID); convertErr == nil && resolvedIfIndex > 0 {
					ifIndex = resolvedIfIndex
					observation.Visibility.IfIndexResolved = true
					observation.Visibility.IfIndexValue = ifIndex
					setProbeLocalWindowsRouteTargetEnv(ifIndex)
					steps = append(steps, "detect_adapter_precheck: ifindex_recover_from_luid_ok")
				} else {
					steps = append(steps, "detect_adapter_precheck: ifindex_recover_from_luid_failed")
				}
			}
			if ifIndex > 0 {
				if routeErr := ensureProbeLocalWindowsRouteTargetByInterfaceIndex(ifIndex); routeErr != nil {
					steps = append(steps, "detect_adapter_precheck: route_target_repair_failed")
					setSuccessNotReadyObservation(
						probeLocalTUNInstallCodeAdapterJointVisibilityMiss,
						"已检测到既有 TUN 网卡，仅执行可用性修复；路由目标修复失败，请检查网卡状态后重试",
						fmt.Errorf("repair existing wintun route target failed: %w", routeErr),
					)
					logInstallSuccess()
					return nil
				}
				steps = append(steps, "detect_adapter_precheck: route_target_repair_ok")
			}
			setSuccessNotReadyObservation(
				probeLocalTUNInstallCodeAdapterJointVisibilityMiss,
				"已检测到既有 TUN 网卡，仅执行可用性修复；当前尚未满足 present PnP + NetAdapter 联合可见",
				errors.New("existing wintun adapter is not jointly visible after repair-only path"),
			)
			logInstallSuccess()
			return nil
		}
		return failInstall(
			probeLocalTUNInstallCodeAdapterPhantomOnly,
			"detect_adapter_precheck",
			"检测到既有目标 TUN 网卡实例（PnP），已禁止重建，请清理异常实例后重试",
			fmt.Errorf("existing target adapter instance is present but net adapter is unavailable: %s", formatProbeLocalWintunVisibilityEvidence(precheckEvidence)),
		)
	}
	steps = append(steps, "detect_adapter_precheck: not_found")

	libraryPath, err := probeLocalResolveWintunPath()
	if err != nil {
		steps = append(steps, "resolve_wintun_path: failed")
		return failInstall(
			probeLocalTUNInstallCodeWintunLibraryMissing,
			"resolve_wintun_path",
			"请确认 Wintun 库路径可解析",
			fmt.Errorf("resolve wintun library path: %w", err),
		)
	}
	captureDriverPackageEvidence(libraryPath)
	steps = append(steps, "resolve_wintun_path: ok")

	observation.Create.Called = true
	handle, err := probeLocalCreateWintunAdapter(libraryPath, probeLocalTUNAdapterName, probeLocalTUNTunnelType)
	observation.Create.HandleNonZero = handle != 0
	if err != nil {
		observation.Create.RawError = strings.TrimSpace(err.Error())
		steps = append(steps, "create_or_open_adapter: failed")
		return failInstall(
			probeLocalTUNInstallCodeAdapterCreateFailed,
			"create_or_open_adapter",
			"Wintun 适配器创建失败，请检查管理员权限与驱动状态",
			fmt.Errorf("create/open wintun adapter: %w", err),
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
			if handle == 0 {
				return
			}
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
	phantomOnlyDetected := false
	lastVisibilityEvidence := probeLocalWintunVisibilityEvidence{}
	for _, delay := range []time.Duration{0, 200 * time.Millisecond, 450 * time.Millisecond, 800 * time.Millisecond, 1200 * time.Millisecond, 1800 * time.Millisecond, 2500 * time.Millisecond, 3500 * time.Millisecond, 5000 * time.Millisecond, 6500 * time.Millisecond} {
		if delay > 0 {
			probeLocalTUNInstallSleep(delay)
		}
		evidence, err := probeLocalInspectWintunVisibility()
		if err != nil {
			detectErr = err
			steps = append(steps, "detect_adapter_retry: error")
			continue
		}
		if evidence.NetAdapterMatched && evidence.NetAdapter.InterfaceIndex > 0 {
			observation.Visibility.IfIndexResolved = true
			observation.Visibility.IfIndexValue = evidence.NetAdapter.InterfaceIndex
			setProbeLocalWindowsRouteTargetEnv(evidence.NetAdapter.InterfaceIndex)
		}
		if evidence.isJointlyVisible() {
			detected = true
			observation.Visibility.DetectVisible = true
			steps = append(steps, "detect_adapter_retry: found")
			break
		}
		if evidence.isPhantomOnly() {
			phantomOnlyDetected = true
			lastVisibilityEvidence = evidence
			steps = append(steps, "detect_adapter_retry: phantom_only")
			continue
		}
		steps = append(steps, "detect_adapter_retry: not_found")
	}
	if detected {
		steps = append(steps, "verify_adapter: found")
		if createdOrOpened {
			probeLocalRetainWintunAdapterHandle(libraryPath, handle)
			handle = 0
			steps = append(steps, "retain_adapter_handle: ok")
		}
		setSuccessObservation("创建后检测到 TUN 适配器可见")
		logInstallSuccess()
		return nil
	}
	if createdOrOpened && phantomOnlyDetected {
		steps = append(steps, "verify_adapter: phantom_only_detected")
		removedPhantoms, removePhantomErr := probeLocalRemovePhantomWintunDevices()
		if removePhantomErr != nil {
			steps = append(steps, "verify_adapter: remove_phantom_failed")
		} else if removedPhantoms > 0 {
			steps = append(steps, "verify_adapter: remove_phantom_ok")
		}
		probeLocalRetainWintunAdapterHandle(libraryPath, handle)
		handle = 0
		steps = append(steps, "retain_adapter_handle: ok")
		setSuccessNotReadyObservation(
			probeLocalTUNInstallCodeAdapterPhantomOnly,
			"检测到 Phantom 可见性异常，仅执行可用性修复并禁止重建，请清理异常实例后重试",
			fmt.Errorf("phantom-only adapter detected in repair-only mode: removed_phantom=%d remove_err=%v evidence=%s", removedPhantoms, removePhantomErr, formatProbeLocalWintunVisibilityEvidence(lastVisibilityEvidence)),
		)
		logInstallSuccess()
		return nil
	}
	if detectErr != nil {
		steps = append(steps, "detect_adapter_retry: failed")
		if createdOrOpened {
			if handleLUID != 0 {
				ifIndex, convertErr := probeLocalConvertInterfaceLUIDToIndex(handleLUID)
				if convertErr == nil && ifIndex > 0 {
					observation.Visibility.IfIndexResolved = true
					observation.Visibility.IfIndexValue = ifIndex
					setProbeLocalWindowsRouteTargetEnv(ifIndex)
					steps = append(steps, "verify_adapter: detect_error_but_luid_ifindex_resolved")
				}
			}
			probeLocalRetainWintunAdapterHandle(libraryPath, handle)
			handle = 0
			steps = append(steps, "retain_adapter_handle: ok")
			setSuccessNotReadyObservation(
				probeLocalTUNInstallCodeAdapterJointVisibilityMiss,
				"驱动安装流程已完成且适配器已创建/打开，但联合可见检测失败，请稍后刷新确认",
				fmt.Errorf("verify wintun adapter after install detect failed: %w", detectErr),
			)
			logInstallSuccess()
			return nil
		}
		return failInstall(
			probeLocalTUNInstallCodeAdapterNotDetected,
			"verify_adapter",
			"安装后未检测到网卡，请检查驱动与设备管理器",
			fmt.Errorf("verify wintun adapter after install: %w", detectErr),
		)
	}
	if createdOrOpened {
		if handleLUID != 0 {
			for _, delay := range []time.Duration{0, 300 * time.Millisecond, 700 * time.Millisecond, 1200 * time.Millisecond, 1800 * time.Millisecond} {
				if delay > 0 {
					probeLocalTUNInstallSleep(delay)
				}
				adapterByLUID, ok, findErr := probeLocalFindWintunAdapterByLUID(handleLUID)
				if findErr != nil {
					steps = append(steps, "verify_adapter: fallback_luid_adapter_lookup_error")
					continue
				}
				if ok {
					if adapterByLUID.InterfaceIndex > 0 {
						observation.Visibility.IfIndexResolved = true
						observation.Visibility.IfIndexValue = adapterByLUID.InterfaceIndex
						setProbeLocalWindowsRouteTargetEnv(adapterByLUID.InterfaceIndex)
					}
					evidence, visibilityErr := probeLocalInspectWintunVisibility()
					if visibilityErr == nil && evidence.isJointlyVisible() {
						observation.Visibility.DetectVisible = true
						steps = append(steps, "verify_adapter: fallback_luid_adapter_visible")
						probeLocalRetainWintunAdapterHandle(libraryPath, handle)
						handle = 0
						steps = append(steps, "retain_adapter_handle: ok")
						setSuccessObservation("通过 LUID 枚举与 present PnP 联合确认 TUN 适配器可见")
						logInstallSuccess()
						return nil
					}
					recreateReasonCode := probeLocalTUNInstallCodeAdapterJointVisibilityMiss
					recreateHint := "LUID 可解析但缺少 present PnP + NetAdapter 联合可见"
					recreateCause := fmt.Errorf("fallback luid resolved but joint visibility missing: %w", firstProbeLocalTUNErr(visibilityErr, errors.New("joint visibility missing")))
					if visibilityErr == nil && evidence.isPhantomOnly() {
						recreateReasonCode = probeLocalTUNInstallCodeAdapterPhantomOnly
						recreateHint = "LUID 可解析但仅存在 Phantom PnP 节点，不满足联合可见"
						recreateCause = fmt.Errorf("fallback luid resolved but adapter is phantom-only: %s", formatProbeLocalWintunVisibilityEvidence(evidence))
					}
					steps = append(steps, "verify_adapter: fallback_visibility_conflict_no_recreate")
					probeLocalRetainWintunAdapterHandle(libraryPath, handle)
					handle = 0
					steps = append(steps, "retain_adapter_handle: ok")
					setSuccessNotReadyObservation(
						recreateReasonCode,
						"检测到可见性冲突，仅执行可用性修复并禁止重建，请稍后刷新确认",
						fmt.Errorf("fallback visibility conflict in repair-only mode: %w; hint=%s", recreateCause, recreateHint),
					)
					observation.Diagnostic.Stage = "verify_adapter"
					observation.Diagnostic.Hint = recreateHint
					observation.Diagnostic.Details = strings.TrimSpace(recreateCause.Error())
					setProbeLocalTUNInstallObservation(observation)
					logInstallSuccess()
					return nil
				}
				steps = append(steps, "verify_adapter: fallback_luid_adapter_not_found")
			}
			ifIndex, convertErr := probeLocalConvertInterfaceLUIDToIndex(handleLUID)
			if convertErr == nil && ifIndex > 0 {
				observation.Visibility.IfIndexResolved = true
				observation.Visibility.IfIndexValue = ifIndex
				setProbeLocalWindowsRouteTargetEnv(ifIndex)
				steps = append(steps, "verify_adapter: fallback_luid_ifindex_diagnostic_only")
			} else {
				steps = append(steps, "verify_adapter: fallback_luid_ifindex_failed")
			}
			probeLocalRetainWintunAdapterHandle(libraryPath, handle)
			handle = 0
			steps = append(steps, "retain_adapter_handle: ok")
			steps = append(steps, "verify_adapter: fallback_luid_ifindex_confirm_install_not_ready")
			setSuccessNotReadyObservation(
				probeLocalTUNInstallCodeAdapterJointVisibilityMiss,
				"已完成驱动安装并可通过 LUID/ifIndex 识别适配器，但尚未满足 present PnP + NetAdapter 联合可见，请稍后刷新确认",
				fmt.Errorf("wintun adapter joint visibility missing after install; luid=%d ifindex=%d err=%w", handleLUID, ifIndex, firstProbeLocalTUNErr(convertErr, errors.New("adapter not visible in system enumeration"))),
			)
			logInstallSuccess()
			return nil
		}
		steps = append(steps, "verify_adapter: created_handle_but_not_jointly_visible")
		probeLocalRetainWintunAdapterHandle(libraryPath, handle)
		handle = 0
		steps = append(steps, "retain_adapter_handle: ok")
		setSuccessNotReadyObservation(
			probeLocalTUNInstallCodeAdapterJointVisibilityMiss,
			"已完成驱动安装并已创建/打开适配器，但尚未满足 present PnP + NetAdapter 联合可见，请稍后刷新确认",
			errors.New("wintun adapter was created/opened but joint visibility is not reached yet"),
		)
		logInstallSuccess()
		return nil
	}
	steps = append(steps, "verify_adapter: not_detected")
	return failInstall(
		probeLocalTUNInstallCodeAdapterNotDetected,
		"verify_adapter",
		"安装后未检测到网卡，请检查驱动与系统策略",
		errors.New("wintun adapter is not detected after install"),
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

func retainProbeLocalWintunAdapterHandle(libraryPath string, handle uintptr) {
	if handle == 0 {
		return
	}
	cleanPath := strings.TrimSpace(libraryPath)
	if cleanPath == "" {
		return
	}
	probeLocalRetainedWintunAdapterState.mu.Lock()
	prevPath := probeLocalRetainedWintunAdapterState.libraryPath
	prevHandle := probeLocalRetainedWintunAdapterState.handle
	if prevHandle == handle && strings.EqualFold(prevPath, cleanPath) {
		probeLocalRetainedWintunAdapterState.mu.Unlock()
		return
	}
	probeLocalRetainedWintunAdapterState.libraryPath = cleanPath
	probeLocalRetainedWintunAdapterState.handle = handle
	probeLocalRetainedWintunAdapterState.mu.Unlock()
	if prevHandle != 0 {
		if closeErr := probeLocalCloseWintunAdapter(prevPath, prevHandle); closeErr != nil {
			logProbeWarnf("release previous retained wintun adapter handle failed: %v", closeErr)
		}
	}
}

func releaseProbeLocalRetainedWintunAdapterHandle() {
	probeLocalRetainedWintunAdapterState.mu.Lock()
	path := probeLocalRetainedWintunAdapterState.libraryPath
	handle := probeLocalRetainedWintunAdapterState.handle
	probeLocalRetainedWintunAdapterState.libraryPath = ""
	probeLocalRetainedWintunAdapterState.handle = 0
	probeLocalRetainedWintunAdapterState.mu.Unlock()
	if handle != 0 {
		if closeErr := probeLocalCloseWintunAdapter(path, handle); closeErr != nil {
			logProbeWarnf("release retained wintun adapter handle failed: %v", closeErr)
		}
	}
}

func formatProbeLocalWintunVisibilityEvidence(evidence probeLocalWintunVisibilityEvidence) string {
	parts := []string{
		fmt.Sprintf("net_adapter=%t", evidence.NetAdapterMatched),
		fmt.Sprintf("present_pnp=%t", evidence.PresentPnPMatched),
		fmt.Sprintf("phantom_pnp=%t", evidence.PhantomPnPMatched),
	}
	if evidence.NetAdapterMatched {
		parts = append(parts, fmt.Sprintf("ifindex=%d", evidence.NetAdapter.InterfaceIndex))
		if strings.TrimSpace(evidence.NetAdapter.Name) != "" {
			parts = append(parts, fmt.Sprintf("adapter_name=%s", strings.TrimSpace(evidence.NetAdapter.Name)))
		}
	}
	if strings.TrimSpace(evidence.MatchedPnPFriendlyName) != "" {
		parts = append(parts, fmt.Sprintf("pnp_name=%s", strings.TrimSpace(evidence.MatchedPnPFriendlyName)))
	}
	if strings.TrimSpace(evidence.MatchedPnPStatus) != "" {
		parts = append(parts, fmt.Sprintf("pnp_status=%s", strings.TrimSpace(evidence.MatchedPnPStatus)))
	}
	if strings.TrimSpace(evidence.MatchedPnPProblem) != "" {
		parts = append(parts, fmt.Sprintf("pnp_problem=%s", strings.TrimSpace(evidence.MatchedPnPProblem)))
	}
	if strings.TrimSpace(evidence.MatchedPnPInstanceID) != "" {
		parts = append(parts, fmt.Sprintf("pnp_instance=%s", strings.TrimSpace(evidence.MatchedPnPInstanceID)))
	}
	return strings.Join(parts, ",")
}

func resetProbeLocalTUNInstallWindowsHooksForTest() {
	releaseProbeLocalRetainedWintunAdapterHandle()
	probeLocalEnsureWintunLibrary = ensureProbeEmbeddedWintunLibrary
	probeLocalResolveWintunPath = resolveProbeWintunPath
	probeLocalDetectWintunAdapter = detectProbeLocalWintunAdapter
	probeLocalInspectWintunVisibility = inspectProbeLocalWintunVisibility
	probeLocalRemovePhantomWintunDevices = removeProbeLocalPhantomWintunDevices
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
	probeLocalRetainWintunAdapterHandle = retainProbeLocalWintunAdapterHandle
}
