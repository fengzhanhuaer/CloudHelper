package core

import "strings"

// Optional, additive report from the native router updater. Older reporters
// omit it; absence clears stale progress rather than retaining a finished job.
type probeRouterUpgradeStatus struct {
	Phase            string `json:"phase"`
	Message          string `json:"message"`
	Running          bool   `json:"running"`
	LatestVersion    string `json:"latest_version"`
	DownloadedBytes  int64  `json:"downloaded_bytes"`
	TotalBytes       int64  `json:"total_bytes"`
	SpeedBytesPerSec int64  `json:"speed_bytes_per_sec"`
	Source           string `json:"source"`
	Endpoint         string `json:"endpoint"`
	HTTPStatus       int    `json:"http_status"`
	CurlCode         int    `json:"curl_code"`
	Error            string `json:"error"`
}

func updateProbeRuntimeRouterPluginUpgradeStatus(nodeID string, status *probeRouterUpgradeStatus) {
	probeRuntimeStore.mu.Lock()
	defer probeRuntimeStore.mu.Unlock()
	current := probeRuntimeStore.data[nodeID]
	current.RouterPluginUpgradeStatus = nil
	if current.RouterPluginUpgrade && status != nil {
		switch status.Phase {
		case "checking", "checked", "downloading", "verifying", "applying", "complete", "failed":
			copy := *status
			copy.Source = truncateRouterUpgradeText(copy.Source, 32)
			copy.Message = truncateRouterUpgradeText(copy.Message, 256)
			copy.Error = truncateRouterUpgradeText(copy.Error, 256)
			copy.Endpoint = truncateRouterUpgradeText(copy.Endpoint, 1024)
			copy.LatestVersion = truncateRouterUpgradeText(copy.LatestVersion, 64)
			current.RouterPluginUpgradeStatus = &copy
		}
	}
	probeRuntimeStore.data[nodeID] = current
}

func truncateRouterUpgradeText(value string, limit int) string {
	runes := []rune(strings.TrimSpace(value))
	if len(runes) > limit {
		runes = runes[:limit]
	}
	return string(runes)
}
