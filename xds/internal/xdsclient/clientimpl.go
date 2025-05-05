/*
 *
 * Copyright 2022 gRPC authors.
 *
 * Licensed under the Apache License, Version 2.0 (the "License");
 * you may not use this file except in compliance with the License.
 * You may obtain a copy of the License at
 *
 *     http://www.apache.org/licenses/LICENSE-2.0
 *
 * Unless required by applicable law or agreed to in writing, software
 * distributed under the License is distributed on an "AS IS" BASIS,
 * WITHOUT WARRANTIES OR CONDITIONS OF ANY KIND, either express or implied.
 * See the License for the specific language governing permissions and
 * limitations under the License.
 *
 */

package xdsclient

import (
	"errors"
	"sync/atomic"
	"time"

	estats "google.golang.org/grpc/experimental/stats"
	"google.golang.org/grpc/internal/backoff"
	"google.golang.org/grpc/internal/grpclog"
	"google.golang.org/grpc/internal/xds/bootstrap"
	"google.golang.org/grpc/xds/internal/clients/lrsclient"
	"google.golang.org/grpc/xds/internal/clients/xdsclient"
)

const (
	// NameForServer represents the value to be passed as name when creating an xDS
	// client from xDS-enabled gRPC servers. This is a well-known dedicated key
	// value, and is defined in gRFC A71.
	NameForServer = "#server"

	defaultWatchExpiryTimeout = 15 * time.Second
)

var (

	// ErrClientClosed is returned when the xDS client is closed.
	ErrClientClosed = errors.New("xds: the xDS client is closed")

	// The following functions are no-ops in the actual code, but can be
	// overridden in tests to give them visibility into certain events.
	xdsClientImplCreateHook = func(string) {}
	xdsClientImplCloseHook  = func(string) {}

	defaultExponentialBackoff = backoff.DefaultExponential.Backoff

	xdsClientResourceUpdatesValidMetric = estats.RegisterInt64Count(estats.MetricDescriptor{
		Name:        "grpc.xds_client.resource_updates_valid",
		Description: "A counter of resources received that were considered valid. The counter will be incremented even for resources that have not changed.",
		Unit:        "resource",
		Labels:      []string{"grpc.target", "grpc.xds.server", "grpc.xds.resource_type"},
		Default:     false,
	})
	xdsClientResourceUpdatesInvalidMetric = estats.RegisterInt64Count(estats.MetricDescriptor{
		Name:        "grpc.xds_client.resource_updates_invalid",
		Description: "A counter of resources received that were considered invalid.",
		Unit:        "resource",
		Labels:      []string{"grpc.target", "grpc.xds.server", "grpc.xds.resource_type"},
		Default:     false,
	})
	xdsClientServerFailureMetric = estats.RegisterInt64Count(estats.MetricDescriptor{
		Name:        "grpc.xds_client.server_failure",
		Description: "A counter of xDS servers going from healthy to unhealthy. A server goes unhealthy when we have a connectivity failure or when the ADS stream fails without seeing a response message, as per gRFC A57.",
		Unit:        "failure",
		Labels:      []string{"grpc.target", "grpc.xds.server"},
		Default:     false,
	})
)

// clientImpl is the real implementation of the xDS client. The exported Client
// is a wrapper of this struct with a ref count.
type clientImpl struct {
	*xdsclient.XDSClient

	config    *bootstrap.Config
	lrsClient *lrsclient.LRSClient
	logger    *grpclog.PrefixLogger // Logger for this client.
}

func init() {
	DefaultPool = &Pool{clients: make(map[string]*clientRefCounted)}
}

// BootstrapConfig returns the configuration read from the bootstrap file.
// Callers must treat the return value as read-only.
func (c *clientImpl) BootstrapConfig() *bootstrap.Config {
	return c.config
}

// SetWatchExpiryTimeoutForTesting override the default watch expiry timeout
// with provided timeout value for testing.
func (c *clientImpl) SetWatchExpiryTimeoutForTesting(watchExpiryTimeout time.Duration) {
	c.XDSClient.SetWatchExpiryTimeoutForTesting(watchExpiryTimeout)
}

// SetBackoffForTesting override the default backoff with provided function
// for testing.
func (c *clientImpl) SetBackoffForTesting(backoff func(int) time.Duration) {
	c.XDSClient.SetBackoffForTesting(backoff)
}

// clientRefCounted is ref-counted, and to be shared by the xds resolver and
// balancer implementations, across multiple ClientConns and Servers.
type clientRefCounted struct {
	*clientImpl

	refCount int32 // accessed atomically
}

func (c *clientRefCounted) incrRef() int32 {
	return atomic.AddInt32(&c.refCount, 1)
}

func (c *clientRefCounted) decrRef() int32 {
	return atomic.AddInt32(&c.refCount, -1)
}
