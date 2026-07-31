// SPDX-License-Identifier: Apache-2.0
// Copyright Authors of Cilium

package metadata

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"strings"
	"time"

	"github.com/cilium/cilium/pkg/safeio"
)

const (
	defaultBaseURL   = "http://169.254.169.254/opc/v2/"
	authHeader       = "Bearer Oracle"
	maxAttempts      = 3
	initialRetryWait = 200 * time.Millisecond
)

// Client retrieves instance information from version 2 of the OCI instance
// metadata service.
type Client struct {
	baseURL    string
	httpClient *http.Client
}

// Instance contains the metadata needed to initialize OCI IPAM.
type Instance struct {
	ID                 string
	Shape              string
	AvailabilityDomain string
	CompartmentID      string
}

type instanceResponse struct {
	ID                 string `json:"id"`
	Shape              string `json:"shape"`
	AvailabilityDomain string `json:"availabilityDomain"`
	CompartmentID      string `json:"compartmentId"`
}

// NewClient creates an OCI metadata client with bounded request time.
func NewClient() *Client {
	return newClient(defaultBaseURL, &http.Client{Timeout: 5 * time.Second})
}

func newClient(baseURL string, httpClient *http.Client) *Client {
	return &Client{
		baseURL:    strings.TrimRight(baseURL, "/") + "/",
		httpClient: httpClient,
	}
}

func (c *Client) get(ctx context.Context, path string, dst any) error {
	var lastErr error
	retryWait := initialRetryWait
	for attempt := 1; attempt <= maxAttempts; attempt++ {
		retry, err := c.getOnce(ctx, path, dst)
		if err == nil {
			return nil
		}
		lastErr = err
		if !retry || attempt == maxAttempts {
			return err
		}

		timer := time.NewTimer(retryWait)
		select {
		case <-ctx.Done():
			timer.Stop()
			return ctx.Err()
		case <-timer.C:
		}
		retryWait *= 2
	}
	return lastErr
}

func (c *Client) getOnce(ctx context.Context, path string, dst any) (retry bool, err error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, c.baseURL+path, nil)
	if err != nil {
		return false, fmt.Errorf("create OCI metadata request for %q: %w", path, err)
	}
	req.Header.Set("Authorization", authHeader)

	resp, err := c.httpClient.Do(req)
	if err != nil {
		return ctx.Err() == nil, fmt.Errorf("request OCI metadata %q: %w", path, err)
	}

	if resp.StatusCode != http.StatusOK {
		_, _ = io.Copy(io.Discard, io.LimitReader(resp.Body, 4<<10))
		_ = resp.Body.Close()
		retry := resp.StatusCode == http.StatusNotFound ||
			resp.StatusCode == http.StatusTooManyRequests ||
			resp.StatusCode >= http.StatusInternalServerError
		return retry, fmt.Errorf("OCI metadata %q returned HTTP status %s", path, resp.Status)
	}

	value, err := safeio.ReadAllLimit(resp.Body, safeio.MB)
	closeErr := resp.Body.Close()
	if err != nil {
		return false, fmt.Errorf("read OCI metadata %q: %w", path, err)
	}
	if closeErr != nil {
		return false, fmt.Errorf("close OCI metadata %q response: %w", path, closeErr)
	}
	if err := json.Unmarshal(value, dst); err != nil {
		return false, fmt.Errorf("decode OCI metadata %q: %w", path, err)
	}
	return false, nil
}

// GetInstance returns the local compute instance metadata needed to initialize
// OCI IPAM. VNIC metadata is deliberately not used here: OCI IMDS does not
// expose subnet OCIDs, and nicIndex identifies a physical NIC rather than the
// instance's primary VNIC.
func (c *Client) GetInstance(ctx context.Context) (Instance, error) {
	var instance instanceResponse
	if err := c.get(ctx, "instance/", &instance); err != nil {
		return Instance{}, err
	}

	result := Instance{
		ID:                 strings.TrimSpace(instance.ID),
		Shape:              strings.TrimSpace(instance.Shape),
		AvailabilityDomain: strings.TrimSpace(instance.AvailabilityDomain),
		CompartmentID:      strings.TrimSpace(instance.CompartmentID),
	}
	if result.ID == "" || result.Shape == "" || result.CompartmentID == "" {
		return Instance{}, errors.New("OCI metadata response is missing required instance fields")
	}

	return result, nil
}

// GetInstanceMetadata returns the local instance fields needed by CiliumNode.
func GetInstanceMetadata(ctx context.Context) (instanceID, shape, availabilityDomain, compartmentID string, err error) {
	instance, err := NewClient().GetInstance(ctx)
	if err != nil {
		return "", "", "", "", err
	}
	return instance.ID, instance.Shape, instance.AvailabilityDomain, instance.CompartmentID, nil
}
