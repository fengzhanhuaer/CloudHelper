package main

import (
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
	"testing"
)

func TestFrontendImportsExistInWailsBindings(t *testing.T) {
	dtsExports := loadWailsExportSet(t, filepath.Join("frontend", "wailsjs", "go", "main", "App.d.ts"))
	jsExports := loadWailsExportSet(t, filepath.Join("frontend", "wailsjs", "go", "main", "App.js"))

	importsByFile := collectFrontendWailsNamedImports(t, filepath.Join("frontend", "src"))
	missingInDTS := make([]string, 0)
	missingInJS := make([]string, 0)

	files := make([]string, 0, len(importsByFile))
	for file := range importsByFile {
		files = append(files, file)
	}
	sort.Strings(files)

	for _, file := range files {
		names := importsByFile[file]
		sort.Strings(names)
		for _, name := range names {
			if _, ok := dtsExports[name]; !ok {
				missingInDTS = append(missingInDTS, fmt.Sprintf("%s (%s)", name, file))
			}
			if _, ok := jsExports[name]; !ok {
				missingInJS = append(missingInJS, fmt.Sprintf("%s (%s)", name, file))
			}
		}
	}

	if len(missingInDTS) > 0 || len(missingInJS) > 0 {
		sort.Strings(missingInDTS)
		sort.Strings(missingInJS)
		t.Fatalf(
			"前端导入与 Wails 绑定不一致。\nApp.d.ts 缺失:\n  %s\nApp.js 缺失:\n  %s",
			joinOrNone(missingInDTS),
			joinOrNone(missingInJS),
		)
	}
}

func loadWailsExportSet(t *testing.T, path string) map[string]struct{} {
	t.Helper()
	raw, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("读取 %s 失败: %v", path, err)
	}
	re := regexp.MustCompile(`(?m)export\s+function\s+([A-Za-z_][A-Za-z0-9_]*)\s*\(`)
	matches := re.FindAllStringSubmatch(string(raw), -1)
	set := make(map[string]struct{}, len(matches))
	for _, m := range matches {
		set[m[1]] = struct{}{}
	}
	return set
}

func collectFrontendWailsNamedImports(t *testing.T, root string) map[string][]string {
	t.Helper()
	result := map[string][]string{}
	importRe := regexp.MustCompile(`(?s)import\s*\{([^}]*)\}\s*from\s*["'][^"']*wailsjs/go/main/App["']`)

	err := filepath.WalkDir(root, func(path string, d os.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if d.IsDir() {
			return nil
		}
		ext := strings.ToLower(filepath.Ext(path))
		if ext != ".ts" && ext != ".tsx" {
			return nil
		}

		raw, readErr := os.ReadFile(path)
		if readErr != nil {
			return readErr
		}
		content := string(raw)
		matches := importRe.FindAllStringSubmatch(content, -1)
		if len(matches) == 0 {
			return nil
		}

		namesSet := map[string]struct{}{}
		for _, m := range matches {
			block := m[1]
			parts := strings.Split(block, ",")
			for _, part := range parts {
				item := strings.TrimSpace(part)
				if item == "" {
					continue
				}
				if strings.HasPrefix(item, "type ") {
					continue
				}
				// 兼容 import { Foo as Bar }
				if idx := strings.Index(item, " as "); idx > 0 {
					item = strings.TrimSpace(item[:idx])
				}
				if item == "" {
					continue
				}
				namesSet[item] = struct{}{}
			}
		}

		names := make([]string, 0, len(namesSet))
		for name := range namesSet {
			names = append(names, name)
		}
		sort.Strings(names)
		rel, relErr := filepath.Rel(".", path)
		if relErr != nil {
			rel = path
		}
		result[rel] = names
		return nil
	})
	if err != nil {
		t.Fatalf("扫描前端导入失败: %v", err)
	}
	return result
}

func joinOrNone(values []string) string {
	if len(values) == 0 {
		return "<none>"
	}
	return strings.Join(values, "\n  ")
}
