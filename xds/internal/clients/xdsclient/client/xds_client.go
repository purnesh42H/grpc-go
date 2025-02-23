//revive:disable:unused-parameter

/*
 *
 * Copyright 2025 gRPC authors.
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

// Package client provides ways to create and use an xDS client to watch xDS
// resources from an xDS management server.
package client

import (
	v3statuspb "github.com/envoyproxy/go-control-plane/envoy/service/status/v3"
	"google.golang.org/grpc/xds/internal/clients/xdsclient"
)

// XDSClient is a client which queries a set of discovery APIs (collectively
// termed as xDS) on a remote management server, to discover
// various dynamic resources.
type XDSClient struct {
}

// New returns a new xDS Client configured with the provided config.
func New(config xdsclient.Config) (*XDSClient, error) {
	panic("unimplemented")
}

// WatchResource starts watching the specified resource.
//
// typeURL specifies the resource type implementation to use. The watch fails
// if there is no resource type implementation for the given typeURL. See the
// ResourceTypes field in the Config struct used to create the XDSClient.
//
// The returned function cancels the watch and prevents future calls to the
// watcher.
func (c *XDSClient) WatchResource(typeURL, name string, watcher xdsclient.ResourceWatcher) (cancel func()) {
	panic("unimplemented")
}

// Close closes the xDS client.
func (c *XDSClient) Close() error {
	panic("unimplemented")
}

// DumpResources returns the status and contents of all xDS resources being
// watched by the xDS client.
func (c *XDSClient) DumpResources() *v3statuspb.ClientStatusResponse {
	panic("unimplemented")
}
