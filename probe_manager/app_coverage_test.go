package main

import (
	"reflect"
	"strings"
	"testing"

	"github.com/cloudhelper/probe_manager/backend"
)

// TestAppWrapsAllBackendMethods 确保 backend.App 的每一个导出方法都在 main.App 中
// 有对应的包装（Wails 只绑定 main.App，漏加包装会导致前端调用时报"功能不可用"）。
//
// 每次在 backend.App 新增方法后：
//   - 若该方法需要暴露给前端 → 在 app.go 中添加对应包装，测试自动通过
//   - 若该方法是内部/生命周期方法，不应暴露 → 将方法名加入下方 skipMethods 集合
func TestAppWrapsAllBackendMethods(t *testing.T) {
	// 不需要暴露给前端的 backend.App 方法，加入此集合即可跳过检查
	skipMethods := map[string]bool{
		"Startup":  true, // Wails 生命周期，由 main.go 通过 OnStartup 注册
		"Shutdown": true, // Wails 生命周期，由 main.go 通过 OnShutdown 注册
	}

	backendType := reflect.TypeOf((*backend.App)(nil))
	mainType := reflect.TypeOf((*App)(nil))

	var missing []string
	for i := 0; i < backendType.NumMethod(); i++ {
		method := backendType.Method(i)
		if skipMethods[method.Name] {
			continue
		}
		if _, ok := mainType.MethodByName(method.Name); !ok {
			missing = append(missing, method.Name)
		}
	}

	if len(missing) > 0 {
		t.Errorf(
			"以下 backend.App 方法在 main.App (app.go) 中缺少包装，Wails 前端将无法调用：\n\n  %s\n\n"+
				"请在 app.go 中添加对应包装方法，或将方法名加入 skipMethods（如果该方法不应暴露给前端）。",
			strings.Join(missing, "\n  "),
		)
	}
}
