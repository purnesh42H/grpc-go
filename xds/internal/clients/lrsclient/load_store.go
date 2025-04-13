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

package lrsclient

import (
	"context"
	"time"
)

// A LoadStore aggregates loads for multiple clusters and services that are
// intended to be reported via LRS.
//
// LoadStore stores loads reported to a single LRS server. Use multiple stores
// for multiple servers.
//
// It is safe for concurrent use.
type LoadStore struct {
}

// Stop stops the LRS stream associated with this LoadStore.
//
// If this LoadStore is the only one using the underlying LRS stream, the
// stream will be closed. If other LoadStores are also using the same stream,
// the reference count to the stream is decremented, and the stream remains
// open until all LoadStores have called Stop().
//
// If this is the last LoadStore for the stream, this method makes a last
// attempt to flush any unreported load data to the LRS server. It will either
// wait for this attempt to complete, or for the provided context to be done
// before canceling the LRS stream.
func (ls *LoadStore) Stop(ctx context.Context) error {
	panic("unimplemented")
}

// ReporterForCluster returns the PerClusterReporter for the given cluster and
// service.
func (ls *LoadStore) ReporterForCluster(clusterName, serviceName string) PerClusterReporter {
	panic("unimplemented")
}

// stats returns the load data for the given cluster names. Data is returned in
// a slice with no specific order.
//
// If no clusterName is given (an empty slice), all data for all known clusters
// is returned.
//
// If a cluster's Data is empty (no load to report), it's not appended to the
// returned slice.
func (s *LoadStore) stats(clusterNames []string) []*loadData {
	panic("unimplemented")
}

// PerClusterReporter records load data pertaining to a single cluster. It
// provides methods to record call starts, finishes, server-reported loads,
// and dropped calls.
type PerClusterReporter struct {
}

// CallStarted records a call started in the LoadStore.
func (p *PerClusterReporter) CallStarted(locality string) {
	panic("unimplemented")
}

// CallFinished records a call finished in the LoadStore.
func (p *PerClusterReporter) CallFinished(locality string, err error) {
	panic("unimplemented")
}

// CallServerLoad records the server load in the LoadStore.
func (p *PerClusterReporter) CallServerLoad(locality, name string, val float64) {
	panic("unimplemented")
}

// CallDropped records a call dropped in the LoadStore.
func (p *PerClusterReporter) CallDropped(category string) {
	panic("unimplemented")
}

// loadData contains all load data reported to the LoadStore since the most recent
// call to stats().
type loadData struct {
	// cluster is the name of the cluster this data is for.
	cluster string
	// service is the name of the EDS service this data is for.
	service string
	// totalDrops is the total number of dropped requests.
	totalDrops uint64
	// drops is the number of dropped requests per category.
	drops map[string]uint64
	// localityStats contains load reports per locality.
	localityStats map[string]localityData
	// reportInternal is the duration since last time load was reported (stats()
	// was called).
	ReportInterval time.Duration
}

// localityData contains load data for a single locality.
type localityData struct {
	// RequestStats contains counts of requests made to the locality.
	RequestStats requestData
	// loadStats contains server load data for requests made to the locality,
	// indexed by the load type.
	loadStats map[string]serverLoadData
}

// requestData contains request counts.
type requestData struct {
	// succeeded is the number of succeeded requests.
	succeeded uint64
	// errored is the number of requests which ran into errors.
	errored uint64
	// inProgress is the number of requests in flight.
	inProgress uint64
	// issued is the total number requests that were sent.
	issued uint64
}

// serverLoadData contains server load data.
type serverLoadData struct {
	// count is the number of load reports.
	count uint64
	// sum is the total value of all load reports.
	sum float64
}
