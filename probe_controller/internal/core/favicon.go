package core

import "net/http"

const dashboardFaviconSVG = `<svg xmlns="http://www.w3.org/2000/svg" viewBox="0 0 64 64">
  <defs>
    <linearGradient id="g" x1="0" y1="0" x2="1" y2="1">
      <stop offset="0%" stop-color="#58a6ff" />
      <stop offset="100%" stop-color="#2ea043" />
    </linearGradient>
  </defs>
  <rect x="6" y="6" width="52" height="52" rx="12" fill="#0b1628" />
  <path d="M22 21L14 32L22 43" stroke="url(#g)" stroke-width="5" stroke-linecap="round" stroke-linejoin="round" fill="none" />
  <path d="M42 21L50 32L42 43" stroke="url(#g)" stroke-width="5" stroke-linecap="round" stroke-linejoin="round" fill="none" />
  <path d="M30 17L34 47" stroke="#9cd0ff" stroke-width="5" stroke-linecap="round" />
</svg>`

func faviconSVGHandler(w http.ResponseWriter, r *http.Request) {
	if !isFaviconSVGPath(r.URL.Path) {
		http.NotFound(w, r)
		return
	}
	if r.Method != http.MethodGet && r.Method != http.MethodHead {
		http.Error(w, "Method not allowed", http.StatusMethodNotAllowed)
		return
	}

	w.Header().Set("Content-Type", "image/svg+xml; charset=utf-8")
	w.Header().Set("Cache-Control", "public, max-age=86400")
	if r.Method == http.MethodHead {
		w.WriteHeader(http.StatusOK)
		return
	}
	_, _ = w.Write([]byte(dashboardFaviconSVG))
}

func faviconICOHandler(w http.ResponseWriter, r *http.Request) {
	if !isFaviconICOPath(r.URL.Path) {
		http.NotFound(w, r)
		return
	}
	if r.Method != http.MethodGet && r.Method != http.MethodHead {
		http.Error(w, "Method not allowed", http.StatusMethodNotAllowed)
		return
	}
	target := "/favicon.svg"
	if hasDashboardPrefix(r.URL.Path) {
		target = "/dashboard/favicon.svg"
	}
	http.Redirect(w, r, target, http.StatusFound)
}

func isFaviconSVGPath(path string) bool {
	switch path {
	case "/favicon.svg", "/dashboard/favicon.svg":
		return true
	default:
		return false
	}
}

func isFaviconICOPath(path string) bool {
	switch path {
	case "/favicon.ico", "/dashboard/favicon.ico":
		return true
	default:
		return false
	}
}

func hasDashboardPrefix(path string) bool {
	return len(path) >= len("/dashboard/") && path[:len("/dashboard/")] == "/dashboard/"
}
