package core

import "net/http"

type TunnelNode struct {
	ID     string `json:"id"`
	Name   string `json:"name"`
	Online bool   `json:"online"`
}

func AdminTunnelNodesHandler(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		http.Error(w, "Method not allowed", http.StatusMethodNotAllowed)
		return
	}

	writeJSON(w, http.StatusOK, map[string]interface{}{
		"nodes": currentTunnelNodes(),
	})
}

func currentTunnelNodes() []TunnelNode {
	return []TunnelNode{
		{ID: "cloudserver", Name: "Cloud Server", Online: true},
	}
}
