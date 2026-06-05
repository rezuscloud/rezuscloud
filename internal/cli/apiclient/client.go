// Package apiclient provides an HTTP client for the RezusCloud REST API.
package apiclient

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strings"
)

// Resource represents a generic API resource with K8s-style structure.
type Resource struct {
	APIVersion string      `json:"apiVersion,omitempty"`
	Kind       string      `json:"kind,omitempty"`
	Metadata   *ObjectMeta `json:"metadata,omitempty"`
	Spec       any         `json:"spec,omitempty"`
	Status     any         `json:"status,omitempty"`
}

// ObjectMeta holds the standard metadata for a resource.
type ObjectMeta struct {
	Name              string            `json:"name"`
	UID               string            `json:"uid,omitempty"`
	ResourceVersion   int64             `json:"resourceVersion,omitempty"`
	CreatedAt         string            `json:"createdAt,omitempty"`
	UpdatedAt         string            `json:"updatedAt,omitempty"`
	DeletionTimestamp *string           `json:"deletionTimestamp,omitempty"`
	Finalizers        []string          `json:"finalizers,omitempty"`
	Labels            map[string]string `json:"labels,omitempty"`
	Annotations       map[string]string `json:"annotations,omitempty"`
}

// ListResponse is the response from a list endpoint.
type ListResponse struct {
	Items []Resource `json:"items"`
	Total int        `json:"total"`
}

// ErrorResponse is the structured error format from the API.
type ErrorResponse struct {
	Status  string `json:"status"`
	Message string `json:"message"`
	Reason  string `json:"reason"`
	Code    int    `json:"code"`
}

func (e *ErrorResponse) Error() string {
	return fmt.Sprintf("%s: %s", e.Reason, e.Message)
}

// Client is an HTTP client for the RezusCloud REST API.
type Client struct {
	baseURL    string
	token      string
	httpClient *http.Client
}

// New creates a new API client.
func New(baseURL, token string) *Client {
	return &Client{
		baseURL:    strings.TrimRight(baseURL, "/"),
		token:      token,
		httpClient: &http.Client{},
	}
}

// Get retrieves a single resource by name.
func (c *Client) Get(ctx context.Context, path, name string) (*Resource, error) {
	u := c.url(path, name)
	body, err := c.doRequest(ctx, http.MethodGet, u, nil)
	if err != nil {
		return nil, err
	}

	var resource Resource
	if err := json.Unmarshal(body, &resource); err != nil {
		return nil, fmt.Errorf("decode response: %w", err)
	}

	return &resource, nil
}

// List retrieves a list of resources.
func (c *Client) List(ctx context.Context, path string, opts ListOptions) (*ListResponse, error) {
	u, err := url.Parse(c.url(path))
	if err != nil {
		return nil, fmt.Errorf("parse url: %w", err)
	}

	q := u.Query()
	if opts.Offset > 0 {
		q.Set("offset", fmt.Sprintf("%d", opts.Offset))
	}
	if opts.Limit > 0 {
		q.Set("limit", fmt.Sprintf("%d", opts.Limit))
	}
	if opts.LabelSelector != "" {
		q.Set("labelSelector", opts.LabelSelector)
	}
	u.RawQuery = q.Encode()

	body, err := c.doRequest(ctx, http.MethodGet, u.String(), nil)
	if err != nil {
		return nil, err
	}

	var list ListResponse
	if err := json.Unmarshal(body, &list); err != nil {
		return nil, fmt.Errorf("decode response: %w", err)
	}

	return &list, nil
}

// Create creates a new resource.
func (c *Client) Create(ctx context.Context, path string, resource *Resource) (*Resource, error) {
	body, err := json.Marshal(resource)
	if err != nil {
		return nil, fmt.Errorf("encode resource: %w", err)
	}

	respBody, err := c.doRequest(ctx, http.MethodPost, c.url(path), bytes.NewReader(body))
	if err != nil {
		return nil, err
	}

	var created Resource
	if err := json.Unmarshal(respBody, &created); err != nil {
		return nil, fmt.Errorf("decode response: %w", err)
	}

	return &created, nil
}

// Update updates an existing resource.
func (c *Client) Update(ctx context.Context, path, name string, resource *Resource) (*Resource, error) {
	body, err := json.Marshal(resource)
	if err != nil {
		return nil, fmt.Errorf("encode resource: %w", err)
	}

	respBody, err := c.doRequest(ctx, http.MethodPut, c.url(path, name), bytes.NewReader(body))
	if err != nil {
		return nil, err
	}

	var updated Resource
	if err := json.Unmarshal(respBody, &updated); err != nil {
		return nil, fmt.Errorf("decode response: %w", err)
	}

	return &updated, nil
}

// Delete deletes a resource by name.
func (c *Client) Delete(ctx context.Context, path, name string) (*Resource, error) {
	respBody, err := c.doRequest(ctx, http.MethodDelete, c.url(path, name), nil)
	if err != nil {
		return nil, err
	}

	// 204 No Content — successful delete with no body.
	if len(respBody) == 0 {
		return nil, nil
	}

	var deleted Resource
	if err := json.Unmarshal(respBody, &deleted); err != nil {
		return nil, fmt.Errorf("decode response: %w", err)
	}

	return &deleted, nil
}

// RawGet performs a GET request and returns the raw body.
func (c *Client) RawGet(ctx context.Context, pathAndName ...string) ([]byte, error) {
	return c.doRequest(ctx, http.MethodGet, c.url(pathAndName...), nil)
}

// RawPost performs a POST request and returns the raw body.
func (c *Client) RawPost(ctx context.Context, path string, body any) ([]byte, error) {
	var bodyReader io.Reader
	if body != nil {
		b, err := json.Marshal(body)
		if err != nil {
			return nil, fmt.Errorf("encode body: %w", err)
		}
		bodyReader = bytes.NewReader(b)
	}
	return c.doRequest(ctx, http.MethodPost, c.url(path), bodyReader)
}

// StreamGet performs a GET request and returns the raw response body as a stream.
// The caller is responsible for closing the returned ReadCloser.
// This is used for SSE endpoints where the response is streamed.
func (c *Client) StreamGet(ctx context.Context, pathAndName ...string) (io.ReadCloser, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, c.url(pathAndName...), nil)
	if err != nil {
		return nil, fmt.Errorf("create request: %w", err)
	}

	req.Header.Set("Accept", "text/event-stream")
	if c.token != "" {
		req.Header.Set("Authorization", "Bearer "+c.token)
	}

	resp, err := c.httpClient.Do(req)
	if err != nil {
		return nil, fmt.Errorf("request failed: %w", err)
	}

	if resp.StatusCode >= 400 {
		defer func() { _ = resp.Body.Close() }()
		respBody, _ := io.ReadAll(resp.Body)
		var errResp ErrorResponse
		if jsonErr := json.Unmarshal(respBody, &errResp); jsonErr == nil && errResp.Message != "" {
			return nil, &errResp
		}
		return nil, fmt.Errorf("stream request failed: %s", string(respBody))
	}

	return resp.Body, nil
}

// url builds the full URL from path segments.
func (c *Client) url(segments ...string) string {
	return c.baseURL + "/" + strings.Join(segments, "/")
}

// doRequest executes an HTTP request with auth headers.
func (c *Client) doRequest(ctx context.Context, method, u string, body io.Reader) ([]byte, error) {
	req, err := http.NewRequestWithContext(ctx, method, u, body)
	if err != nil {
		return nil, fmt.Errorf("create request: %w", err)
	}

	if body != nil {
		req.Header.Set("Content-Type", "application/json")
	}
	req.Header.Set("Accept", "application/json")

	if c.token != "" {
		req.Header.Set("Authorization", "Bearer "+c.token)
	}

	resp, err := c.httpClient.Do(req)
	if err != nil {
		return nil, fmt.Errorf("request failed: %w", err)
	}
	defer func() { _ = resp.Body.Close() }()

	respBody, err := io.ReadAll(resp.Body)
	if err != nil {
		return nil, fmt.Errorf("read response: %w", err)
	}

	if resp.StatusCode >= 400 {
		var errResp ErrorResponse
		if jsonErr := json.Unmarshal(respBody, &errResp); jsonErr == nil && errResp.Message != "" {
			return nil, &errResp
		}

		return nil, fmt.Errorf("%s %s: %s", method, u, string(respBody))
	}

	return respBody, nil
}

// ListOptions are options for list requests.
type ListOptions struct {
	Offset        int
	Limit         int
	LabelSelector string
}

// ParseResource parses a JSON or YAML resource from bytes.
func ParseResource(data []byte) (*Resource, error) {
	var resource Resource
	if err := json.Unmarshal(data, &resource); err != nil {
		return nil, fmt.Errorf("parse resource: %w", err)
	}

	return &resource, nil
}
