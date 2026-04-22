package dashboard

import (
	_ "embed"
	"net/http"
)

var (
	//go:embed page.html
	pageHTML string
)

func Handler(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	_, _ = w.Write([]byte(pageHTML))
}
