package internal

import (
	"errors"
	"sync"

	"go.opentelemetry.io/otel/propagation"
	"google.golang.org/grpc/metadata"
)

// CustomCarrier wraps propagation.TextMapCarrier and metadata.MD
// and supports both text and binary data.
type CustomCarrier struct {
	propagation.TextMapCarrier
	md  metadata.MD
	mtx sync.RWMutex
}

// NewCustomCarrier creates a new CustomCarrier with
// embedded propagation.TextMapCarrier and metadata.MD.
func NewCustomCarrier(carrier propagation.TextMapCarrier, md metadata.MD) *CustomCarrier {
	return &CustomCarrier{
		TextMapCarrier: carrier,
		md:             md,
	}
}

// Get retrieves the value associated with the given key as a string.
func (c *CustomCarrier) Get(key string) string {
	c.mtx.RLock()
	defer c.mtx.RUnlock()

	return c.TextMapCarrier.Get(key)
}

// Set sets the value for the given key as a string.
func (c *CustomCarrier) Set(key, value string) {
	c.mtx.Lock()
	defer c.mtx.Unlock()

	c.TextMapCarrier.Set(key, value)
}

// SetBinary sets the binary value for the given key in the metadata.
func (c *CustomCarrier) SetBinary(key string, value []byte) {
	c.mtx.Lock()
	defer c.mtx.Unlock()

	// Set the raw binary value in the metadata using the -bin suffix
	c.md[key+"-bin"] = []string{string(value)}
}

// GetBinary retrieves the binary value associated with the given key from the metadata.
func (c *CustomCarrier) GetBinary(key string) ([]byte, error) {
	c.mtx.RLock()
	defer c.mtx.RUnlock()

	// Retrieve the binary data directly from metadata
	values := c.md[key+"-bin"]
	if len(values) == 0 {
		return nil, errors.New("key not found")
	}

	return []byte(values[0]), nil
}
