/*
 *
 * Copyright 2020 gRPC authors.
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

package xdsresource

import (
	"errors"
	"fmt"
	"strings"
)

// ErrorType is the type of the error that the watcher will receive from the xds
// client.
type ErrorType int

const (
	// ErrorTypeUnknown indicates the error doesn't have a specific type. It is
	// the default value, and is returned if the error is not an xds error.
	ErrorTypeUnknown ErrorType = iota
	// ErrorTypeConnection indicates a connection error from the gRPC client.
	ErrorTypeConnection
	// ErrorTypeResourceNotFound indicates a resource is not found from the xds
	// response. It's typically returned if the resource is removed in the xds
	// server.
	ErrorTypeResourceNotFound
	// ErrorTypeResourceTypeUnsupported indicates the receipt of a message from
	// the management server with resources of an unsupported resource type.
	ErrorTypeResourceTypeUnsupported
	// ErrTypeStreamFailedAfterRecv indicates an ADS stream error, after
	// successful receipt of at least one message from the server.
	ErrTypeStreamFailedAfterRecv
	// ErrorTypeNACKed indicates that configuration provided by the xDS management
	// server was NACKed.
	ErrorTypeNACKed
)

type xdsClientError struct {
	t    ErrorType
	desc string
}

func (e *xdsClientError) Error() string {
	return e.desc
}

// NewErrorf creates an xDS client error. The callbacks are called with this
// error, to pass additional information about the error.
func NewErrorf(t ErrorType, format string, args ...any) error {
	return &xdsClientError{t: t, desc: fmt.Sprintf(format, args...)}
}

// NewError creates an xDS client error. The callbacks are called with this
// error, to pass additional information about the error.
func NewError(t ErrorType, message string) error {
	return NewErrorf(t, "%s", message)
}

// NewErrorFromMessage creates an xDS client error from the message based on
// keywords potentially present in the error string.
//
// Warning: Relying on string content to determine error types is brittle and
// generally discouraged. Error messages can change, breaking this logic.
// Prefer using type assertions or checking specific error variables where possible.
func NewErrorFromMessage(message string) error {
	// Define keywords or phrases associated with each error type.
	// These are just examples and might need adjustment based on actual error messages.
	switch {
	case strings.Contains(message, "connection error"):
		return &xdsClientError{t: ErrorTypeConnection, desc: message}
	case strings.Contains(message, "resource not found"), strings.Contains(message, "does not exist"):
		return &xdsClientError{t: ErrorTypeResourceNotFound, desc: message}
	case strings.Contains(message, "unsupported resource type"):
		return &xdsClientError{t: ErrorTypeResourceTypeUnsupported, desc: message}
	case strings.Contains(message, "stream failed"): // This might be too generic
		return &xdsClientError{t: ErrTypeStreamFailedAfterRecv, desc: message}
	case strings.Contains(message, "NACKed"), strings.Contains(message, "invalid"): // "invalid" might be too generic
		return &xdsClientError{t: ErrorTypeNACKed, desc: message}
	default:
		// If no specific keyword is found, default to Unknown.
		return &xdsClientError{t: ErrorTypeUnknown, desc: message}
	}
}

// ErrType returns the error's type.
func ErrType(err error) ErrorType {
	var xe *xdsClientError
	if errors.As(err, &xe) {
		return xe.t
	}
	return ErrorTypeUnknown
}
