package core

import (
	"crypto/ed25519"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"strings"
	"time"
)

const probeVirtualRouterAuthTicketVersion = "route-auth-v1"

var probeVirtualRouterAuthTicketNow = time.Now

type probeVirtualRouterAuthTicketPayload struct {
	Version       string `json:"v"`
	RouteID       string `json:"route_id"`
	ClientEntryID string `json:"client_entry_id,omitempty"`
	UserID        string `json:"user_id"`
	UserPublicKey string `json:"user_public_key"`
	IssuedAt      string `json:"issued_at"`
}

func buildProbeVirtualRouterAuthTicket(rule probeVirtualRouterTopologyRule, priv ed25519.PrivateKey) (string, error) {
	routeID := probeVirtualRouterRuntimeRouteID(rule)
	if routeID == "" {
		return "", fmt.Errorf("route_id is required")
	}
	if len(priv) != ed25519.PrivateKeySize {
		return "", fmt.Errorf("admin private key is invalid")
	}
	userPublicKey := strings.TrimSpace(rule.UserPublicKey)
	if userPublicKey == "" {
		return "", fmt.Errorf("user_public_key is required")
	}
	clientEntryID := strings.TrimSpace(rule.ID)
	if clientEntryID == "" {
		clientEntryID = routeID
	}
	payload := probeVirtualRouterAuthTicketPayload{
		Version:       probeVirtualRouterAuthTicketVersion,
		RouteID:       routeID,
		ClientEntryID: clientEntryID,
		UserID:        strings.TrimSpace(rule.UserID),
		UserPublicKey: userPublicKey,
		IssuedAt:      probeVirtualRouterMonthlyAuthTicketIssuedAt(probeVirtualRouterAuthTicketNow()),
	}
	payloadBytes, err := json.Marshal(payload)
	if err != nil {
		return "", err
	}
	sig := ed25519.Sign(priv, payloadBytes)
	enc := base64.RawURLEncoding
	return enc.EncodeToString(payloadBytes) + "." + enc.EncodeToString(sig), nil
}

func probeVirtualRouterMonthlyAuthTicketIssuedAt(now time.Time) string {
	utc := now.UTC()
	year, month, _ := utc.Date()
	return time.Date(year, month, 1, 0, 0, 0, 0, time.UTC).Format(time.RFC3339)
}
