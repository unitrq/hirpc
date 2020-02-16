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

// net/http handler interface implementation

package hirpc

import (
	"fmt"
	"net/http"
	"sync"

	"golang.org/x/sync/semaphore"
)

// DefaultBatchLimit - 1
const DefaultBatchLimit = 1

// HTTPCodec - translates http request into set of marked requests and writes one or many results into response stream
type HTTPCodec interface {
	DecodeRequest(*http.Request) (map[string]CallRequest, error)
	EncodeResponse(http.ResponseWriter, *string, CallResult)
	EncodeResponses(http.ResponseWriter, map[string]CallResult)
}

// HTTPHandler - http request handler
type HTTPHandler struct {
	ep    *Endpoint // endpoint used to dispatch call requests
	codec HTTPCodec // transport protocol request and response codec
	batch int       // max number of concurrently running calls, 0 forces sequential execution
}

// semaphore - used to bound batch execution concurrency or to order calls sequentially
func (h *HTTPHandler) semaphore() *semaphore.Weighted {
	size := h.batch
	if size < 1 {
		size = 1
	}
	return semaphore.NewWeighted(int64(size))
}

// ServeHTTP - decode request body into multiple call requests and execute them sequentially or concurrently
func (h *HTTPHandler) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	requests, err := h.codec.DecodeRequest(r)
	if err != nil {
		h.codec.EncodeResponse(w, nil, CallResult{err})
		return
	}
	l := len(requests)
	ctx := r.Context()
	sem := h.semaphore()          // concurrent call execution semaphore
	calls := make([]func(), 0, l) // resolved method calls
	results := make(map[string]CallResult, l)
	wg := sync.WaitGroup{}
	// resolve requests into method calls via endpoint
	for i, c := range requests {
		id := i
		mc, err := h.ep.Dispatch(c)
		if err != nil {
			results[id] = CallResult{err}
			continue
		}
		calls = append(calls, func() {
			defer sem.Release(1)
			defer wg.Done()
			if ctx.Err() != nil {
				return
			}
			results[id] = mc.Sync(ctx)
		})
	}
	// limit call concurrency with semaphore
	for _, call := range calls {
		if err := sem.Acquire(ctx, 1); err != nil {
			h.codec.EncodeResponse(w, nil, CallResult{err})
			return
		}
		wg.Add(1)
		go call()
	}
	done := make(chan struct{})
	go func() {
		defer close(done)
		wg.Wait()
	}()
	select {
	case <-ctx.Done():
		h.codec.EncodeResponse(w, nil, CallResult{ctx.Err()})
		return
	case <-done:
		break
	}
	h.codec.EncodeResponses(w, results)
}

// NewHTTPHandler - create new http handler using specified codec
func (ep *Endpoint) NewHTTPHandler(codec HTTPCodec, batch int) (*HTTPHandler, error) {
	if codec == nil {
		return nil, fmt.Errorf("codec is nil")
	}
	return &HTTPHandler{ep, codec, batch}, nil
}
