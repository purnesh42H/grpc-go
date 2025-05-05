/*
 *
 * Copyright 2019 gRPC authors.
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
 */

package xdsclient

import (
	"context"
	"time"

	"google.golang.org/grpc/credentials"
	"google.golang.org/grpc/internal/xds/bootstrap"
	xdsbootstrap "google.golang.org/grpc/xds/bootstrap"
	"google.golang.org/grpc/xds/internal/clients"
	"google.golang.org/grpc/xds/internal/clients/grpctransport"
	"google.golang.org/grpc/xds/internal/clients/lrsclient"
)

const (
	loadStoreStopTimeout = 1 * time.Second
)

// ReportLoad starts a load reporting stream to the given server. All load
// reports to the same server share the LRS stream.
//
// It returns a lrsclient.LoadStore for the user to report loads.
func (c *clientImpl) ReportLoad(server *bootstrap.ServerConfig) (*lrsclient.LoadStore, func()) {
	credentials := make(map[string]credentials.Bundle)
	selectedCreds := ""

	for _, cc := range server.ChannelCreds() {
		c := xdsbootstrap.GetCredentials(cc.Type)
		if c == nil {
			continue
		}
		bundle, _, err := c.Build(cc.Config)
		if err != nil {
			continue
		}
		if selectedCreds == "" {
			selectedCreds = cc.Type
		}
		credentials[cc.Type] = bundle
	}
	lrsServerIdentifier := clients.ServerIdentifier{ServerURI: server.ServerURI(), Extensions: grpctransport.ServerIdentifierExtension{Credentials: selectedCreds}}

	if c.lrsClient == nil {
		node := c.BootstrapConfig().Node()
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

		lrsConfig := lrsclient.Config{Node: gNode, TransportBuilder: gTransportBuilder}
		lrsC, err := lrsclient.New(lrsConfig)
		if err != nil {
			c.logger.Warningf("Failed to create an lrs client to the management server to report load: %v", server, err)
			return nil, func() {}
		}
		c.lrsClient = lrsC
	}
	load, err := c.lrsClient.ReportLoad(lrsServerIdentifier)
	if err != nil {
		c.logger.Warningf("Failed to create a load store to the management server to report load: %v", server, err)
		return nil, func() {}
	}
	return load, func() {
		ctx, cancel := context.WithTimeout(context.Background(), loadStoreStopTimeout)
		defer cancel()
		load.Stop(ctx)
	}
}
