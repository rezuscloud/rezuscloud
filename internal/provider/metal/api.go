package metal

import (
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"time"
)

// ManagementAPI communicates with the RezusCloud REST API for provider
// registration and machine reporting.
type ManagementAPI struct {
	baseURL  string
	apiToken string
	client   *http.Client
}

// NewManagementAPI creates a new management API client.
func NewManagementAPI(baseURL, apiToken string) *ManagementAPI {
	return &ManagementAPI{
		baseURL:  baseURL,
		apiToken: apiToken,
		client: &http.Client{
			Timeout: 30 * time.Second,
		},
	}
}

// RegisterProvider creates or updates the provider resource in the management plane.
func (a *ManagementAPI) RegisterProvider(providerType string, machineTypes, regions []string) error {
	payload := map[string]interface{}{
		"metadata": map[string]interface{}{
			"name":   providerType,
			"labels": map[string]string{},
		},
		"spec": map[string]interface{}{
			"endpoint": "outbound-gRPC",
		},
	}

	body, err := json.Marshal(payload)
	if err != nil {
		return fmt.Errorf("marshal payload: %w", err)
	}

	url := a.baseURL + "/api/v1/providers/" + providerType
	req, err := http.NewRequest("PUT", url, bytes.NewReader(body))
	if err != nil {
		return fmt.Errorf("create request: %w", err)
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Authorization", "Bearer "+a.apiToken)

	resp, err := a.client.Do(req)
	if err != nil {
		return fmt.Errorf("register provider: %w", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK && resp.StatusCode != http.StatusCreated {
		respBody, _ := io.ReadAll(resp.Body)
		return fmt.Errorf("register provider: HTTP %d: %s", resp.StatusCode, string(respBody))
	}

	return a.UpdateProviderStatus(providerType, true, "")
}

// UpdateProviderStatus sends a heartbeat update.
func (a *ManagementAPI) UpdateProviderStatus(providerType string, connected bool, errMsg string) error {
	payload := map[string]interface{}{
		"status": map[string]interface{}{
			"connected":     connected,
			"lastHeartbeat": time.Now().UTC().Format(time.RFC3339),
		},
	}
	statusMap, _ := payload["status"].(map[string]interface{})
	if errMsg != "" && statusMap != nil {
		statusMap["error"] = errMsg
	}

	body, err := json.Marshal(payload)
	if err != nil {
		return fmt.Errorf("marshal: %w", err)
	}

	url := a.baseURL + "/api/v1/providers/" + providerType + "/status"
	req, err := http.NewRequest("PUT", url, bytes.NewReader(body))
	if err != nil {
		return fmt.Errorf("create request: %w", err)
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Authorization", "Bearer "+a.apiToken)

	resp, err := a.client.Do(req)
	if err != nil {
		return fmt.Errorf("update status: %w", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusNoContent && resp.StatusCode != http.StatusOK {
		respBody, _ := io.ReadAll(resp.Body)
		return fmt.Errorf("update status: HTTP %d: %s", resp.StatusCode, string(respBody))
	}

	return nil
}

// RegisterDiscoveredMachine creates a machine resource in the management plane
// for a newly discovered Talos node.
func (a *ManagementAPI) RegisterDiscoveredMachine(ip string, providerType string) error {
	payload := map[string]interface{}{
		"metadata": map[string]interface{}{
			"name": fmt.Sprintf("discovered-%s", ip),
			"labels": map[string]string{
				"rezuscloud.io/provider":    providerType,
				"rezuscloud.io/discovered":  "true",
				"rezuscloud.io/maintenance": "true",
			},
		},
		"spec": map[string]interface{}{
			"managementAddress": ip + ":50000",
			"connected":         false,
		},
		"status": map[string]interface{}{
			"stage":       "initializing",
			"maintenance": true,
			"network": map[string]interface{}{
				"addresses": []string{ip},
			},
		},
	}

	body, err := json.Marshal(payload)
	if err != nil {
		return fmt.Errorf("marshal machine payload: %w", err)
	}

	url := a.baseURL + "/api/v1/machines/discovered-" + replaceDots(ip)
	req, err := http.NewRequest("PUT", url, bytes.NewReader(body))
	if err != nil {
		return fmt.Errorf("create request: %w", err)
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Authorization", "Bearer "+a.apiToken)

	resp, err := a.client.Do(req)
	if err != nil {
		return fmt.Errorf("register machine: %w", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK && resp.StatusCode != http.StatusCreated {
		respBody, _ := io.ReadAll(resp.Body)
		return fmt.Errorf("register machine: HTTP %d: %s", resp.StatusCode, string(respBody))
	}

	return nil
}

// MarkMachineLost updates a machine's status to offline when it disappears from scans.
func (a *ManagementAPI) MarkMachineLost(ip string) error {
	payload := map[string]interface{}{
		"status": map[string]interface{}{
			"stage":       "off",
			"maintenance": true,
			"lastError":   "node no longer responding to discovery probes",
		},
	}

	body, err := json.Marshal(payload)
	if err != nil {
		return fmt.Errorf("marshal: %w", err)
	}

	url := a.baseURL + "/api/v1/machines/discovered-" + replaceDots(ip) + "/status"
	req, err := http.NewRequest("PUT", url, bytes.NewReader(body))
	if err != nil {
		return fmt.Errorf("create request: %w", err)
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Authorization", "Bearer "+a.apiToken)

	resp, err := a.client.Do(req)
	if err != nil {
		return fmt.Errorf("mark lost: %w", err)
	}
	defer resp.Body.Close()

	// Ignore 404 — machine may have been deleted.
	if resp.StatusCode != http.StatusNoContent && resp.StatusCode != http.StatusOK && resp.StatusCode != http.StatusNotFound {
		respBody, _ := io.ReadAll(resp.Body)
		return fmt.Errorf("mark lost: HTTP %d: %s", resp.StatusCode, string(respBody))
	}

	return nil
}

func replaceDots(ip string) string {
	s := make([]byte, 0, len(ip))
	for i := 0; i < len(ip); i++ {
		if ip[i] == '.' {
			s = append(s, '-')
		} else {
			s = append(s, ip[i])
		}
	}
	return string(s)
}
