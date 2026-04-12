package handler

import (
	"strconv"

	"github.com/cloudhelper/manager_service/internal/adapter/node"
	"github.com/cloudhelper/manager_service/internal/api/response"
	"github.com/gin-gonic/gin"
)

type NodeHandler struct {
	store *node.Store
}

func NewNodeHandler(store *node.Store) *NodeHandler {
	return &NodeHandler{store: store}
}

func (h *NodeHandler) List(c *gin.Context) {
	rid := c.GetString("RequestID")
	nodes, err := h.store.List()
	if err != nil {
		response.Internal(c, rid, "failed to load nodes: "+err.Error())
		return
	}
	response.OK(c, rid, nodes)
}

func (h *NodeHandler) Create(c *gin.Context) {
	rid := c.GetString("RequestID")
	var req struct {
		NodeName string `json:"node_name"`
	}
	if err := c.ShouldBindJSON(&req); err != nil {
		response.BadRequest(c, rid, "invalid request body")
		return
	}
	n, err := h.store.Create(req.NodeName)
	if err != nil {
		response.BadRequest(c, rid, err.Error())
		return
	}
	response.OK(c, rid, n)
}

func (h *NodeHandler) Update(c *gin.Context) {
	rid := c.GetString("RequestID")
	nodeNoStr := c.Param("node_no")
	nodeNo, err := strconv.Atoi(nodeNoStr)
	if err != nil || nodeNo <= 0 {
		response.BadRequest(c, rid, "invalid node_no")
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
	if err := c.ShouldBindJSON(&req); err != nil {
		response.BadRequest(c, rid, "invalid request body")
		return
	}
	updated, err := h.store.Update(nodeNo, node.UpdateSettings(req))
	if err != nil {
		response.BadRequest(c, rid, err.Error())
		return
	}
	response.OK(c, rid, updated)
}

func (h *NodeHandler) TestLink(c *gin.Context) {
	rid := c.GetString("RequestID")
	var req struct {
		NodeID       string `json:"node_id"`
		EndpointType string `json:"endpoint_type"`
		Scheme       string `json:"scheme"`
		Host         string `json:"host"`
		Port         int    `json:"port"`
	}
	if err := c.ShouldBindJSON(&req); err != nil {
		response.BadRequest(c, rid, "invalid request body")
		return
	}
	ctx, cancel := c.Request.Context(), func() {}
	result, err := node.TestLink(ctx, req.NodeID, req.EndpointType, req.Scheme, req.Host, req.Port)
	if err != nil {
		response.BadRequest(c, rid, err.Error())
		return
	}
	defer cancel()
	response.OK(c, rid, result)
}
