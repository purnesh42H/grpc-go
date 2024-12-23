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

// OnResourceProcessed() is a function to be invoked by watcher implementations upon
// completing the processing of a callback from the xDS client. Failure to
// invoke this callback prevents the xDS client from reading further messages
// from the xDS server.
type OnResourceProcessed func()

// ResourceWatcher is an interface that can to be implemented
// to wrap the callbacks to be invoked for different events corresponding
// to the resource being watched.
type ResourceWatcher interface {
	// OnResourceChanged is invoked notify the watcher of a new version of the
	// resource received from the xDS server or an error indicating the reason
	// why the resource cannot be obtained.
	//
	// The ResourceData parameter needs to be type asserted to the appropriate
	// type for the resource being watched. In case of error, th ResourceData
	// is nil otherwise its not nil and error is nil but both will never be nil
	// together.
	OnResourceChanged(ResourceData, error, OnResourceProcessed)

	// OnAmbientError is invoked to notify the watcher of an error that occurs after a
	// resource has been received that should not modify the
	// watcher's use of that resource but that may be useful information about
	// the ambient state of the XdsClient. In particular, the watcher should
	// NOT stop using the previously seen resource, and the XdsClient will NOT
	// remove the resource from its cache. However, the error message may be
	// useful as additional context to include in errors that are being
	// generated for other reasons.
	//
	// It is invoked under different error conditions including but not
	// limited to the following:
	//      - authority mentioned in the resource is not found
	//      - resource name parsing error
	//      - resource deserialization error
	//      - resource validation error
	//      - ADS stream failure
	//      - connection failure
	OnAmbientError(error, OnResourceProcessed)
}
