package core

import (
	"crypto/ed25519"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"strings"
	"time"
)

const probeVirtualRouterAuthTicketVersion = "route-auth-v3"

var probeVirtualRouterAuthTicketNow = time.Now

type probeVirtualRouterAuthTicketPayload struct {
	Version       string `json:"version"`
	RouteID       string `json:"route_id"`
	ClientEntryID string `json:"client_entry_id,omitempty"`
	UserID        string `json:"user_id"`
	UserPublicKey string `json:"user_public_key"`
	FromNodeID    string `json:"from_node_id"`
	ToNodeID      string `json:"to_node_id"`
	TicketID      string `json:"ticket_id"`
	IssuedAt      string `json:"issued_at"`
	ExpiresAt     string `json:"expires_at"`
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
	issuedAt := probeVirtualRouterMonthlyAuthTicketIssuedAt(probeVirtualRouterAuthTicketNow())
	issuedTime, _ := time.Parse(time.RFC3339, issuedAt)
	ticketSeed := strings.Join([]string{routeID, clientEntryID, strings.TrimSpace(rule.FromNodeID), strings.TrimSpace(rule.ToNodeID), strings.TrimSpace(rule.Secret), issuedAt}, "\n")
	ticketSum := sha256.Sum256([]byte(ticketSeed))
	payload := probeVirtualRouterAuthTicketPayload{
		Version:       probeVirtualRouterAuthTicketVersion,
		RouteID:       routeID,
		ClientEntryID: clientEntryID,
		UserID:        strings.TrimSpace(rule.UserID),
		UserPublicKey: userPublicKey,
		FromNodeID:    normalizeProbeNodeID(rule.FromNodeID),
		ToNodeID:      normalizeProbeNodeID(rule.ToNodeID),
		TicketID:      hex.EncodeToString(ticketSum[:16]),
		IssuedAt:      issuedAt,
		ExpiresAt:     issuedTime.Add(35 * 24 * time.Hour).Format(time.RFC3339),
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
