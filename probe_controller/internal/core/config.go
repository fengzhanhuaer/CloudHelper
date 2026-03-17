package core

import "time"

const (
	listenAddr           = "127.0.0.1:15030"
	dataDir              = "./data"
	mainStoreFile        = "cloudhelper.json"
	probeConfigStoreFile = "probe_config.json"
	blacklistStoreFile   = "blacklist.json"

	nonceTTL          = 30 * time.Second
	sessionTTL        = 1 * time.Hour
	nonceRequestLimit = 5
)
