package handler

import (
	"encoding/json"
	"net/http"

	"github.com/cloudhelper/manager_service/internal/adapter/netassist"
	"github.com/cloudhelper/manager_service/internal/api/response"
	"github.com/gin-gonic/gin"
)

type NetAssistHandler struct {
	client *netassist.Client
}

func NewNetAssistHandler(client *netassist.Client) *NetAssistHandler {
	return &NetAssistHandler{client: client}
}

func (h *NetAssistHandler) GetStatus(c *gin.Context) {
	rid := c.GetString("RequestID")
	raw, err := h.client.GetStatus(c.Request.Context(), "")
	if err != nil {
		response.Internal(c, rid, "failed to fetch net assist status: "+err.Error())
		return
	}
	
	envelope := response.Envelope{Code: response.CodeOK, Message: "success", RequestID: rid}
	var data interface{}
	if err := json.Unmarshal(raw, &data); err == nil {
		envelope.Data = data
	} else {
		envelope.Data = json.RawMessage(raw)
	}
	c.JSON(http.StatusOK, envelope)
}

func (h *NetAssistHandler) SwitchMode(c *gin.Context) {
	rid := c.GetString("RequestID")
	var req struct {
		Mode string `json:"mode"`
	}
	if err := c.ShouldBindJSON(&req); err != nil {
		response.BadRequest(c, rid, "invalid request body")
		return
	}
	raw, err := h.client.SwitchMode(c.Request.Context(), "", req.Mode)
	if err != nil {
		response.Internal(c, rid, "failed to switch mode: "+err.Error())
		return
	}
	
	envelope := response.Envelope{Code: response.CodeOK, Message: "success", RequestID: rid}
	var data interface{}
	if err := json.Unmarshal(raw, &data); err == nil {
		envelope.Data = data
	} else {
		envelope.Data = json.RawMessage(raw)
	}
	c.JSON(http.StatusOK, envelope)
}
