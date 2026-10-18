package mobilecore

import (
	"github.com/cloudhelper/probe_node/internal/controllerlink"
	"net/http"
)

func ConfigureControllerCache(controller, dir string) string {
	controllerlink.Default.Configure(controller, dir)
	return "controller cache configured"
}

func mobileControllerDo(req *http.Request) (*http.Response, error) {
	base := http.DefaultTransport.(*http.Transport).Clone()
	base.Proxy = nil
	tr := controllerlink.Default.Wrap(req.URL.String(), base, nil)
	defer tr.CloseIdleConnections()
	return (&http.Client{Transport: tr, CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }}).Do(req)
}
