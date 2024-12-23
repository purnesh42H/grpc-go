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
 */

package xdsclient

import (
	"context"
	"fmt"
	"sync/atomic"

	"google.golang.org/grpc/grpclog"
	igrpclog "google.golang.org/grpc/internal/grpclog"
	"google.golang.org/grpc/internal/grpcsync"
	"google.golang.org/grpc/xds/clients"
	"google.golang.org/grpc/xds/clients/xdsclient/xdsresource"
	"google.golang.org/protobuf/types/known/anypb"
	"google.golang.org/protobuf/types/known/timestamppb"

	v3adminpb "github.com/envoyproxy/go-control-plane/envoy/admin/v3"
	v3statuspb "github.com/envoyproxy/go-control-plane/envoy/service/status/v3"
)

type resourceState struct {
	watchers          map[ResourceWatcher]bool       // Set of watchers for this resource.
	cache             ResourceData                   // Most recent ACKed update for this resource.
	md                updateMetadata                 // Metadata for the most recent update.
	deletionIgnored   bool                           // True, if resource deletion was ignored for a prior update.
	xdsChannelConfigs map[*xdsChannelWithConfig]bool // Set of xdsChannels where this resource is subscribed.
}

// xdsChannelForADS is used to acquire a reference to an xdsChannel. This
// functionality is provided by the xdsClient.
//
// The arguments to the function are as follows:
//   - the server config for the xdsChannel
//   - the calling authority on which a set of callbacks are invoked by the
//     xdsChannel on ADS stream events
//
// Returns a reference to the xdsChannel and a function to release the same. A
// non-nil error is returned if the channel creation fails and the first two
// return values are meaningless in this case.
type xdsChannelForADS func(*clients.ServerConfig, *authorityState) (*xdsChannel, func(), error)

// xdsChannelWithConfig is a struct that holds an xdsChannel and its associated
// ServerConfig, along with a cleanup function to release the xdsChannel.
type xdsChannelWithConfig struct {
	channel      *xdsChannel
	serverConfig *clients.ServerConfig
	cleanup      func()
}

// authority provides the functionality required to communicate with a
// management server corresponding to an authority name specified in the
// bootstrap configuration.
//
// It holds references to one or more xdsChannels, one for each server
// configuration in the bootstrap, to allow fallback from a primary management
// server to a secondary management server. Authorities that contain similar
// server configuration entries will end up sharing the xdsChannel for that
// server configuration. The xdsChannels are owned and managed by the xdsClient.
//
// It also contains a cache of resource state for resources requested from
// management server(s). This cache contains the list of registered watchers and
// the most recent resource configuration received from the management server.
type authorityState struct {
	// The following fields are initialized at creation time and are read-only
	// afterwards, and therefore don't need to be protected with a mutex.
	name                      string                       // Name of the authority from bootstrap configuration.
	watcherCallbackSerializer *grpcsync.CallbackSerializer // Serializer to run watcher callbacks, owned by the xDS client implementation.
	getChannelForADS          xdsChannelForADS             // Function to get an xdsChannel for ADS, provided by the xDS client implementation.
	xdsClientSerializer       *grpcsync.CallbackSerializer // Serializer to run call ins from the xDS client, owned by this authority.
	xdsClientSerializerClose  func()                       // Function to close the above serializer.
	logger                    *igrpclog.PrefixLogger       // Logger for this authority.

	// The below defined fields must only be accessed in the context of the
	// serializer callback, owned by this authority.

	// A two level map containing the state of all the resources being watched.
	//
	// The first level map key is the ResourceType (Listener, Route etc). This
	// allows us to have a single map for all resources instead of having per
	// resource-type maps.
	//
	// The second level map key is the resource name, with the value being the
	// actual state of the resource.
	resources map[ResourceType]map[string]*resourceState

	// An ordered list of xdsChannels corresponding to the list of server
	// configurations specified for this authority in the bootstrap. The
	// ordering specifies the order in which these channels are preferred for
	// fallback.
	xdsChannelConfigs []*xdsChannelWithConfig

	// The current active xdsChannel. Here, active does not mean that the
	// channel has a working connection to the server. It simply points to the
	// channel that we are trying to work with, based on fallback logic.
	activeXDSChannel *xdsChannelWithConfig
}

// authorityBuildOptions wraps arguments required to create a new authority.
type authorityBuildOptions struct {
	serverConfigs    []*clients.ServerConfig      // Server configs for the authority
	name             string                       // Name of the authority
	serializer       *grpcsync.CallbackSerializer // Callback serializer for invoking watch callbacks
	getChannelForADS xdsChannelForADS             // Function to acquire a reference to an xdsChannel
	logPrefix        string                       // Prefix for logging
}

// newAuthority creates a new authority instance with the provided
// configuration. The authority is responsible for managing the state of
// resources requested from the management server, as well as acquiring and
// releasing references to channels used to communicate with the management
// server.
//
// Note that no channels to management servers are created at this time. Instead
// a channel to the first server configuration is created when the first watch
// is registered, and more channels are created as needed by the fallback logic.
func newAuthorityState(args authorityBuildOptions) *authorityState {
	ctx, cancel := context.WithCancel(context.Background())
	l := grpclog.Component("xds")
	logPrefix := args.logPrefix + fmt.Sprintf("[authority %q] ", args.name)
	ret := &authorityState{
		name:                      args.name,
		watcherCallbackSerializer: args.serializer,
		getChannelForADS:          args.getChannelForADS,
		xdsClientSerializer:       grpcsync.NewCallbackSerializer(ctx),
		xdsClientSerializerClose:  cancel,
		logger:                    igrpclog.NewPrefixLogger(l, logPrefix),
		resources:                 make(map[ResourceType]map[string]*resourceState),
	}

	// Create an ordered list of xdsChannels with their server configs. The
	// actual channel to the first server configuration is created when the
	// first watch is registered, and channels to other server configurations
	// are created as needed to support fallback.
	for _, sc := range args.serverConfigs {
		ret.xdsChannelConfigs = append(ret.xdsChannelConfigs, &xdsChannelWithConfig{serverConfig: sc})
	}
	return ret
}

// adsStreamFailure is called to notify the authority about an ADS stream
// failure on an xdsChannel to the management server identified by the provided
// server config. The error is forwarded to all the resource watchers.
//
// This method is called by the xDS client implementation (on all interested
// authorities) when a stream error is reported by an xdsChannel.
//
// Errors of type xdsresource.ErrTypeStreamFailedAfterRecv are ignored.
func (a *authorityState) adsStreamFailure(serverConfig *clients.ServerConfig, err error) {
	a.xdsClientSerializer.TrySchedule(func(context.Context) {
		a.handleADSStreamFailure(serverConfig, err)
	})
}

// Handles ADS stream failure by invoking watch callbacks and triggering
// fallback if the associated conditions are met.
//
// Only executed in the context of a serializer callback.
func (a *authorityState) handleADSStreamFailure(serverConfig *clients.ServerConfig, err error) {
	if a.logger.V(2) {
		a.logger.Infof("Connection to server %s failed with error: %v", serverConfig, err)
	}

	// We do not consider it an error if the ADS stream was closed after having
	// received a response on the stream. This is because there are legitimate
	// reasons why the server may need to close the stream during normal
	// operations, such as needing to rebalance load or the underlying
	// connection hitting its max connection age limit. See gRFC A57 for more
	// details.
	if xdsresource.ErrType(err) == xdsresource.ErrTypeStreamFailedAfterRecv {
		a.logger.Warningf("Watchers not notified since ADS stream failed after having received at least one response: %v", err)
		return
	}

	// Propagate the connection error from the transport layer to all watchers.
	for _, rType := range a.resources {
		for _, state := range rType {
			for watcher := range state.watchers {
				watcher := watcher
				a.watcherCallbackSerializer.TrySchedule(func(context.Context) {
					watcher.OnAmbientError(xdsresource.NewErrorf(xdsresource.ErrorTypeConnection, "xds: error received from xDS stream: %v", err), func() {})
				})
			}
		}
	}

	// Two conditions need to be met for fallback to be triggered:
	// 1. There is a connectivity failure on the ADS stream, as described in
	//    gRFC A57. For us, this means that the ADS stream was closed before the
	//    first server response was received. We already checked that condition
	//    earlier in this method.
	// 2. There is at least one watcher for a resource that is not cached.
	//    Cached resources include ones that
	//    - have been successfully received and can be used.
	//    - are considered non-existent according to xDS Protocol Specification.
	if !a.watcherExistsForUncachedResource() {
		if a.logger.V(2) {
			a.logger.Infof("No watchers for uncached resources. Not triggering fallback")
		}
		return
	}
	a.fallbackToNextServerIfPossible(serverConfig)
}

// serverIndexForConfig returns the index of the xdsChannelConfig that matches
// the provided ServerConfig. If no match is found, it returns the length of the
// xdsChannelConfigs slice, which represents the index of a non-existent config.
func (a *authorityState) serverIndexForConfig(sc *clients.ServerConfig) int {
	for i, cfg := range a.xdsChannelConfigs {
		if cfg.serverConfig.Equal(sc) {
			return i
		}
	}
	return len(a.xdsChannelConfigs)
}

// Determines the server to fallback to and triggers fallback to the same. If
// required, creates an xdsChannel to that server, and re-subscribes to all
// existing resources.
//
// Only executed in the context of a serializer callback.
func (a *authorityState) fallbackToNextServerIfPossible(failingServerConfig *clients.ServerConfig) {
	if a.logger.V(2) {
		a.logger.Infof("Attempting to initiate fallback after failure from server %q", failingServerConfig)
	}

	// The server to fallback to is the next server on the list. If the current
	// server is the last server, then there is nothing that can be done.
	currentServerIdx := a.serverIndexForConfig(failingServerConfig)
	if currentServerIdx == len(a.xdsChannelConfigs) {
		// This can never happen.
		a.logger.Errorf("Received error from an unknown server: %s", failingServerConfig)
		return
	}
	if currentServerIdx == len(a.xdsChannelConfigs)-1 {
		if a.logger.V(2) {
			a.logger.Infof("No more servers to fallback to")
		}
		return
	}
	fallbackServerIdx := currentServerIdx + 1
	fallbackChannel := a.xdsChannelConfigs[fallbackServerIdx]

	// If the server to fallback to already has an xdsChannel, it means that
	// this connectivity error is from a server with a higher priority. There
	// is not much we can do here.
	if fallbackChannel.channel != nil {
		if a.logger.V(2) {
			a.logger.Infof("Channel to the next server in the list %q already exists", fallbackChannel.serverConfig)
		}
		return
	}

	// Create an xdsChannel for the fallback server.
	if a.logger.V(2) {
		a.logger.Infof("Initiating fallback to server %s", fallbackChannel.serverConfig)
	}
	xc, cleanup, err := a.getChannelForADS(fallbackChannel.serverConfig, a)
	if err != nil {
		a.logger.Errorf("Failed to create XDS channel: %v", err)
		return
	}
	fallbackChannel.channel = xc
	fallbackChannel.cleanup = cleanup
	a.activeXDSChannel = fallbackChannel

	// Subscribe to all existing resources from the new management server.
	for typ, resources := range a.resources {
		for name, state := range resources {
			if a.logger.V(2) {
				a.logger.Infof("Resubscribing to resource of type %q and name %q", typ.TypeName(), name)
			}
			xc.subscribe(typ, name)

			// Add the fallback channel to the list of xdsChannels from which
			// this resource has been requested from. Retain the cached resource
			// and the set of existing watchers (and other metadata fields) in
			// the resource state.
			state.xdsChannelConfigs[fallbackChannel] = true
		}
	}
}

// adsResourceUpdate is called to notify the authority about a resource update
// received on the ADS stream.
//
// This method is called by the xDS client implementation (on all interested
// authorities) when a stream error is reported by an xdsChannel.
func (a *authorityState) adsResourceUpdate(serverConfig *clients.ServerConfig, rType ResourceType, updates map[string]dataAndErrTuple, md updateMetadata, onDone func()) {
	a.xdsClientSerializer.TrySchedule(func(context.Context) {
		a.handleADSResourceUpdate(serverConfig, rType, updates, md, onDone)
	})
}

// handleADSResourceUpdate processes an update from the xDS client, updating the
// resource cache and notifying any registered watchers of the update.
//
// If the update is received from a higher priority xdsChannel that was
// previously down, we revert to it and close all lower priority xdsChannels.
//
// Once the update has been processed by all watchers, the authority is expected
// to invoke the onDone callback.
//
// Only executed in the context of a serializer callback.
func (a *authorityState) handleADSResourceUpdate(serverConfig *clients.ServerConfig, rType ResourceType, updates map[string]dataAndErrTuple, md updateMetadata, onDone func()) {
	a.handleRevertingToPrimaryOnUpdate(serverConfig)

	// We build a list of callback funcs to invoke, and invoke them at the end
	// of this method instead of inline (when handling the update for a
	// particular resource), because we want to make sure that all calls to
	// increment watcherCnt happen before any callbacks are invoked. This will
	// ensure that the onDone callback is never invoked before all watcher
	// callbacks are invoked, and the watchers have processed the update.
	watcherCnt := new(atomic.Int64)
	done := func() {
		if watcherCnt.Add(-1) == 0 {
			onDone()
		}
	}
	funcsToSchedule := []func(context.Context){}
	defer func() {
		if len(funcsToSchedule) == 0 {
			// When there are no watchers for the resources received as part of
			// this update, invoke onDone explicitly to unblock the next read on
			// the ADS stream.
			onDone()
			return
		}
		for _, f := range funcsToSchedule {
			a.watcherCallbackSerializer.ScheduleOr(f, onDone)
		}
	}()

	resourceStates := a.resources[rType]
	for name, uErr := range updates {
		state, ok := resourceStates[name]
		if !ok {
			continue
		}

		// On error, keep previous version of the resource. But update status
		// and error.
		if uErr.err != nil {
			state.md.errState = md.errState
			state.md.status = md.status
			for watcher := range state.watchers {
				watcher := watcher
				err := uErr.err
				watcherCnt.Add(1)
				funcsToSchedule = append(funcsToSchedule, func(context.Context) { watcher.OnAmbientError(err, done) })
			}
			continue
		}

		if state.deletionIgnored {
			state.deletionIgnored = false
			a.logger.Infof("A valid update was received for resource %q of type %q after previously ignoring a deletion", name, rType.TypeName())
		}
		// Notify watchers if any of these conditions are met:
		//   - this is the first update for this resource
		//   - this update is different from the one currently cached
		//   - the previous update for this resource was NACKed, but the update
		//     before that was the same as this update.
		if state.cache == nil || !state.cache.RawEqual(uErr.resource) || state.md.errState != nil {
			// Update the resource cache.
			if a.logger.V(2) {
				a.logger.Infof("Resource type %q with name %q added to cache", rType.TypeName(), name)
			}
			state.cache = uErr.resource

			for watcher := range state.watchers {
				watcher := watcher
				resource := uErr.resource
				watcherCnt.Add(1)
				funcsToSchedule = append(funcsToSchedule, func(context.Context) { watcher.OnResourceChanged(resource, nil, done) })
			}
		}

		// Set status to ACK, and clear error state. The metadata might be a
		// NACK metadata because some other resources in the same response
		// are invalid.
		state.md = md
		state.md.errState = nil
		state.md.status = serviceStatusACKed
		if md.errState != nil {
			state.md.version = md.errState.version
		}
	}

	// If this resource type requires that all resources be present in every
	// SotW response from the server, a response that does not include a
	// previously seen resource will be interpreted as a deletion of that
	// resource unless ignore_resource_deletion option was set in the server
	// config.
	if !rType.AllResourcesRequiredInSotW() {
		return
	}
	for name, state := range resourceStates {
		if state.cache == nil {
			// If the resource state does not contain a cached update, which can
			// happen when:
			// - resource was newly requested but has not yet been received, or,
			// - resource was removed as part of a previous update,
			// we don't want to generate an error for the watchers.
			//
			// For the first of the above two conditions, this ADS response may
			// be in reaction to an earlier request that did not yet request the
			// new resource, so its absence from the response does not
			// necessarily indicate that the resource does not exist. For that
			// case, we rely on the request timeout instead.
			//
			// For the second of the above two conditions, we already generated
			// an error when we received the first response which removed this
			// resource. So, there is no need to generate another one.
			continue
		}
		if _, ok := updates[name]; ok {
			// If the resource was present in the response, move on.
			continue
		}
		if state.md.status == serviceStatusNotExist {
			// The metadata status is set to "ServiceStatusNotExist" if a
			// previous update deleted this resource, in which case we do not
			// want to repeatedly call the watch callbacks with a
			// "resource-not-found" error.
			continue
		}
		if serverConfig.IgnoreResourceDeletion {
			// Per A53, resource deletions are ignored if the
			// `ignore_resource_deletion` server feature is enabled through the
			// bootstrap configuration. If the resource deletion is to be
			// ignored, the resource is not removed from the cache and the
			// corresponding OnResourceDoesNotExist() callback is not invoked on
			// the watchers.
			if !state.deletionIgnored {
				state.deletionIgnored = true
				a.logger.Warningf("Ignoring resource deletion for resource %q of type %q", name, rType.TypeName())
			}
			continue
		}

		// If we get here, it means that the resource exists in cache, but not
		// in the new update. Delete the resource from cache, and send a
		// resource not found error to indicate that the resource has been
		// removed. Metadata for the resource is still maintained, as this is
		// required by CSDS.
		state.cache = nil
		state.md = updateMetadata{status: serviceStatusNotExist}
		for watcher := range state.watchers {
			watcher := watcher
			watcherCnt.Add(1)
			funcsToSchedule = append(funcsToSchedule, func(context.Context) {
				watcher.OnResourceChanged(nil, xdsresource.NewErrorf(xdsresource.ErrorTypeResourceNotFound, "resource not found in received response"), done)
			})
		}
	}
}

// adsResourceDoesNotExist is called by the xDS client implementation (on all
// interested authorities) to notify the authority that a subscribed resource
// does not exist.
func (a *authorityState) adsResourceDoesNotExist(rType ResourceType, resourceName string) {
	a.xdsClientSerializer.TrySchedule(func(context.Context) {
		a.handleADSResourceDoesNotExist(rType, resourceName)
	})
}

// handleADSResourceDoesNotExist is called when a subscribed resource does not
// exist. It removes the resource from the cache, updates the metadata status
// to ServiceStatusNotExist, and notifies all watchers that the resource does
// not exist.
func (a *authorityState) handleADSResourceDoesNotExist(rType ResourceType, resourceName string) {
	if a.logger.V(2) {
		a.logger.Infof("Watch for resource %q of type %s timed out", resourceName, rType.TypeName())
	}

	resourceStates := a.resources[rType]
	if resourceStates == nil {
		if a.logger.V(2) {
			a.logger.Infof("Resource %q of type %s currently not being watched", resourceName, rType.TypeName())
		}
		return
	}
	state, ok := resourceStates[resourceName]
	if !ok {
		if a.logger.V(2) {
			a.logger.Infof("Resource %q of type %s currently not being watched", resourceName, rType.TypeName())
		}
		return
	}

	state.cache = nil
	state.md = updateMetadata{status: serviceStatusNotExist}
	for watcher := range state.watchers {
		watcher := watcher
		a.watcherCallbackSerializer.TrySchedule(func(context.Context) {
			watcher.OnResourceChanged(nil, xdsresource.NewErrorf(xdsresource.ErrorTypeResourceNotFound, "resource not found in received response"), func() {})
		})
	}
}

// handleRevertingToPrimaryOnUpdate is called when a resource update is received
// from the xDS client.
//
// If the update is from the currently active server, nothing is done. Else, all
// lower priority servers are closed and the active server is reverted to the
// highest priority server that sent the update.
//
// This method is only executed in the context of a serializer callback.
func (a *authorityState) handleRevertingToPrimaryOnUpdate(serverConfig *clients.ServerConfig) {
	if a.activeXDSChannel != nil && a.activeXDSChannel.serverConfig.Equal(serverConfig) {
		// If the resource update is from the current active server, nothing
		// needs to be done from fallback point of view.
		return
	}

	if a.logger.V(2) {
		a.logger.Infof("Received update from non-active server %q", serverConfig)
	}

	// If the resource update is not from the current active server, it means
	// that we have received an update from a higher priority server and we need
	// to revert back to it. This method guarantees that when an update is
	// received from a server, all lower priority servers are closed.
	serverIdx := a.serverIndexForConfig(serverConfig)
	if serverIdx == len(a.xdsChannelConfigs) {
		// This can never happen.
		a.logger.Errorf("Received update from an unknown server: %s", serverConfig)
		return
	}
	a.activeXDSChannel = a.xdsChannelConfigs[serverIdx]

	// Close all lower priority channels.
	//
	// But before closing any channel, we need to unsubscribe from any resources
	// that were subscribed to on this channel. Resources could be subscribed to
	// from multiple channels as we fallback to lower priority servers. But when
	// a higher priority one comes back up, we need to unsubscribe from all
	// lower priority ones before releasing the reference to them.
	for i := serverIdx + 1; i < len(a.xdsChannelConfigs); i++ {
		cfg := a.xdsChannelConfigs[i]

		for rType, rState := range a.resources {
			for resourceName, state := range rState {
				for xcc := range state.xdsChannelConfigs {
					if xcc != cfg {
						continue
					}
					// If the current resource is subscribed to on this channel,
					// unsubscribe, and remove the channel from the list of
					// channels that this resource is subscribed to.
					xcc.channel.unsubscribe(rType, resourceName)
					delete(state.xdsChannelConfigs, xcc)
				}
			}
		}

		// Release the reference to the channel.
		if cfg.cleanup != nil {
			if a.logger.V(2) {
				a.logger.Infof("Closing lower priority server %q", cfg.serverConfig)
			}
			cfg.cleanup()
			cfg.cleanup = nil
		}
		cfg.channel = nil
	}
}

// watchResource registers a new watcher for the specified resource type and
// name. It returns a function that can be called to cancel the watch.
//
// If this is the first watch for any resource on this authority, an xdsChannel
// to the first management server (from the list of server configurations) will
// be created.
//
// If this is the first watch for the given resource name, it will subscribe to
// the resource with the xdsChannel. If a cached copy of the resource exists, it
// will immediately notify the new watcher. When the last watcher for a resource
// is removed, it will unsubscribe the resource from the xdsChannel.
func (a *authorityState) watchResource(rType ResourceType, resourceName string, watcher ResourceWatcher) func() {
	cleanup := func() {}
	done := make(chan struct{})

	a.xdsClientSerializer.ScheduleOr(func(context.Context) {
		defer close(done)

		if a.logger.V(2) {
			a.logger.Infof("New watch for type %q, resource name %q", rType.TypeName(), resourceName)
		}

		xdsChannel := a.xdsChannelToUse()
		if xdsChannel == nil {
			return
		}

		// Lookup the entry for the resource type in the top-level map. If there is
		// no entry for this resource type, create one.
		resources := a.resources[rType]
		if resources == nil {
			resources = make(map[string]*resourceState)
			a.resources[rType] = resources
		}

		// Lookup the resource state for the particular resource name that the watch
		// is being registered for. If this is the first watch for this resource
		// name, request it from the management server.
		state := resources[resourceName]
		if state == nil {
			if a.logger.V(2) {
				a.logger.Infof("First watch for type %q, resource name %q", rType.TypeName(), resourceName)
			}
			state = &resourceState{
				watchers:          make(map[ResourceWatcher]bool),
				md:                updateMetadata{status: serviceStatusRequested},
				xdsChannelConfigs: map[*xdsChannelWithConfig]bool{xdsChannel: true},
			}
			resources[resourceName] = state
			xdsChannel.channel.subscribe(rType, resourceName)
		}
		// Always add the new watcher to the set of watchers.
		state.watchers[watcher] = true

		// If we have a cached copy of the resource, notify the new watcher
		// immediately.
		if state.cache != nil {
			if a.logger.V(2) {
				a.logger.Infof("Resource type %q with resource name %q found in cache: %v", rType.TypeName(), resourceName, state.cache)
			}
			resource := state.cache
			a.watcherCallbackSerializer.TrySchedule(func(context.Context) { watcher.OnResourceChanged(resource, nil, func() {}) })
		}
		// If last update was NACK'd, notify the new watcher of error
		// immediately as well.
		if state.md.status == serviceStatusNACKed {
			if a.logger.V(2) {
				a.logger.Infof("Resource type %q with resource name %q was NACKed: %v", rType.TypeName(), resourceName, state.cache)
			}
			a.watcherCallbackSerializer.TrySchedule(func(context.Context) { watcher.OnAmbientError(state.md.errState.err, func() {}) })
		}
		// If the metadata field is updated to indicate that the management
		// server does not have this resource, notify the new watcher.
		if state.md.status == serviceStatusNotExist {
			a.watcherCallbackSerializer.TrySchedule(func(context.Context) {
				watcher.OnResourceChanged(nil, xdsresource.NewErrorf(xdsresource.ErrorTypeResourceNotFound, "resource not found in received response"), func() {})
			})
		}
		cleanup = a.unwatchResource(rType, resourceName, watcher)
	}, func() {
		if a.logger.V(2) {
			a.logger.Infof("Failed to schedule a watch for type %q, resource name %q, because the xDS client is closed", rType.TypeName(), resourceName)
		}
		close(done)
	})
	<-done
	return cleanup
}

func (a *authorityState) unwatchResource(rType ResourceType, resourceName string, watcher ResourceWatcher) func() {
	return grpcsync.OnceFunc(func() {
		done := make(chan struct{})
		a.xdsClientSerializer.ScheduleOr(func(context.Context) {
			defer close(done)

			if a.logger.V(2) {
				a.logger.Infof("Canceling a watch for type %q, resource name %q", rType.TypeName(), resourceName)
			}

			// Lookup the resource type from the resource cache. The entry is
			// guaranteed to be present, since *we* were the ones who added it in
			// there when the watch was registered.
			resources := a.resources[rType]
			state := resources[resourceName]

			// Delete this particular watcher from the list of watchers, so that its
			// callback will not be invoked in the future.
			delete(state.watchers, watcher)
			if len(state.watchers) > 0 {
				if a.logger.V(2) {
					a.logger.Infof("%d more watchers exist for type %q, resource name %q", rType.TypeName(), resourceName)
				}
				return
			}

			// There are no more watchers for this resource. Unsubscribe this
			// resource from all channels where it was subscribed to and delete
			// the state associated with it.
			if a.logger.V(2) {
				a.logger.Infof("Removing last watch for resource name %q", resourceName)
			}
			for xcc := range state.xdsChannelConfigs {
				xcc.channel.unsubscribe(rType, resourceName)
			}
			delete(resources, resourceName)

			// If there are no more watchers for this resource type, delete the
			// resource type from the top-level map.
			if len(resources) == 0 {
				if a.logger.V(2) {
					a.logger.Infof("Removing last watch for resource type %q", rType.TypeName())
				}
				delete(a.resources, rType)
			}
			// If there are no more watchers for any resource type, release the
			// reference to the xdsChannels.
			if len(a.resources) == 0 {
				if a.logger.V(2) {
					a.logger.Infof("Removing last watch for for any resource type, releasing reference to the xdsChannel")
				}
				a.closeXDSChannels()
			}
		}, func() { close(done) })
		<-done
	})
}

// xdsChannelToUse returns the xdsChannel to use for communicating with the
// management server. If an active channel is available, it returns that.
// Otherwise, it creates a new channel using the first server configuration in
// the list of configurations, and returns that.
//
// Only executed in the context of a serializer callback.
func (a *authorityState) xdsChannelToUse() *xdsChannelWithConfig {
	if a.activeXDSChannel != nil {
		return a.activeXDSChannel
	}

	sc := a.xdsChannelConfigs[0].serverConfig
	xc, cleanup, err := a.getChannelForADS(sc, a)
	if err != nil {
		a.logger.Warningf("Failed to create xDS channel: %v", err)
		return nil
	}
	a.xdsChannelConfigs[0].channel = xc
	a.xdsChannelConfigs[0].cleanup = cleanup
	a.activeXDSChannel = a.xdsChannelConfigs[0]
	return a.activeXDSChannel
}

// closeXDSChannels closes all the xDS channels associated with this authority,
// when there are no more watchers for any resource type.
//
// Only executed in the context of a serializer callback.
func (a *authorityState) closeXDSChannels() {
	for _, xcc := range a.xdsChannelConfigs {
		if xcc.cleanup != nil {
			xcc.cleanup()
			xcc.cleanup = nil
		}
		xcc.channel = nil
	}
	a.activeXDSChannel = nil
}

// watcherExistsForUncachedResource returns true if there is at least one
// watcher for a resource that has not yet been cached.
//
// Only executed in the context of a serializer callback.
func (a *authorityState) watcherExistsForUncachedResource() bool {
	for _, resourceStates := range a.resources {
		for _, state := range resourceStates {
			if state.md.status == serviceStatusRequested {
				return true
			}
		}
	}
	return false
}

// dumpResources returns a dump of the resource configuration cached by this
// authority, for CSDS purposes.
func (a *authorityState) dumpResources() []*v3statuspb.ClientConfig_GenericXdsConfig {
	var ret []*v3statuspb.ClientConfig_GenericXdsConfig
	done := make(chan struct{})

	a.xdsClientSerializer.ScheduleOr(func(context.Context) {
		defer close(done)
		ret = a.resourceConfig()
	}, func() { close(done) })
	<-done
	return ret
}

// resourceConfig returns a slice of GenericXdsConfig objects representing the
// current state of all resources managed by this authority. This is used for
// reporting the current state of the xDS client.
//
// Only executed in the context of a serializer callback.
func (a *authorityState) resourceConfig() []*v3statuspb.ClientConfig_GenericXdsConfig {
	var ret []*v3statuspb.ClientConfig_GenericXdsConfig
	for rType, resourceStates := range a.resources {
		typeURL := rType.TypeURL()
		for name, state := range resourceStates {
			var raw *anypb.Any
			if state.cache != nil {
				raw = state.cache.Raw()
			}
			config := &v3statuspb.ClientConfig_GenericXdsConfig{
				TypeUrl:      typeURL,
				Name:         name,
				VersionInfo:  state.md.version,
				XdsConfig:    raw,
				LastUpdated:  timestamppb.New(state.md.timestamp),
				ClientStatus: serviceStatusToProto(state.md.status),
			}
			if errState := state.md.errState; errState != nil {
				config.ErrorState = &v3adminpb.UpdateFailureState{
					LastUpdateAttempt: timestamppb.New(errState.timestamp),
					Details:           errState.err.Error(),
					VersionInfo:       errState.version,
				}
			}
			ret = append(ret, config)
		}
	}
	return ret
}

func (a *authorityState) close() {
	a.xdsClientSerializerClose()
	<-a.xdsClientSerializer.Done()
	if a.logger.V(2) {
		a.logger.Infof("Closed")
	}
}

func serviceStatusToProto(serviceStatus serviceStatus) v3adminpb.ClientResourceStatus {
	switch serviceStatus {
	case serviceStatusUnknown:
		return v3adminpb.ClientResourceStatus_UNKNOWN
	case serviceStatusRequested:
		return v3adminpb.ClientResourceStatus_REQUESTED
	case serviceStatusNotExist:
		return v3adminpb.ClientResourceStatus_DOES_NOT_EXIST
	case serviceStatusACKed:
		return v3adminpb.ClientResourceStatus_ACKED
	case serviceStatusNACKed:
		return v3adminpb.ClientResourceStatus_NACKED
	default:
		return v3adminpb.ClientResourceStatus_UNKNOWN
	}
}
