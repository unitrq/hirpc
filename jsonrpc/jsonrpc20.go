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

// jsonrpc - JSON-RPC 2.0 compatible codec for hirpc http request handler

package jsonrpc

import (
	"encoding/json"
	"io"
	"io/ioutil"
	"math/rand"
	"net/http"
	"strings"
	"time"

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

	jsonContentType = "application/json; charset=utf-8"                                // Content-Type header value
	maxBodySize     = 2 * 1024 * 1024                                                  // max allowed body size in bytes
	charset         = "abcdefghijklmnopqrstuvwxyzABCDEFGHIJKLMNOPQRSTUVWXYZ0123456789" // random string generator charset
)

// random string generator - used to regenerate missing or repeated call ids
type rndc struct {
	charset string
	len     int
	sr      *rand.Rand
}

// preseed random string generator
var srnd = &rndc{charset, len(charset), rand.New(rand.NewSource(time.Now().UnixNano()))}

// DefaultCodec - default global codec instance
var DefaultCodec = &Codec{}

// new - generate new random string of given length from charset
func (r *rndc) new(length int) string {
	b := make([]byte, length)
	for i := range b {
		b[i] = r.charset[r.sr.Intn(r.len)]
	}
	return string(b)
}

// Codec - JSON-RPC 2.0 compatible http request codec
type Codec struct {
}

// Request - procedure call request object
type Request struct {
	ID      *string          `json:"id"`
	Method  string           `json:"method"`
	Version string           `json:"jsonrpc"`
	Params  *json.RawMessage `json:"params"`
}

// Response - procedure call response object
type Response struct {
	ID      *string     `json:"id"`
	Version string      `json:"jsonrpc"`
	Result  interface{} `json:"result,omitempty"`
	Error   *Error      `json:"error,omitempty"`
}

// Error - error description object
type Error struct {
	Code    int         `json:"code"`
	Message string      `json:"message"`
	Data    interface{} `json:"data,omitempty"`
}

// NewError - new JSON-RPC error value constructor
func NewError(code int, msg string) *Error {
	return &Error{
		Code:    code,
		Message: msg,
	}
}

// Error - error interface implementation
func (e *Error) Error() string {
	return e.Message
}

// ServiceMethod - return service and method names respectively
func (r *Request) ServiceMethod() (string, string) {
	sp := strings.Split(r.Method, ".")
	if len(sp) == 2 {
		return sp[0], sp[1]
	}
	return "", r.Method
}

// Parameter - decode parameter into value or return error
func (r *Request) Parameter(val interface{}) error {
	return json.Unmarshal(*r.Params, val)
}

// DecodeRequest - decodes POST request body into one or more JSON-RPC 2.0 method calls
func (c *Codec) DecodeRequest(r *http.Request) (map[string]hirpc.CallRequest, error) {
	if r.Method != "POST" {
		return nil, NewError(EInvalidRequest, "method not allowed")
	}
	body, err := ioutil.ReadAll(io.LimitReader(r.Body, maxBodySize))
	r.Body.Close()
	if err != nil {
		return nil, NewError(EParseError, err.Error())
	}
	// try to unmarshal as batch array first, decoder will fail early on non-array
	calls := []*Request{}
	if err := json.Unmarshal(body, &calls); err != nil {
		call := &Request{}
		if err := json.Unmarshal(body, call); err != nil {
			return nil, NewError(EParseError, "failed to unmarshal request body")
		}
		calls = append(calls, call)
	}
	res := make(map[string]hirpc.CallRequest, len(calls))
	for _, call := range calls {
		if call.ID == nil || len(*call.ID) == 0 {
			id := srnd.new(8)
			call.ID = &id
		}
		for {
			if _, ok := res[*call.ID]; !ok {
				break
			}
			id := srnd.new(8)
			call.ID = &id
		}
		res[*call.ID] = call
	}
	return res, nil
}

// callResponse - single call response constructor
func callResponse(id *string, cr hirpc.CallResult) *Response {
	response := &Response{
		ID:      id,
		Version: "2.0",
	}
	result, err := cr.Result()
	if err != nil {
		response.Error = NewError(EServerErrorMin, err.Error())
	} else {
		response.Result = result
	}
	return response
}

// EncodeResponse - encodes one CallResult into JSON-RPC 2.0 response
func (c *Codec) EncodeResponse(w http.ResponseWriter, id *string, result hirpc.CallResult) {
	w.Header().Set("Content-Type", jsonContentType)
	w.WriteHeader(200)
	response := callResponse(id, result)
	json.NewEncoder(w).Encode(response)
}

// EncodeResponses - encodes multiple CallResult's into JSON-RPC 2.0 response
func (c *Codec) EncodeResponses(w http.ResponseWriter, results map[string]hirpc.CallResult) {
	if len(results) < 2 {
		for id, r := range results {
			v := id
			c.EncodeResponse(w, &v, r)
		}
		return
	}
	w.Header().Set("Content-Type", jsonContentType)
	w.WriteHeader(200)
	enc := json.NewEncoder(w)
	res := make([]*Response, 0, len(results))
	for id, r := range results {
		i := id
		res = append(res, callResponse(&i, r))
	}
	enc.Encode(res)
}
