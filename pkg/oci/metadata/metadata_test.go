// SPDX-License-Identifier: Apache-2.0
// Copyright Authors of Cilium

package metadata

import (
	"context"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
)

func TestGetInstance(t *testing.T) {
	var calls int
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		calls++
		require.Equal(t, authHeader, r.Header.Get("Authorization"))
		switch r.URL.Path {
		case "/opc/v2/instance/":
			_, _ = w.Write([]byte(`{
				"id":" ocid1.instance.oc1.test ",
				"shape":" VM.Standard.A1.Flex ",
				"availabilityDomain":" AD-1 ",
				"compartmentId":" ocid1.compartment.oc1.test "
			}`))
		case "/opc/v2/vnics/":
			_, _ = w.Write([]byte(`[
				{"vnicId":"secondary","subnetOcid":"secondary-subnet","nicIndex":1},
				{"vnicId":" primary-vnic ","subnetOcid":" primary-subnet ","nicIndex":0}
			]`))
		default:
			http.NotFound(w, r)
		}
	}))
	defer server.Close()

	client := newClient(server.URL+"/opc/v2", server.Client())
	instance, err := client.GetInstance(context.Background())

	require.NoError(t, err)
	require.Equal(t, 2, calls)
	require.Equal(t, Instance{
		ID:                 "ocid1.instance.oc1.test",
		Shape:              "VM.Standard.A1.Flex",
		AvailabilityDomain: "AD-1",
		CompartmentID:      "ocid1.compartment.oc1.test",
		PrimaryVNICID:      "primary-vnic",
		PrimarySubnetID:    "primary-subnet",
	}, instance)
}

func TestGetInstanceErrors(t *testing.T) {
	tests := []struct {
		name    string
		handler http.HandlerFunc
	}{
		{
			name: "non-success status",
			handler: func(w http.ResponseWriter, _ *http.Request) {
				http.Error(w, "no metadata", http.StatusForbidden)
			},
		},
		{
			name: "invalid JSON",
			handler: func(w http.ResponseWriter, _ *http.Request) {
				_, _ = w.Write([]byte("{"))
			},
		},
		{
			name: "missing required fields",
			handler: func(w http.ResponseWriter, r *http.Request) {
				if r.URL.Path == "/opc/v2/instance/" {
					_, _ = w.Write([]byte(`{"id":"id"}`))
					return
				}
				_, _ = w.Write([]byte(`[{"vnicId":"vnic","subnetOcid":"subnet","nicIndex":0}]`))
			},
		},
		{
			name: "primary VNIC missing",
			handler: func(w http.ResponseWriter, r *http.Request) {
				if r.URL.Path == "/opc/v2/instance/" {
					_, _ = w.Write([]byte(`{"id":"id","shape":"shape","compartmentId":"compartment"}`))
					return
				}
				_, _ = w.Write([]byte(`[{"vnicId":"vnic","subnetOcid":"subnet","nicIndex":1}]`))
			},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			server := httptest.NewServer(tt.handler)
			defer server.Close()
			client := newClient(server.URL+"/opc/v2", server.Client())

			_, err := client.GetInstance(context.Background())
			require.Error(t, err)
		})
	}
}

func TestGetRetriesTransientStatus(t *testing.T) {
	var attempts int
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/opc/v2/instance/" {
			attempts++
			if attempts < 3 {
				http.Error(w, "retry", http.StatusTooManyRequests)
				return
			}
			_, _ = w.Write([]byte(`{"id":"id","shape":"shape","compartmentId":"compartment"}`))
			return
		}
		_, _ = w.Write([]byte(`[{"vnicId":"vnic","subnetOcid":"subnet","nicIndex":0}]`))
	}))
	defer server.Close()

	client := newClient(server.URL+"/opc/v2", server.Client())
	instance, err := client.GetInstance(context.Background())

	require.NoError(t, err)
	require.Equal(t, 3, attempts)
	require.Equal(t, "id", instance.ID)
}

func TestGetHonorsContext(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		time.Sleep(100 * time.Millisecond)
		_, _ = w.Write([]byte(`{}`))
	}))
	defer server.Close()

	ctx, cancel := context.WithTimeout(context.Background(), time.Millisecond)
	defer cancel()

	client := newClient(server.URL, server.Client())
	_, err := client.GetInstance(ctx)
	require.ErrorIs(t, err, context.DeadlineExceeded)
}
