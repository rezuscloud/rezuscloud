package openstack

import (
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"time"
)

// ManagementAPI communicates with the RezusCloud REST API for provider registration.
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
	// Build provider spec for creation.
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

	// Now update status to connected with schema.
	statusPayload := map[string]interface{}{
		"status": map[string]interface{}{
			"connected":     true,
			"lastHeartbeat": time.Now().UTC().Format(time.RFC3339),
			"schema": map[string]interface{}{
				"machineTypes": machineTypes,
				"regions":      regions,
			},
		},
	}

	statusBody, err := json.Marshal(statusPayload)
	if err != nil {
		return fmt.Errorf("marshal status: %w", err)
	}

	statusURL := a.baseURL + "/api/v1/providers/" + providerType + "/status"
	statusReq, err := http.NewRequest("PUT", statusURL, bytes.NewReader(statusBody))
	if err != nil {
		return fmt.Errorf("create status request: %w", err)
	}
	statusReq.Header.Set("Content-Type", "application/json")
	statusReq.Header.Set("Authorization", "Bearer "+a.apiToken)

	statusResp, err := a.client.Do(statusReq)
	if err != nil {
		return fmt.Errorf("update status: %w", err)
	}
	defer statusResp.Body.Close()

	if statusResp.StatusCode != http.StatusNoContent && statusResp.StatusCode != http.StatusOK {
		respBody, _ := io.ReadAll(statusResp.Body)
		return fmt.Errorf("update status: HTTP %d: %s", statusResp.StatusCode, string(respBody))
	}

	return nil
}

// UpdateProviderStatus sends a heartbeat update.
func (a *ManagementAPI) UpdateProviderStatus(providerType string, connected bool, errMsg string) error {
	payload := map[string]interface{}{
		"status": map[string]interface{}{
			"connected":     connected,
			"lastHeartbeat": time.Now().UTC().Format(time.RFC3339),
		},
	}
	if errMsg != "" {
		statusMap, _ := payload["status"].(map[string]interface{})
		if statusMap != nil {
			statusMap["error"] = errMsg
		}
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
