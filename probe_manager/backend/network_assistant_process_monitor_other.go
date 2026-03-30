//go:build !windows

package backend

// pidNameByPort 在非 Windows 平台上不支持，返回空字符串。
func pidNameByPort(port uint16, isUDP bool) string {
	return ""
}

// listRunningProcessesPlatform 在非 Windows 平台上返回空列表。
func listRunningProcessesPlatform() []NetworkProcessInfo {
	return nil
}
