//go:build !windows

package backend

import "errors"

// ErrRelaunchAsAdmin は Windows 専用のため、他プラットフォームでは使用されない。
var ErrRelaunchAsAdmin = errors.New("relaunch as admin")

// isWindowsAdmin は非 Windows では常に true を返す（権限昇格不要）。
func isWindowsAdmin() bool { return true }

// relaunchAsAdmin は非 Windows では使用されない。
func relaunchAsAdmin() error { return ErrRelaunchAsAdmin }
