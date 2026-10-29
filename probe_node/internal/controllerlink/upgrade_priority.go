package controllerlink

import "context"

type upgradePriorityKey struct{}

// WithUpgradePriority keeps the authentication challenge on the same IP-first
// policy as its upgrade request, without adding a wire header or changing auth.
func WithUpgradePriority(ctx context.Context) context.Context {
	return context.WithValue(ctx, upgradePriorityKey{}, true)
}

func IsUpgradePath(path string) bool {
	return path == "/api/probe/proxy/download" || path == "/api/probe/proxy/github/latest"
}
