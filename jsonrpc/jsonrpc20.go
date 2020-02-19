// MIT License
//
// Copyright (c) 2020  Alexander Vladimirov
//
// Permission is hereby granted, free of charge, to any person obtaining a copy
// of this software and associated documentation files (the "Software"), to deal
// in the Software without restriction, including without limitation the rights
// to use, copy, modify, merge, publish, distribute, sublicense, and/or sell
// copies of the Software, and to permit persons to whom the Software is
// furnished to do so, subject to the following conditions:
//
// The above copyright notice and this permission notice shall be included in all
// copies or substantial portions of the Software.
//
// THE SOFTWARE IS PROVIDED "AS IS", WITHOUT WARRANTY OF ANY KIND, EXPRESS OR
// IMPLIED, INCLUDING BUT NOT LIMITED TO THE WARRANTIES OF MERCHANTABILITY,
// FITNESS FOR A PARTICULAR PURPOSE AND NONINFRINGEMENT. IN NO EVENT SHALL THE
// AUTHORS OR COPYRIGHT HOLDERS BE LIABLE FOR ANY CLAIM, DAMAGES OR OTHER
// LIABILITY, WHETHER IN AN ACTION OF CONTRACT, TORT OR OTHERWISE, ARISING FROM,
// OUT OF OR IN CONNECTION WITH THE SOFTWARE OR THE USE OR OTHER DEALINGS IN THE
// SOFTWARE.

// jsonrpc - provides JSON-RPC 2.0 spec compliant codec for hirpc.Endpoint request handler

package jsonrpc

import (
	"encoding/json"
	"fmt"
	"io"
	"io/ioutil"
	"net/http"
	"strings"

	"github.com/unitrq/hirpc"
)

const (
	// EParseError - Invalid JSON was received by the server. An error occurred on the server while parsing the JSON text.
	EParseError = -32700
	// EInvalidRequest - The JSON sent is not a valid Request object.
	EInvalidRequest = -32600
	// EMethodNotFound - The method does not exist / is not available.
	EMethodNotFound = -32601
	// EInvalidParams - Invalid method parameter(s).
	EInvalidParams = -32602
	// EInternalError - Internal JSON-RPC error.
	EInternalError = -32603
	// EServerErrorMin - Implementation-defined server error min code
	EServerErrorMin = -32099
	// EServerErrorMax - Implementation-defined server error max code
	EServerErrorMax = -32000

	jsonContentType = "application/json; charset=utf-8" // Content-Type header value
	maxBodySize     = 2 * 1024 * 1024                   // max allowed body size in bytes
)

// Codec - default global codec instance
var Codec = &HTTPCodec{}

// HTTPCodec - JSON-RPC 2.0 spec compliant codec for hirpc.Endpoint request handler
type HTTPCodec struct{}

// Request - procedure call request object
type Request struct {
	ID      *json.RawMessage `json:"id"`
	Method  string           `json:"method"`
	Version string           `json:"jsonrpc"`
	Params  *json.RawMessage `json:"params"`
}

// Response - procedure call response object
type Response struct {
	ID      *json.RawMessage `json:"id"`
	Version string           `json:"jsonrpc"`
	Result  interface{}      `json:"result,omitempty"`
	Error   *Error           `json:"error,omitempty"`
}

// Error - error description object
type Error struct {
	Code    int         `json:"code"`
	Message string      `json:"message"`
	Data    interface{} `json:"data,omitempty"`
}

// callResponse - create call response constructor using request id
func callResponse(id *json.RawMessage, res interface{}, err error) *Response {
	response := &Response{
		ID:      id,
		Version: "2.0",
	}
	if err == nil {
		response.Result = res
	} else {
		response.Error = NewError(EServerErrorMax, err)
	}
	return response
}

// NewError - new JSON-RPC error value constructor
func NewError(code int, err error) *Error {
	if e, ok := err.(*Error); ok {
		return e
	}
	return &Error{
		Code:    code,
		Message: err.Error(),
	}
}

// Error - error interface implementation
func (e *Error) Error() string {
	return e.Message
}

// Target - return service and method names respectively
func (r *Request) Target() (string, string) {
	sp := strings.Split(r.Method, ".")
	if len(sp) == 2 {
		return sp[0], sp[1]
	}
	return "", r.Method
}

// Payload - decode parameter into value or return error
func (r *Request) Payload(val interface{}) error {
	return json.Unmarshal(*r.Params, val)
}

// Result - decode parameter into value or return error
func (r *Request) Result(v interface{}, err error) interface{} {
	if r.ID == nil {
		return nil
	}
	return callResponse(r.ID, v, err)
}

// DecodeRequest - decodes POST request body into one or more JSON-RPC 2.0 method calls
func (c *HTTPCodec) DecodeRequest(r *http.Request) ([]hirpc.CallRequest, error) {
	if r.Method != "POST" {
		return nil, NewError(EInvalidRequest, fmt.Errorf("method not allowed"))
	}
	body, err := ioutil.ReadAll(io.LimitReader(r.Body, maxBodySize))
	r.Body.Close()
	if err != nil {
		return nil, NewError(EParseError, err)
	}
	// try to unmarshal as batch array first, decoder will fail early on non-array
	requests := make([]*Request, 0)
	if err := json.Unmarshal(body, &requests); err != nil {
		call := &Request{}
		if err := json.Unmarshal(body, call); err != nil {
			return nil, NewError(EParseError, err)
		}
		return []hirpc.CallRequest{call}, nil
	}
	l := len(requests)
	if l == 0 {
		return nil, nil
	}
	calls := make([]hirpc.CallRequest, l)
	for i, r := range requests {
		calls[i] = r
	}
	return calls, nil
}

// EncodeError - encode error message into http response
func (c *HTTPCodec) EncodeError(w http.ResponseWriter, err error) {
	w.Header().Set("Content-Type", jsonContentType)
	w.WriteHeader(200)
	enc := json.NewEncoder(w)
	enc.Encode(callResponse(nil, nil, err))
}

// EncodeResults - encode multiple call results into http response
func (c *HTTPCodec) EncodeResults(w http.ResponseWriter, results ...interface{}) {
	w.Header().Set("Content-Type", jsonContentType)
	w.WriteHeader(200)
	enc := json.NewEncoder(w)
	switch len(results) {
	case 0:
		break
	case 1:
		enc.Encode(results[0])
	default:
		enc.Encode(results)
	}
}
