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

// A LoadStore aggregates loads for multiple clusters and services that are
// intended to be reported via LRS.
//
// LoadStore stores loads reported to a single LRS server. Use multiple stores
// for multiple servers.
//
// It is safe for concurrent use.
type LoadStore struct {
}

// Stop stops the LoadStore's load reporting stream.
func (ls *LoadStore) Stop() error {
	panic("unimplemented")
}

// ReporterForCluster returns the PerClusterReporter for the given cluster and
// service.
func (ls *LoadStore) ReporterForCluster(clusterName, serviceName string) PerClusterReporter {
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
