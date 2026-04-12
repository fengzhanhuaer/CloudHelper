package handler

import (
	"context"
	"encoding/json"
	"net/http"
	"strconv"
	"strings"
	"time"

	"github.com/cloudhelper/manager_service/internal/adapter/node"
	"github.com/cloudhelper/manager_service/internal/api/response"
)

// NodeHandler handles probe node CRUD and link test endpoints.
// PKG-W2-02 / RQ-004
type NodeHandler struct {
	store *node.Store
}

// NewNodeHandler constructs a NodeHandler.
func NewNodeHandler(store *node.Store) *NodeHandler {
	return &NodeHandler{store: store}
}

// List handles GET /api/probe/nodes
func (h *NodeHandler) List(w http.ResponseWriter, r *http.Request) {
	rid := r.Header.Get("X-Request-ID")
	nodes, err := h.store.List()
	if err != nil {
		response.Internal(w, rid, "failed to load nodes: "+err.Error())
		return
	}
	response.OK(w, rid, nodes)
}

// Create handles POST /api/probe/nodes
// Body: { "node_name": "..." }
func (h *NodeHandler) Create(w http.ResponseWriter, r *http.Request) {
	rid := r.Header.Get("X-Request-ID")
	var req struct {
		NodeName string `json:"node_name"`
	}
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		response.BadRequest(w, rid, "invalid request body")
		return
	}
	n, err := h.store.Create(req.NodeName)
	if err != nil {
		response.BadRequest(w, rid, err.Error())
		return
	}
	response.OK(w, rid, n)
}

// Update handles PUT /api/probe/nodes/{node_no}
func (h *NodeHandler) Update(w http.ResponseWriter, r *http.Request) {
	rid := r.Header.Get("X-Request-ID")
	nodeNoStr := r.PathValue("node_no")
	nodeNo, err := strconv.Atoi(nodeNoStr)
	if err != nil || nodeNo <= 0 {
		response.BadRequest(w, rid, "invalid node_no")
		return
	}
	var req struct {
		NodeName      string `json:"node_name"`
		Remark        string `json:"remark"`
		TargetSystem  string `json:"target_system"`
		DirectConnect bool   `json:"direct_connect"`
		PaymentCycle  string `json:"payment_cycle"`
		Cost          string `json:"cost"`
		ExpireAt      string `json:"expire_at"`
		VendorName    string `json:"vendor_name"`
		VendorURL     string `json:"vendor_url"`
	}
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		response.BadRequest(w, rid, "invalid request body")
		return
	}
	updated, err := h.store.Update(nodeNo, node.UpdateSettings{
		NodeName:      req.NodeName,
		Remark:        req.Remark,
		TargetSystem:  req.TargetSystem,
		DirectConnect: req.DirectConnect,
		PaymentCycle:  req.PaymentCycle,
		Cost:          req.Cost,
		ExpireAt:      req.ExpireAt,
		VendorName:    req.VendorName,
		VendorURL:     req.VendorURL,
	})
	if err != nil {
		response.BadRequest(w, rid, err.Error())
		return
	}
	response.OK(w, rid, updated)
}

// TestLink handles POST /api/probe/link/test
// Body: { "node_id":"", "endpoint_type":"http|https|http3|service|public", "scheme":"", "host":"", "port": 0 }
func (h *NodeHandler) TestLink(w http.ResponseWriter, r *http.Request) {
	rid := r.Header.Get("X-Request-ID")
	var req struct {
		NodeID       string `json:"node_id"`
		EndpointType string `json:"endpoint_type"`
		Scheme       string `json:"scheme"`
		Host         string `json:"host"`
		Port         int    `json:"port"`
	}
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		response.BadRequest(w, rid, "invalid request body")
		return
	}
	ctx, cancel := context.WithTimeout(r.Context(), 15*time.Second)
	defer cancel()

	result, err := node.TestLink(ctx,
		strings.TrimSpace(req.NodeID),
		strings.TrimSpace(req.EndpointType),
		strings.TrimSpace(req.Scheme),
		strings.TrimSpace(req.Host),
		req.Port,
	)
	if err != nil {
		response.BadRequest(w, rid, err.Error())
		return
	}
	response.OK(w, rid, result)
}
