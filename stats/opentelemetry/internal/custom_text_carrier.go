package internal

import (
	"errors"

	"go.opentelemetry.io/otel/propagation"
	"google.golang.org/grpc/metadata"
)

// CustomCarrier wraps metadata.MD and supports
// both text and binary data.
type CustomCarrier struct {
	propagation.TextMapCarrier
	Md metadata.MD
}

// NewCustomCarrier creates a new CustomCarrier with
// embedded metadata.MD.
func NewCustomCarrier(md metadata.MD) CustomCarrier {
	return CustomCarrier{
		Md: md,
	}
}

// Get retrieves the value associated with the given key as a string.
func (c CustomCarrier) Get(key string) string {
	values := c.Md[key]
	if len(values) == 0 {
		return ""
	}

	return values[0]
}

// Set sets the value for the given key as a string.
// If key already exist, value will be ovewritten
func (c CustomCarrier) Set(key, value string) {
	c.Md.Set(key, value)
}

// SetBinary sets the binary value for the given key in the metadata.
// If key already exist, value will be ovewritten
func (c CustomCarrier) SetBinary(key string, value []byte) {
	// Only support 'grpc-trace-bin' binary header.
	if key == "grpc-trace-bin" {
		// Set the raw binary value in the metadata
		c.Md[key] = []string{string(value)}
	}
}

// GetBinary retrieves the binary value associated with the given key from the metadata.
func (c CustomCarrier) GetBinary(key string) ([]byte, error) {
	if key != "grpc-trace-bin" {
		return nil, errors.New("only support 'grpc-trace-bin' binary header")
	}

	// Retrieve the binary data directly from metadata
	values := c.Md[key]
	if len(values) == 0 {
		return nil, errors.New("key not found")
	}

	return []byte(values[0]), nil
}
