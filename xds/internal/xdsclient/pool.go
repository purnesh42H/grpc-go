/*
 *
 * Copyright 2024 gRPC authors.
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
	"fmt"
	"sync"
	"time"

	v3statuspb "github.com/envoyproxy/go-control-plane/envoy/service/status/v3"
	"google.golang.org/grpc/credentials"
	estats "google.golang.org/grpc/experimental/stats"
	istats "google.golang.org/grpc/internal/stats"
	"google.golang.org/grpc/internal/xds/bootstrap"
	xdsbootstrap "google.golang.org/grpc/xds/bootstrap"
	"google.golang.org/grpc/xds/internal/clients"
	"google.golang.org/grpc/xds/internal/clients/grpctransport"
	"google.golang.org/grpc/xds/internal/clients/xdsclient"
	gxdsclient "google.golang.org/grpc/xds/internal/clients/xdsclient"
	"google.golang.org/grpc/xds/internal/xdsclient/xdsresource"
	"google.golang.org/protobuf/proto"
)

var (
	// DefaultPool is the default pool for xDS clients. It is created at init
	// time by reading bootstrap configuration from env vars.
	DefaultPool *Pool
)

// Pool represents a pool of xDS clients that share the same bootstrap
// configuration.
type Pool struct {
	// Note that mu should ideally only have to guard clients. But here, we need
	// it to guard config as well since SetFallbackBootstrapConfig writes to
	// config.
	mu      sync.Mutex
	clients map[string]*clientRefCounted
	config  *bootstrap.Config
}

// OptionsForTesting contains options to configure xDS client creation for
// testing purposes only.
type OptionsForTesting struct {
	// Name is a unique name for this xDS client.
	Name string

	// WatchExpiryTimeout is the timeout for xDS resource watch expiry. If
	// unspecified, uses the default value used in non-test code.
	WatchExpiryTimeout time.Duration

	// StreamBackoffAfterFailure is the backoff function used to determine the
	// backoff duration after stream failures.
	// If unspecified, uses the default value used in non-test code.
	StreamBackoffAfterFailure func(int) time.Duration

	// MetricsRecorder is the metrics recorder the xDS Client will use. If
	// unspecified, uses a no-op MetricsRecorder.
	MetricsRecorder estats.MetricsRecorder
}

// NewPool creates a new xDS client pool with the given bootstrap config.
//
// If a nil bootstrap config is passed and SetFallbackBootstrapConfig is not
// called before a call to NewClient, the latter will fail. i.e. if there is an
// attempt to create an xDS client from the pool without specifying bootstrap
// configuration (either at pool creation time or by setting the fallback
// bootstrap configuration), xDS client creation will fail.
func NewPool(config *bootstrap.Config) *Pool {
	return &Pool{
		clients: make(map[string]*clientRefCounted),
		config:  config,
	}
}

// NewClient returns an xDS client with the given name from the pool. If the
// client doesn't already exist, it creates a new xDS client and adds it to the
// pool.
//
// The second return value represents a close function which the caller is
// expected to invoke once they are done using the client.  It is safe for the
// caller to invoke this close function multiple times.
func (p *Pool) NewClient(name string, metricsRecorder estats.MetricsRecorder) (XDSClient, func(), error) {
	return p.newRefCountedGeneric(name, metricsRecorder)
}

// NewClientForTesting returns an xDS client configured with the provided
// options from the pool. If the client doesn't already exist, it creates a new
// xDS client and adds it to the pool.
//
// The second return value represents a close function which the caller is
// expected to invoke once they are done using the client.  It is safe for the
// caller to invoke this close function multiple times.
//
// # Testing Only
//
// This function should ONLY be used for testing purposes.
func (p *Pool) NewClientForTesting(opts OptionsForTesting) (XDSClient, func(), error) {
	if opts.Name == "" {
		return nil, nil, fmt.Errorf("xds: opts.Name field must be non-empty")
	}
	if opts.WatchExpiryTimeout == 0 {
		opts.WatchExpiryTimeout = defaultWatchExpiryTimeout
	}
	if opts.StreamBackoffAfterFailure == nil {
		opts.StreamBackoffAfterFailure = defaultExponentialBackoff
	}
	if opts.MetricsRecorder == nil {
		opts.MetricsRecorder = istats.NewMetricsRecorderList(nil)
	}
	c, close, err := p.newRefCountedGeneric(opts.Name, opts.MetricsRecorder)
	if err != nil {
		return c, close, err
	}
	c.SetWatchExpiryTimeoutForTesting(opts.WatchExpiryTimeout)
	c.SetBackoffForTesting(opts.StreamBackoffAfterFailure)
	return c, close, err
}

// GetClientForTesting returns an xDS client created earlier using the given
// name from the pool. If the client with the given name doesn't already exist,
// it returns an error.
//
// The second return value represents a close function which the caller is
// expected to invoke once they are done using the client.  It is safe for the
// caller to invoke this close function multiple times.
//
// # Testing Only
//
// This function should ONLY be used for testing purposes.
func (p *Pool) GetClientForTesting(name string) (*clientRefCounted, func(), error) {
	p.mu.Lock()
	defer p.mu.Unlock()

	c, ok := p.clients[name]
	if !ok {
		return nil, nil, fmt.Errorf("xds:: xDS client with name %q not found", name)
	}
	c.incrRef()
	return c, sync.OnceFunc(func() { p.clientRefCountedClose(name) }), nil
}

// SetFallbackBootstrapConfig is used to specify a bootstrap configuration
// that will be used as a fallback when the bootstrap environment variables
// are not defined.
func (p *Pool) SetFallbackBootstrapConfig(config *bootstrap.Config) {
	p.mu.Lock()
	defer p.mu.Unlock()

	if p.config != nil {
		logger.Error("Attempt to set a bootstrap configuration even though one is already set via environment variables.")
		return
	}
	p.config = config
}

// DumpResources returns the status and contents of all xDS resources.
func (p *Pool) DumpResources() *v3statuspb.ClientStatusResponse {
	p.mu.Lock()
	defer p.mu.Unlock()

	resp := &v3statuspb.ClientStatusResponse{}
	for key, client := range p.clients {
		b, err := client.XDSClient.DumpResources()
		if err != nil {
			return nil
		}
		r := &v3statuspb.ClientStatusResponse{}
		if err := proto.Unmarshal(b, r); err != nil {
			return nil
		}
		cfg := r.Config[0]
		cfg.ClientScope = key
		resp.Config = append(resp.Config, cfg)
	}
	return resp
}

// BootstrapConfigForTesting returns the bootstrap configuration used by the
// pool. The caller should not mutate the returned config.
//
// To be used only for testing purposes.
func (p *Pool) BootstrapConfigForTesting() *bootstrap.Config {
	p.mu.Lock()
	defer p.mu.Unlock()
	return p.config
}

// UnsetBootstrapConfigForTesting unsets the bootstrap configuration used by
// the pool.
//
// To be used only for testing purposes.
func (p *Pool) UnsetBootstrapConfigForTesting() {
	p.mu.Lock()
	defer p.mu.Unlock()
	p.config = nil
}

func (p *Pool) clientRefCountedClose(name string) {
	p.mu.Lock()
	client, ok := p.clients[name]
	if !ok {
		logger.Errorf("Attempt to close a non-existent xDS client with name %s", name)
		p.mu.Unlock()
		return
	}
	if client.decrRef() != 0 {
		p.mu.Unlock()
		return
	}
	delete(p.clients, name)
	p.mu.Unlock()

	// This attempts to close the transport to the management server and could
	// theoretically call back into the xdsclient package again and deadlock.
	// Hence, this needs to be called without holding the lock.
	client.XDSClient.Close()
	xdsClientImplCloseHook(name)
}

func (p *Pool) newRefCountedGeneric(name string, metricsRecorder estats.MetricsRecorder) (XDSClient, func(), error) {
	p.mu.Lock()
	defer p.mu.Unlock()

	if p.config == nil {
		if len(p.clients) != 0 || p != DefaultPool {
			// If the current pool `p` already contains xDS clients or it is not
			// the `DefaultPool`, the bootstrap config should have been already
			// present in the pool.
			return nil, nil, fmt.Errorf("xds: bootstrap configuration not set in the pool")
		}
		// If the current pool `p` is the `DefaultPool` and has no clients, it
		// might be the first time an xDS client is being created on it. So,
		// the bootstrap configuration is read from environment variables.
		//
		// DefaultPool is initialized with bootstrap configuration from one of the
		// supported environment variables. If the environment variables are not
		// set, then fallback bootstrap configuration should be set before
		// attempting to create an xDS client, else xDS client creation will fail.
		config, err := bootstrap.GetConfiguration()
		if err != nil {
			return nil, nil, fmt.Errorf("xds: failed to read xDS bootstrap config from env vars:  %v", err)
		}
		p.config = config
	}

	if c := p.clients[name]; c != nil {
		c.incrRef()
		return c, sync.OnceFunc(func() { p.clientRefCountedClose(name) }), nil
	}

	gAuthorities := make(map[string]gxdsclient.Authority)
	credentials := make(map[string]credentials.Bundle)

	var serverConfig *bootstrap.ServerConfig

	for name, cfg := range p.config.Authorities() {
		// If server configs are specified in the authorities map, use that.
		// Else, use the top-level server configs.
		serverCfg := p.config.XDSServers()
		if len(cfg.XDSServers) >= 1 {
			serverCfg = cfg.XDSServers
		}
		var gServerCfg []gxdsclient.ServerConfig
		for _, sc := range serverCfg {
			for _, cc := range sc.ChannelCreds() {
				c := xdsbootstrap.GetCredentials(cc.Type)
				if c == nil {
					continue
				}
				bundle, _, err := c.Build(cc.Config)
				if err != nil {
					continue
				}
				credentials[cc.Type] = bundle
			}
			gServerCfg = append(gServerCfg, gxdsclient.ServerConfig{
				ServerIdentifier:       clients.ServerIdentifier{ServerURI: sc.ServerURI(), Extensions: grpctransport.ServerIdentifierExtension{Credentials: sc.ChannelCreds()[0].Type}},
				IgnoreResourceDeletion: sc.ServerFeaturesIgnoreResourceDeletion()})
			serverConfig = sc
		}
		gAuthorities[name] = gxdsclient.Authority{XDSServers: gServerCfg}
	}

	var gServerCfg []gxdsclient.ServerConfig

	for _, sc := range p.config.XDSServers() {
		for _, cc := range sc.ChannelCreds() {
			c := xdsbootstrap.GetCredentials(cc.Type)
			if c == nil {
				continue
			}
			bundle, _, err := c.Build(cc.Config)
			if err != nil {
				continue
			}
			credentials[cc.Type] = bundle
		}
		gServerCfg = append(gServerCfg, gxdsclient.ServerConfig{
			ServerIdentifier:       clients.ServerIdentifier{ServerURI: sc.ServerURI(), Extensions: grpctransport.ServerIdentifierExtension{Credentials: sc.ChannelCreds()[0].Type}},
			IgnoreResourceDeletion: sc.ServerFeaturesIgnoreResourceDeletion()})
		serverConfig = sc
	}

	node := p.config.Node()
	gNode := clients.Node{
		ID:               node.GetId(),
		Cluster:          node.GetCluster(),
		Metadata:         node.Metadata,
		UserAgentName:    node.UserAgentName,
		UserAgentVersion: node.GetUserAgentVersion(),
	}
	if node.Locality != nil {
		gNode.Locality = clients.Locality{
			Region:  node.Locality.Region,
			Zone:    node.Locality.Zone,
			SubZone: node.Locality.SubZone,
		}
	}

	gTransportBuilder := grpctransport.NewBuilder(credentials)

	resouceTypes := make(map[string]gxdsclient.ResourceType)
	resouceTypes[xdsresource.ListenerType.TypeURL] = xdsclient.ResourceType{
		TypeURL:                    xdsresource.ListenerType.TypeURL,
		TypeName:                   xdsresource.ListenerType.TypeName,
		AllResourcesRequiredInSotW: xdsresource.ListenerType.AllResourcesRequiredInSotW,
		Decoder:                    &xdsresource.ListenerDecoder{BootstrapConfig: p.config},
	}
	resouceTypes[xdsresource.RouteConfigType.TypeURL] = xdsclient.ResourceType{
		TypeURL:                    xdsresource.RouteConfigType.TypeURL,
		TypeName:                   xdsresource.RouteConfigType.TypeName,
		AllResourcesRequiredInSotW: xdsresource.RouteConfigType.AllResourcesRequiredInSotW,
		Decoder:                    &xdsresource.RouteConfigDecoder{BootstrapConfig: p.config},
	}
	resouceTypes[xdsresource.ClusterResourceType.TypeURL] = xdsclient.ResourceType{
		TypeURL:                    xdsresource.ClusterResourceType.TypeURL,
		TypeName:                   xdsresource.ClusterResourceType.TypeName,
		AllResourcesRequiredInSotW: xdsresource.ClusterResourceType.AllResourcesRequiredInSotW,
		Decoder:                    &xdsresource.ClusterDecoder{BootstrapConfig: p.config, ServerConfig: serverConfig},
	}
	resouceTypes[xdsresource.EndpointsType.TypeURL] = xdsclient.ResourceType{
		TypeURL:                    xdsresource.EndpointsType.TypeURL,
		TypeName:                   xdsresource.EndpointsType.TypeName,
		AllResourcesRequiredInSotW: xdsresource.EndpointsType.AllResourcesRequiredInSotW,
		Decoder:                    &xdsresource.EndpointsDecoder{},
	}

	gConfig := xdsclient.Config{
		Authorities:      gAuthorities,
		Servers:          gServerCfg,
		Node:             gNode,
		TransportBuilder: gTransportBuilder,
		ResourceTypes:    resouceTypes,
	}

	c, err := gxdsclient.New(gConfig)
	if err != nil {
		return nil, nil, err
	}
	client := &clientRefCounted{clientImpl: &clientImpl{XDSClient: c, config: p.config}, refCount: 1}
	p.clients[name] = client

	xdsClientImplCreateHook(name)

	return client, sync.OnceFunc(func() { p.clientRefCountedClose(name) }), nil
}
