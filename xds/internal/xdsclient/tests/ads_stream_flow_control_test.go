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

package xdsclient_test

import (
	"context"
	"errors"
	"testing"

	"google.golang.org/grpc"
	"google.golang.org/grpc/internal/xds/bootstrap"
	"google.golang.org/grpc/xds/internal/xdsclient"
	xdsclientinternal "google.golang.org/grpc/xds/internal/xdsclient/internal"
	"google.golang.org/grpc/xds/internal/xdsclient/xdsresource"

	v3adsgrpc "github.com/envoyproxy/go-control-plane/envoy/service/discovery/v3"
	v3discoverypb "github.com/envoyproxy/go-control-plane/envoy/service/discovery/v3"
)

// blockingListenerWatcher implements xdsresource.ListenerWatcher. It writes to
// a channel when it receives a callback from the watch. It also makes the
// DoneNotifier passed to the callback available to the test, thereby enabling
// the test to block this watcher for as long as required.
type blockingListenerWatcher struct {
	doneNotifierCh chan func()   // DoneNotifier passed to the callback.
	updateCh       chan struct{} // Written to when an update is received.
	ambientErrCh   chan struct{} // Written to when an ambient error is received.
	resourceErrCh  chan struct{} // Written to when a resource error is received.
}

func newBLockingListenerWatcher() *blockingListenerWatcher {
	return &blockingListenerWatcher{
		doneNotifierCh: make(chan func(), 1),
		updateCh:       make(chan struct{}, 1),
		ambientErrCh:   make(chan struct{}, 1),
		resourceErrCh:  make(chan struct{}, 1),
	}
}

func (lw *blockingListenerWatcher) ResourceChanged(update *xdsresource.ListenerResourceData, done func()) {
	// Notify receipt of the update.
	select {
	case lw.updateCh <- struct{}{}:
	default:
	}

	select {
	case lw.doneNotifierCh <- done:
	default:
	}
}

func (lw *blockingListenerWatcher) ResourceError(err error, done func()) {
	// Notify receipt of an error.
	select {
	case lw.resourceErrCh <- struct{}{}:
	default:
	}

	select {
	case lw.doneNotifierCh <- done:
	default:
	}
}

func (lw *blockingListenerWatcher) AmbientError(err error, done func()) {
	// Notify receipt of an error.
	select {
	case lw.ambientErrCh <- struct{}{}:
	default:
	}

	select {
	case lw.doneNotifierCh <- done:
	default:
	}
}

type wrappedADSStream struct {
	v3adsgrpc.AggregatedDiscoveryService_StreamAggregatedResourcesClient
	recvCh chan struct{}
	doneCh <-chan struct{}
}

func newWrappedADSStream(stream v3adsgrpc.AggregatedDiscoveryService_StreamAggregatedResourcesClient, doneCh <-chan struct{}) *wrappedADSStream {
	return &wrappedADSStream{
		AggregatedDiscoveryService_StreamAggregatedResourcesClient: stream,
		recvCh: make(chan struct{}, 1),
		doneCh: doneCh,
	}
}

func (w *wrappedADSStream) Recv() (*v3discoverypb.DiscoveryResponse, error) {
	select {
	case w.recvCh <- struct{}{}:
	case <-w.doneCh:
		return nil, errors.New("Recv() called after the test has finished")
	}
	return w.AggregatedDiscoveryService_StreamAggregatedResourcesClient.Recv()
}

// Overrides the function to create a new ADS stream (used by the xdsclient
// transport), and returns a wrapped ADS stream, where the test can monitor
// Recv() calls.
func overrideADSStreamCreation(t *testing.T) chan *wrappedADSStream {
	t.Helper()

	adsStreamCh := make(chan *wrappedADSStream, 1)
	origNewADSStream := xdsclientinternal.NewADSStream
	xdsclientinternal.NewADSStream = func(ctx context.Context, cc *grpc.ClientConn) (v3adsgrpc.AggregatedDiscoveryService_StreamAggregatedResourcesClient, error) {
		s, err := v3adsgrpc.NewAggregatedDiscoveryServiceClient(cc).StreamAggregatedResources(ctx)
		if err != nil {
			return nil, err
		}
		ws := newWrappedADSStream(s, ctx.Done())
		select {
		case adsStreamCh <- ws:
		default:
		}
		return ws, nil
	}
	t.Cleanup(func() { xdsclientinternal.NewADSStream = origNewADSStream })
	return adsStreamCh
}

// Creates an xDS client with the given bootstrap contents.
func createXDSClient(t *testing.T, bootstrapContents []byte) xdsclient.XDSClient {
	t.Helper()

	config, err := bootstrap.NewConfigFromContents(bootstrapContents)
	if err != nil {
		t.Fatalf("Failed to parse bootstrap contents: %s, %v", string(bootstrapContents), err)
	}
	pool := xdsclient.NewPool(config)
	client, close, err := pool.NewClientForTesting(xdsclient.OptionsForTesting{
		Name: t.Name(),
	})
	if err != nil {
		t.Fatalf("Failed to create xDS client: %v", err)
	}
	t.Cleanup(close)
	return client
}
