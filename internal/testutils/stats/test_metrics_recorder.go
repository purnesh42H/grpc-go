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

// Package stats implements a TestMetricsRecorder utility.
package stats

import (
	"context"
	"sync"
	"testing"
	"time"

	"github.com/google/go-cmp/cmp"
	estats "google.golang.org/grpc/experimental/stats"
	"google.golang.org/grpc/internal/testutils"
	"google.golang.org/grpc/stats"
)

// TestMetricsRecorder is a MetricsRecorder to be used in tests. It sends
// recording events on channels and provides helpers to check if certain events
// have taken place. It also persists metrics data keyed on the metrics
// descriptor.
type TestMetricsRecorder struct {
	t *testing.T

	intCountCh   *testutils.Channel
	floatCountCh *testutils.Channel
	intHistoCh   *testutils.Channel
	floatHistoCh *testutils.Channel
	intGaugeCh   *testutils.Channel

	// The most recent update for each metric name.
	mu   sync.Mutex
	data map[estats.Metric]float64
}

// NewTestMetricsRecorder returns new TestMetricsRecorder
func NewTestMetricsRecorder(t *testing.T) *TestMetricsRecorder {
	tmr := &TestMetricsRecorder{
		t: t,

		intCountCh:   testutils.NewChannelWithSize(10),
		floatCountCh: testutils.NewChannelWithSize(10),
		intHistoCh:   testutils.NewChannelWithSize(10),
		floatHistoCh: testutils.NewChannelWithSize(10),
		intGaugeCh:   testutils.NewChannelWithSize(10),

		data: make(map[estats.Metric]float64),
	}

	return tmr
}

// AssertDataForMetric asserts data is present for metric. The zero value in the
// check is equivalent to unset.
func (r *TestMetricsRecorder) AssertDataForMetric(metricName string, wantVal float64) {
	r.mu.Lock()
	defer r.mu.Unlock()
	if r.data[estats.Metric(metricName)] != wantVal {
		r.t.Fatalf("Unexpected data for metric %v, got: %v, want: %v", metricName, r.data[estats.Metric(metricName)], wantVal)
	}
}

// PollForDataForMetric polls the metric data for the want. Fails if context
// provided expires before data for metric is found.
func (r *TestMetricsRecorder) PollForDataForMetric(ctx context.Context, metricName string, wantVal float64) {
	for ; ctx.Err() == nil; <-time.After(time.Millisecond) {
		r.mu.Lock()
		if r.data[estats.Metric(metricName)] == wantVal {
			r.mu.Unlock()
			return
		}
		r.mu.Unlock()
	}
	r.t.Fatalf("Timeout waiting for data %v for metric %v", wantVal, metricName)
}

// ClearMetrics clears the metrics data stores of the test metrics recorder by
// setting all the data to 0.
func (r *TestMetricsRecorder) ClearMetrics() {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.data = make(map[estats.Metric]float64)
}

// MetricsData represents data associated with a metric.
type MetricsData struct {
	Handle *estats.MetricDescriptor

	// Only set based on the type of metric. So only one of IntIncr or FloatIncr
	// is set.
	IntIncr   int64
	FloatIncr float64

	LabelKeys []string
	LabelVals []string
}

// WaitForInt64Count waits for an int64 count metric to be recorded and
// verifies that the recorded metrics data matches the expected
// metricsDataWant.
func (r *TestMetricsRecorder) WaitForInt64Count(ctx context.Context, metricsDataWant MetricsData) {
	got, err := r.intCountCh.Receive(ctx)
	if err != nil {
		r.t.Fatalf("timeout waiting for int64Count")
	}
	metricsDataGot := got.(MetricsData)
	if diff := cmp.Diff(metricsDataGot, metricsDataWant); diff != "" {
		r.t.Fatalf("int64count metricsData received unexpected value (-got, +want): %v", diff)
	}
}

// RecordInt64Count sends the metrics data to the intCountCh channel
// and updates the internal data map with the recorded value.
func (r *TestMetricsRecorder) RecordInt64Count(handle *estats.Int64CountHandle, incr int64, labels ...string) {
	r.intCountCh.Send(MetricsData{
		Handle:    handle.Descriptor(),
		IntIncr:   incr,
		LabelKeys: append(handle.Labels, handle.OptionalLabels...),
		LabelVals: labels,
	})

	r.mu.Lock()
	defer r.mu.Unlock()
	r.data[handle.Name] = float64(incr)
}

// WaitForFloat64Count waits for a float count metric to be recorded and
// verifies that the recorded metrics data matches the expected
// metricsDataWant.
func (r *TestMetricsRecorder) WaitForFloat64Count(ctx context.Context, metricsDataWant MetricsData) {
	got, err := r.floatCountCh.Receive(ctx)
	if err != nil {
		r.t.Fatalf("timeout waiting for float64Count")
	}
	metricsDataGot := got.(MetricsData)
	if diff := cmp.Diff(metricsDataGot, metricsDataWant); diff != "" {
		r.t.Fatalf("float64count metricsData received unexpected value (-got, +want): %v", diff)
	}
}

// RecordFloat64Count sends the metrics data to the floatCountCh channel
// and updates the internal data map with the recorded value.
func (r *TestMetricsRecorder) RecordFloat64Count(handle *estats.Float64CountHandle, incr float64, labels ...string) {
	r.floatCountCh.Send(MetricsData{
		Handle:    handle.Descriptor(),
		FloatIncr: incr,
		LabelKeys: append(handle.Labels, handle.OptionalLabels...),
		LabelVals: labels,
	})

	r.mu.Lock()
	defer r.mu.Unlock()
	r.data[handle.Name] = incr
}

// WaitForInt64Histo waits for an int histo metric to be recorded and
// verifies that the recorded metrics data matches the expected
// metricsDataWant.
func (r *TestMetricsRecorder) WaitForInt64Histo(ctx context.Context, metricsDataWant MetricsData) {
	got, err := r.intHistoCh.Receive(ctx)
	if err != nil {
		r.t.Fatalf("timeout waiting for int64Histo")
	}
	metricsDataGot := got.(MetricsData)
	if diff := cmp.Diff(metricsDataGot, metricsDataWant); diff != "" {
		r.t.Fatalf("int64Histo metricsData received unexpected value (-got, +want): %v", diff)
	}
}

// RecordInt64Histo sends the metrics data to the intHistoCh channel
// and updates the internal data map with the recorded value.
func (r *TestMetricsRecorder) RecordInt64Histo(handle *estats.Int64HistoHandle, incr int64, labels ...string) {
	r.intHistoCh.Send(MetricsData{
		Handle:    handle.Descriptor(),
		IntIncr:   incr,
		LabelKeys: append(handle.Labels, handle.OptionalLabels...),
		LabelVals: labels,
	})

	r.mu.Lock()
	defer r.mu.Unlock()
	r.data[handle.Name] = float64(incr)
}

// WaitForFloat64Histo waits for a float histo metric to be recorded and
// verifies that the recorded metrics data matches the expected
// metricsDataWant.
func (r *TestMetricsRecorder) WaitForFloat64Histo(ctx context.Context, metricsDataWant MetricsData) {
	got, err := r.floatHistoCh.Receive(ctx)
	if err != nil {
		r.t.Fatalf("timeout waiting for float64Histo")
	}
	metricsDataGot := got.(MetricsData)
	if diff := cmp.Diff(metricsDataGot, metricsDataWant); diff != "" {
		r.t.Fatalf("float64Histo metricsData received unexpected value (-got, +want): %v", diff)
	}
}

// RecordFloat64Histo sends the metrics data to the floatHistoCh channel
// and updates the internal data map with the recorded value.
func (r *TestMetricsRecorder) RecordFloat64Histo(handle *estats.Float64HistoHandle, incr float64, labels ...string) {
	r.floatHistoCh.Send(MetricsData{
		Handle:    handle.Descriptor(),
		FloatIncr: incr,
		LabelKeys: append(handle.Labels, handle.OptionalLabels...),
		LabelVals: labels,
	})

	r.mu.Lock()
	defer r.mu.Unlock()
	r.data[handle.Name] = incr
}

// WaitForInt64Gauge waits for a int gauge metric to be recorded and
// verifies that the recorded metrics data matches the expected
// metricsDataWant.
func (r *TestMetricsRecorder) WaitForInt64Gauge(ctx context.Context, metricsDataWant MetricsData) {
	got, err := r.intGaugeCh.Receive(ctx)
	if err != nil {
		r.t.Fatalf("timeout waiting for int64Gauge")
	}
	metricsDataGot := got.(MetricsData)
	if diff := cmp.Diff(metricsDataGot, metricsDataWant); diff != "" {
		r.t.Fatalf("int64Gauge metricsData received unexpected value (-got, +want): %v", diff)
	}
}

// RecordInt64Gauge sends the metrics data to the intGaugeCh channel
// and updates the internal data map with the recorded value.
func (r *TestMetricsRecorder) RecordInt64Gauge(handle *estats.Int64GaugeHandle, incr int64, labels ...string) {
	r.intGaugeCh.Send(MetricsData{
		Handle:    handle.Descriptor(),
		IntIncr:   incr,
		LabelKeys: append(handle.Labels, handle.OptionalLabels...),
		LabelVals: labels,
	})

	r.mu.Lock()
	defer r.mu.Unlock()
	r.data[handle.Name] = float64(incr)
}

// To implement a stats.Handler, which allows it to be set as a dial option:

// TagRPC is TestMetricsRecorder's implementation of TagRPC.
func (r *TestMetricsRecorder) TagRPC(ctx context.Context, _ *stats.RPCTagInfo) context.Context {
	return ctx
}

// HandleRPC is TestMetricsRecorder's implementation of HandleRPC.
func (r *TestMetricsRecorder) HandleRPC(context.Context, stats.RPCStats) {}

// TagConn is TestMetricsRecorder's implementation of TagConn.
func (r *TestMetricsRecorder) TagConn(ctx context.Context, _ *stats.ConnTagInfo) context.Context {
	return ctx
}

// HandleConn is TestMetricsRecorder's implementation of HandleConn.
func (r *TestMetricsRecorder) HandleConn(context.Context, stats.ConnStats) {}

// NoopMetricsRecorder is a noop MetricsRecorder to be used in tests to prevent
// nil panics.
type NoopMetricsRecorder struct{}

// RecordInt64Count noop implementation of RecordInt64Count.
func (r *NoopMetricsRecorder) RecordInt64Count(*estats.Int64CountHandle, int64, ...string) {}

// RecordFloat64Count is noop implementation of RecordFloat64Count.
func (r *NoopMetricsRecorder) RecordFloat64Count(*estats.Float64CountHandle, float64, ...string) {}

// RecordInt64Histo is noop implementation of RecordInt64Histo.
func (r *NoopMetricsRecorder) RecordInt64Histo(*estats.Int64HistoHandle, int64, ...string) {}

// RecordFloat64Histo is noop implementation of RecordFloat64Histo.
func (r *NoopMetricsRecorder) RecordFloat64Histo(*estats.Float64HistoHandle, float64, ...string) {}

// RecordInt64Gauge is noop implementation of RecordInt64Gauge.
func (r *NoopMetricsRecorder) RecordInt64Gauge(*estats.Int64GaugeHandle, int64, ...string) {}
