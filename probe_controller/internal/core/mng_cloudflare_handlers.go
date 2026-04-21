package core

import (
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"strings"
)

func mngCloudflarePageHandler(w http.ResponseWriter, r *http.Request) {
	if r.URL.Path != "/mng/cloudflare" {
		http.NotFound(w, r)
		return
	}
	if r.Method != http.MethodGet {
		http.Error(w, "Method not allowed", http.StatusMethodNotAllowed)
		return
	}
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	_, _ = w.Write([]byte(mngCloudflarePageHTML))
}

func mngCloudflareAPIHandler(w http.ResponseWriter, r *http.Request) {
	switch r.Method {
	case http.MethodGet:
		writeJSON(w, http.StatusOK, getCloudflareAPIKey())
		return
	case http.MethodPost:
		var req cloudflareAPIKeyRequest
		if err := decodeMngJSONBody(r, &req); err != nil {
			writeJSON(w, http.StatusBadRequest, map[string]string{"error": "invalid json body"})
			return
		}
		result, err := setCloudflareAPIKey(req)
		if err != nil {
			writeMngCloudflareError(w, err)
			return
		}
		writeJSON(w, http.StatusOK, result)
		return
	default:
		http.Error(w, "Method not allowed", http.StatusMethodNotAllowed)
		return
	}
}

func mngCloudflareZoneHandler(w http.ResponseWriter, r *http.Request) {
	switch r.Method {
	case http.MethodGet:
		writeJSON(w, http.StatusOK, getCloudflareZone())
		return
	case http.MethodPost:
		var req cloudflareZoneRequest
		if err := decodeMngJSONBody(r, &req); err != nil {
			writeJSON(w, http.StatusBadRequest, map[string]string{"error": "invalid json body"})
			return
		}
		result, err := setCloudflareZone(req)
		if err != nil {
			writeMngCloudflareError(w, err)
			return
		}
		writeJSON(w, http.StatusOK, result)
		return
	default:
		http.Error(w, "Method not allowed", http.StatusMethodNotAllowed)
		return
	}
}

func mngCloudflareDDNSRecordsHandler(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		http.Error(w, "Method not allowed", http.StatusMethodNotAllowed)
		return
	}
	writeJSON(w, http.StatusOK, map[string]interface{}{
		"records": listCloudflareRecords(),
	})
}

func mngCloudflareDDNSApplyHandler(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		http.Error(w, "Method not allowed", http.StatusMethodNotAllowed)
		return
	}
	var req cloudflareDDNSApplyRequest
	if err := decodeMngJSONBody(r, &req); err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "invalid json body"})
		return
	}
	result, err := applyCloudflareDDNS(req)
	if err != nil {
		writeMngCloudflareError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, result)
}

func mngCloudflareZeroTrustWhitelistHandler(w http.ResponseWriter, r *http.Request) {
	switch r.Method {
	case http.MethodGet:
		writeJSON(w, http.StatusOK, getCloudflareZeroTrustWhitelist())
		return
	case http.MethodPost:
		var req cloudflareZeroTrustWhitelistRequest
		if err := decodeMngJSONBody(r, &req); err != nil {
			writeJSON(w, http.StatusBadRequest, map[string]string{"error": "invalid json body"})
			return
		}
		result, err := setCloudflareZeroTrustWhitelist(req)
		if err != nil {
			writeMngCloudflareError(w, err)
			return
		}
		writeJSON(w, http.StatusOK, result)
		return
	default:
		http.Error(w, "Method not allowed", http.StatusMethodNotAllowed)
		return
	}
}

func mngCloudflareZeroTrustWhitelistRunHandler(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		http.Error(w, "Method not allowed", http.StatusMethodNotAllowed)
		return
	}
	var req cloudflareZeroTrustRunRequest
	if err := decodeMngJSONBodyAllowEmpty(r, &req); err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "invalid json body"})
		return
	}
	result, err := runCloudflareZeroTrustWhitelistSync(req.Force)
	if err != nil {
		writeMngCloudflareError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, result)
}

func decodeMngJSONBody(r *http.Request, out interface{}) error {
	if r == nil || r.Body == nil {
		return errors.New("empty body")
	}
	dec := json.NewDecoder(r.Body)
	if err := dec.Decode(out); err != nil {
		return err
	}
	return nil
}

func decodeMngJSONBodyAllowEmpty(r *http.Request, out interface{}) error {
	if r == nil || r.Body == nil {
		return nil
	}
	dec := json.NewDecoder(r.Body)
	if err := dec.Decode(out); err != nil {
		if errors.Is(err, io.EOF) {
			return nil
		}
		return err
	}
	return nil
}

func writeMngCloudflareError(w http.ResponseWriter, err error) {
	if err == nil {
		writeJSON(w, http.StatusInternalServerError, map[string]string{"error": "unknown error"})
		return
	}
	msg := strings.TrimSpace(err.Error())
	if msg == "" {
		msg = "unknown error"
	}
	lower := strings.ToLower(msg)
	status := http.StatusBadRequest
	if strings.Contains(lower, "not initialized") {
		status = http.StatusInternalServerError
	} else if strings.Contains(lower, "request failed") || strings.Contains(lower, "status=") || strings.Contains(lower, "not found") || strings.Contains(lower, "timeout") {
		status = http.StatusBadGateway
	}
	writeJSON(w, status, map[string]string{"error": msg})
}
