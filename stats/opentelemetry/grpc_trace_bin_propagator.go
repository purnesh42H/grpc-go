package opentelemetry

import (
	"context"
	"encoding/base64"

	"go.opentelemetry.io/otel/propagation"
	"go.opentelemetry.io/otel/trace"
	"google.golang.org/grpc/stats/opentelemetry/internal"
)

// CustomBinaryPropagator is a TextMapPropagator that propagates SpanContext in binary form.
type GrpcTraceBinPropagator struct{}

// Inject inserts the trace context into the carrier as a binary header.
func (p GrpcTraceBinPropagator) Inject(ctx context.Context, carrier propagation.TextMapCarrier) {
	span := trace.SpanFromContext(ctx)
	if !span.SpanContext().IsValid() {
		return
	}

	binaryData := Binary(span.SpanContext())
	if binaryData == nil {
		return
	}

	if customCarrier, ok := carrier.(*internal.CustomCarrier); ok {
		customCarrier.SetBinary("grpc-trace-bin", binaryData) // fast path: set the binary data without encoding
	} else {
		carrier.Set("grpc-trace-bin", base64.StdEncoding.EncodeToString(binaryData)) // slow path: set the binary data with encoding
	}
}

// Extract decodes the binary data from the carrier and reconstructs the SpanContext.
func (p GrpcTraceBinPropagator) Extract(ctx context.Context, carrier propagation.TextMapCarrier) context.Context {
	var binaryData []byte

	if customCarrier, ok := carrier.(*internal.CustomCarrier); ok {
		binaryData, _ = customCarrier.GetBinary("grpc-trace-bin")
	} else {
		binaryData, _ = base64.StdEncoding.DecodeString(carrier.Get("grpc-trace-bin"))
	}
	if binaryData == nil {
		return ctx
	}

	spanContext, ok := FromBinary([]byte(binaryData))
	if !ok {
		return ctx
	}

	return trace.ContextWithRemoteSpanContext(ctx, spanContext)
}

// Fields returns the list of fields that are used by the propagator.
func (p GrpcTraceBinPropagator) Fields() []string {
	return []string{"grpc-trace-bin"}
}

// Binary returns the binary format representation of a SpanContext.
//
// If sc is the zero value, Binary returns nil.
func Binary(sc trace.SpanContext) []byte {
	if sc.Equal(trace.SpanContext{}) {
		return nil
	}
	var b [29]byte
	traceID, _ := sc.TraceID().MarshalJSON()
	copy(b[2:18], traceID[:])
	b[18] = 1
	spanID, _ := sc.SpanID().MarshalJSON()
	copy(b[19:27], spanID[:])
	b[27] = 2
	b[28] = uint8(sc.TraceFlags())
	return b[:]
}

// FromBinary returns the SpanContext represented by b.
//
// If b has an unsupported version ID or contains no TraceID, FromBinary
// returns with ok==false.
func FromBinary(b []byte) (sc trace.SpanContext, ok bool) {
	if len(b) == 0 || b[0] != 0 {
		return trace.SpanContext{}, false
	}
	b = b[1:]
	if len(b) >= 17 && b[0] == 0 {
		traceID, _ := sc.TraceID().MarshalJSON()
		copy(traceID[:], b[1:17])
		b = b[17:]
	} else {
		return trace.SpanContext{}, false
	}
	if len(b) >= 9 && b[0] == 1 {
		spanID, _ := sc.SpanID().MarshalJSON()
		copy(spanID[:], b[1:9])
		b = b[9:]
	}
	if len(b) >= 2 && b[0] == 2 {
		sc = sc.WithTraceFlags(sc.TraceFlags())
	}
	return sc, true
}
