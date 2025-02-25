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

package xdsclient_test

import (
	"context"
	"fmt"
	"strings"
	"testing"

	"github.com/envoyproxy/go-control-plane/pkg/wellknown"
	"github.com/google/go-cmp/cmp"
	"github.com/google/go-cmp/cmp/cmpopts"
	"github.com/google/uuid"
	"google.golang.org/grpc/credentials/insecure"
	"google.golang.org/grpc/internal/testutils"
	"google.golang.org/grpc/xds/internal/clients"
	"google.golang.org/grpc/xds/internal/clients/grpctransport"
	"google.golang.org/grpc/xds/internal/clients/internal/testutils/e2e"
	"google.golang.org/grpc/xds/internal/clients/xdsclient"
	"google.golang.org/grpc/xds/internal/clients/xdsclient/client"
	xdsclienttestutils "google.golang.org/grpc/xds/internal/clients/xdsclient/internal/testutils"
	"google.golang.org/grpc/xds/internal/clients/xdsclient/internal/xdsresource"

	v3listenerpb "github.com/envoyproxy/go-control-plane/envoy/config/listener/v3"
	v3routerpb "github.com/envoyproxy/go-control-plane/envoy/extensions/filters/http/router/v3"
	v3httppb "github.com/envoyproxy/go-control-plane/envoy/extensions/filters/network/http_connection_manager/v3"

	_ "google.golang.org/grpc/xds"                            // To ensure internal.NewXDSResolverWithConfigForTesting is set.
	_ "google.golang.org/grpc/xds/internal/httpfilter/router" // Register the router filter.
)

type listenerUpdateErrTuple struct {
	update xdsclienttestutils.ListenerUpdate
	err    error
}

type listenerWatcher struct {
	updateCh *testutils.Channel
}

func newListenerWatcher() *listenerWatcher {
	return &listenerWatcher{updateCh: testutils.NewChannel()}
}

func (lw *listenerWatcher) ResourceChanged(data xdsclient.ResourceData, onDone func()) {
	lisData, ok := data.(*xdsclienttestutils.ListenerResourceData)
	if !ok {
		lw.updateCh.Send(listenerUpdateErrTuple{err: fmt.Errorf("unexpected resource type: %T", data)})
		onDone()
		return
	}
	lw.updateCh.Send(listenerUpdateErrTuple{update: lisData.Resource})
	onDone()
}

func (lw *listenerWatcher) ResourceError(err error, onDone func()) {
	lw.updateCh.Replace(listenerUpdateErrTuple{err: xdsresource.NewErrorf(xdsresource.ErrorTypeResourceNotFound, "Listener not found in received response")})
	// When used with a go-control-plane management server that continuously
	// resends resources which are NACKed by the xDS client, using a `Replace()`
	// here and in OnResourceDoesNotExist() simplifies tests which will have
	// access to the most recently received error.
	lw.updateCh.Replace(listenerUpdateErrTuple{err: err})
	onDone()
}

func (lw *listenerWatcher) AmbientError(err error, onDone func()) {

	onDone()
}

// badListenerResource returns a listener resource for the given name which does
// not contain the `RouteSpecifier` field in the HTTPConnectionManager, and
// hence is expected to be NACKed by the client.
func badListenerResource(t *testing.T, name string) *v3listenerpb.Listener {
	hcm := testutils.MarshalAny(t, &v3httppb.HttpConnectionManager{
		HttpFilters: []*v3httppb.HttpFilter{e2e.HTTPFilter("router", &v3routerpb.Router{})},
	})
	return &v3listenerpb.Listener{
		Name:        name,
		ApiListener: &v3listenerpb.ApiListener{ApiListener: hcm},
		FilterChains: []*v3listenerpb.FilterChain{{
			Name: "filter-chain-name",
			Filters: []*v3listenerpb.Filter{{
				Name:       wellknown.HTTPConnectionManager,
				ConfigType: &v3listenerpb.Filter_TypedConfig{TypedConfig: hcm},
			}},
		}},
	}
}

// xdsClient is expected to produce an error containing this string when an
// update is received containing a listener created using `badListenerResource`.
const wantListenerNACKErr = "no RouteSpecifier"

// verifyNoListenerUpdate verifies that no listener update is received on the
// provided update channel, and returns an error if an update is received.
//
// A very short deadline is used while waiting for the update, as this function
// is intended to be used when an update is not expected.
func verifyNoListenerUpdate(ctx context.Context, updateCh *testutils.Channel) error {
	sCtx, sCancel := context.WithTimeout(ctx, defaultTestShortTimeout)
	defer sCancel()
	if u, err := updateCh.Receive(sCtx); err != context.DeadlineExceeded {
		return fmt.Errorf("unexpected ListenerUpdate: %v", u)
	}
	return nil
}

// verifyListenerUpdate waits for an update to be received on the provided
// update channel and verifies that it matches the expected update.
//
// Returns an error if no update is received before the context deadline expires
// or the received update does not match the expected one.
func verifyListenerUpdate(ctx context.Context, updateCh *testutils.Channel, wantUpdate listenerUpdateErrTuple) error {
	u, err := updateCh.Receive(ctx)
	if err != nil {
		return fmt.Errorf("timeout when waiting for a listener resource from the management server: %v", err)
	}
	got := u.(listenerUpdateErrTuple)
	if wantUpdate.err != nil {
		if gotType, wantType := xdsresource.ErrType(got.err), xdsresource.ErrType(wantUpdate.err); gotType != wantType {
			return fmt.Errorf("received update with error type %v, want %v", gotType, wantType)
		}
	}
	cmpOpts := []cmp.Option{
		cmpopts.EquateEmpty(),
		cmpopts.IgnoreFields(xdsclienttestutils.HTTPFilter{}, "Filter", "Config"),
		cmpopts.IgnoreFields(xdsclienttestutils.ListenerUpdate{}, "Raw"),
	}
	if diff := cmp.Diff(wantUpdate.update, got.update, cmpOpts...); diff != "" {
		return fmt.Errorf("received unexpected diff in the listener resource update: (-want, got):\n%s", diff)
	}
	return nil
}

func verifyListenerError(ctx context.Context, updateCh *testutils.Channel, wantErr, wantNodeID string) error {
	u, err := updateCh.Receive(ctx)
	if err != nil {
		return fmt.Errorf("timeout when waiting for a listener error from the management server: %v", err)
	}
	gotErr := u.(listenerUpdateErrTuple).err
	if gotErr == nil || !strings.Contains(gotErr.Error(), wantErr) {
		return fmt.Errorf("update received with error: %v, want %q", gotErr, wantErr)
	}
	if !strings.Contains(gotErr.Error(), wantNodeID) {
		return fmt.Errorf("update received with error: %v, want error with node ID: %q", gotErr, wantNodeID)
	}
	return nil
}

// TestLDSWatch covers the case where a single watcher exists for a single
// listener resource. The test verifies the following scenarios:
//  1. An update from the management server containing the resource being
//     watched should result in the invocation of the watch callback.
//  2. An update from the management server containing a resource *not* being
//     watched should not result in the invocation of the watch callback.
//  3. After the watch is cancelled, an update from the management server
//     containing the resource that was being watched should not result in the
//     invocation of the watch callback.
//
// The test is run for old and new style names.
func TestLDSWatch(t *testing.T) {
	tests := []struct {
		desc                   string
		resourceName           string
		watchedResource        *v3listenerpb.Listener // The resource being watched.
		updatedWatchedResource *v3listenerpb.Listener // The watched resource after an update.
		notWatchedResource     *v3listenerpb.Listener // A resource which is not being watched.
		wantUpdate             listenerUpdateErrTuple
	}{
		{
			desc:                   "old style resource",
			resourceName:           ldsName,
			watchedResource:        e2e.DefaultClientListener(ldsName, rdsName),
			updatedWatchedResource: e2e.DefaultClientListener(ldsName, "new-rds-resource"),
			notWatchedResource:     e2e.DefaultClientListener("unsubscribed-lds-resource", rdsName),
			wantUpdate: listenerUpdateErrTuple{
				update: xdsclienttestutils.ListenerUpdate{
					RouteConfigName: rdsName,
					HTTPFilters:     []xdsclienttestutils.HTTPFilter{{Name: "router"}},
				},
			},
		},
		{
			desc:                   "new style resource",
			resourceName:           ldsNameNewStyle,
			watchedResource:        e2e.DefaultClientListener(ldsNameNewStyle, rdsNameNewStyle),
			updatedWatchedResource: e2e.DefaultClientListener(ldsNameNewStyle, "new-rds-resource"),
			notWatchedResource:     e2e.DefaultClientListener("unsubscribed-lds-resource", rdsNameNewStyle),
			wantUpdate: listenerUpdateErrTuple{
				update: xdsclienttestutils.ListenerUpdate{
					RouteConfigName: rdsNameNewStyle,
					HTTPFilters:     []xdsclienttestutils.HTTPFilter{{Name: "router"}},
				},
			},
		},
	}

	for _, test := range tests {
		t.Run(test.desc, func(t *testing.T) {
			mgmtServer := e2e.StartManagementServer(t, e2e.ManagementServerOptions{})

			nodeID := uuid.New().String()

			resourceTypes := map[string]xdsclient.ResourceType{}
			listenerType := xdsclienttestutils.ResourceTypeMapForTesting[xdsresource.V3ListenerURL]
			resourceTypes[xdsresource.V3ListenerURL] = listenerType.(xdsclient.ResourceType)

			xdsClientConfig := xdsclient.Config{
				Servers:          []clients.ServerConfig{{ServerURI: mgmtServer.Address, Extensions: grpctransport.ServerConfigExtension{Credentials: insecure.NewBundle()}}},
				Node:             clients.Node{ID: nodeID},
				TransportBuilder: &grpctransport.Builder{},
				ResourceTypes:    resourceTypes,
			}

			// Create an xDS client with the above bootstrap contents.
			client, err := client.New(xdsClientConfig)
			if err != nil {
				t.Fatalf("Failed to create xDS client: %v", err)
			}
			defer client.Close()

			// Register a watch for a listener resource and have the watch
			// callback push the received update on to a channel.
			lw := newListenerWatcher()
			ldsCancel := client.WatchResource(xdsresource.V3ListenerURL, test.resourceName, lw)

			// Configure the management server to return a single listener
			// resource, corresponding to the one we registered a watch for.
			resources := e2e.UpdateOptions{
				NodeID:         nodeID,
				Listeners:      []*v3listenerpb.Listener{test.watchedResource},
				SkipValidation: true,
			}
			ctx, cancel := context.WithTimeout(context.Background(), defaultTestTimeout)
			defer cancel()
			if err := mgmtServer.Update(ctx, resources); err != nil {
				t.Fatalf("Failed to update management server with resources: %v, err: %v", resources, err)
			}

			// Verify the contents of the received update.
			if err := verifyListenerUpdate(ctx, lw.updateCh, test.wantUpdate); err != nil {
				t.Fatal(err)
			}

			// Configure the management server to return an additional listener
			// resource, one that we are not interested in.
			resources = e2e.UpdateOptions{
				NodeID:         nodeID,
				Listeners:      []*v3listenerpb.Listener{test.watchedResource, test.notWatchedResource},
				SkipValidation: true,
			}
			if err := mgmtServer.Update(ctx, resources); err != nil {
				t.Fatalf("Failed to update management server with resources: %v, err: %v", resources, err)
			}
			if err := verifyNoListenerUpdate(ctx, lw.updateCh); err != nil {
				t.Fatal(err)
			}

			// Cancel the watch and update the resource corresponding to the original
			// watch.  Ensure that the cancelled watch callback is not invoked.
			ldsCancel()
			resources = e2e.UpdateOptions{
				NodeID:         nodeID,
				Listeners:      []*v3listenerpb.Listener{test.updatedWatchedResource, test.notWatchedResource},
				SkipValidation: true,
			}
			if err := mgmtServer.Update(ctx, resources); err != nil {
				t.Fatalf("Failed to update management server with resources: %v, err: %v", resources, err)
			}
			if err := verifyNoListenerUpdate(ctx, lw.updateCh); err != nil {
				t.Fatal(err)
			}
		})
	}
}
