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

// Go net/http handler interface RPC library - this package provide simple RPC dynamic dispatcher
// and net/http compatible transport interface adapter for it.

package hirpc

import (
	"context"
	"fmt"
	"net/http"
	"reflect"
	"sync"
	"unicode"

	"golang.org/x/sync/semaphore"
)

// DefaultBatchLimit - order multiple method calls sequentially per request
const DefaultBatchLimit = 1

var (
	// reflect.Type of Context and error
	typeOfContext = reflect.TypeOf((*context.Context)(nil)).Elem()
	typeOfError   = reflect.TypeOf((*error)(nil)).Elem()
)

// CallHandler - method call handling function
type CallHandler func(context.Context) (interface{}, error)

// CallRequest - HTTPCodec single method call request object
type CallRequest interface {
	Target() (string, string)             // decode call request target service and method name
	Payload(interface{}) error            // decode call request parameter payload into specific type pointer
	Result(interface{}, error) CallResult // construct result object specific for this request and protocol
}

// CallResult - HTTPCodec method call protocol response object
type CallResult interface {
	GetResult() (interface{}, error)
}

// HTTPCodec - translates single http request into set of method call requests and set of call results or error into http response
type HTTPCodec interface {
	EncodeError(http.ResponseWriter, error)             // send http response representing single error message
	EncodeResults(http.ResponseWriter, ...CallResult)   // send http response representing one or more call results
	DecodeRequest(*http.Request) ([]CallRequest, error) // read and close http request body and decode one or more method call requests to execute, return (nil, nil) if request is valid but no calls was decoded
}

// MethodHandler - stores reflected method function reference and specific signature request/response types.
type MethodHandler struct {
	Meth    reflect.Method // method pointer
	ReqType reflect.Type   // signature parameter type
	ResType reflect.Type   // signature result type
}

// ServiceHandler - stores service instance, type, service-level middleware and method handlers collection.
type ServiceHandler struct {
	Name     string                                       // name used to reference service in registry namespace
	Methods  map[string]*MethodHandler                    // descriptors for methods with RPC handler compatible signature
	Inst     reflect.Value                                // pointer to service instance
	InstType reflect.Type                                 // type of service instance
	mw       []func(*MethodCall, CallHandler) CallHandler // service-specific call handlers
}

// MethodCall - captures context of single method call.
// Stores all objects required to invoke function reference produced by reflect package.
// Instead of executing dispatched request directly, Endpoint returns this frozen state object
// to allow calling side to precisely schedule execution of every single method call.
// This allows calling side to enforce required call execution scheduling policies for example to allow batching multiple
// method calls into single request/response roundtrip.
type MethodCall struct {
	Request CallRequest                                  // dispatch source provided by codec, used to construct codec response for this call
	SH      *ServiceHandler                              // service owning target method
	MH      *MethodHandler                               // target method handler
	Param   reflect.Value                                // deserialized parameter
	Result  reflect.Value                                // allocated result container
	mw      []func(*MethodCall, CallHandler) CallHandler // middlewares applied to this call
}

// Endpoint - RPC service registry
type Endpoint struct {
	mx       sync.RWMutex                                 // used for synchronized service (de-)registration and lookup
	services map[string]*ServiceHandler                   // registered services
	mw       []func(*MethodCall, CallHandler) CallHandler // globally applied middleware
	root     *ServiceHandler                              // this service is exposed as namespace root when dispatching method call if set (when service name == "")
	codec    HTTPCodec                                    // transport protocol request and response codec
	batch    int                                          // max number of concurrently running calls, 0 forces sequential execution
}

// invoke - invokes actual target method using call context
func (call *MethodCall) invoke(ctx context.Context) (interface{}, error) {
	errVal := call.MH.Meth.Func.Call([]reflect.Value{
		call.SH.Inst,
		reflect.ValueOf(ctx),
		call.Param,
		call.Result,
	})
	if eIf := errVal[0].Interface(); eIf != nil {
		return nil, eIf.(error)
	}
	return call.Result.Interface(), nil
}

// Invoke - block until target method returns
func (call *MethodCall) Invoke(ctx context.Context) (interface{}, error) {
	handler := call.invoke
	for _, mw := range call.mw {
		handler = mw(call, handler)
	}
	return handler(ctx)
}

// isCapitalized - returns true if string starts with uppercase
func isCapitalized(s string) bool {
	return s != "" && unicode.IsUpper(rune(s[0]))
}

// isExportedOrBuiltin returns true if a type is exported or a builtin.
func isExportedOrBuiltin(t reflect.Type) bool {
	for t.Kind() == reflect.Ptr {
		t = t.Elem()
	}
	return isCapitalized(t.Name()) || t.PkgPath() == ""
}

// NewEndpoint - create new RPC endpoint
func NewEndpoint(codec HTTPCodec, limit int, mw ...func(*MethodCall, CallHandler) CallHandler) *Endpoint {
	if codec == nil {
		return nil
	}
	if limit < 1 {
		limit = 1
	}
	ep := &Endpoint{
		services: make(map[string]*ServiceHandler),
		codec:    codec,
		batch:    limit,
		mw:       mw[:],
	}
	return ep
}

// Services - get service map copy
func (ep *Endpoint) Services() map[string]*ServiceHandler {
	ep.mx.RLock()
	defer ep.mx.RUnlock()
	services := make(map[string]*ServiceHandler, len(ep.services))
	for k, v := range ep.services {
		services[k] = v
	}
	return services
}

// Use - append new middlewares to global middleware list
func (ep *Endpoint) Use(mw ...func(*MethodCall, CallHandler) CallHandler) {
	ep.mx.Lock()
	defer ep.mx.Unlock()
	ep.mw = append(ep.mw, mw...)
}

// Root - register RPC handler instance as namespace root
// This service is used for method lookup when dispatched service name is empty
func (ep *Endpoint) Root(name string, inst interface{}, mw ...func(*MethodCall, CallHandler) CallHandler) error {
	ep.mx.Lock()
	defer ep.mx.Unlock()
	s, err := newServiceHandler("", inst, mw...)
	if err != nil {
		return err
	}
	ep.root = s
	return nil
}

// Register - register RPC handler instance by service name
// All exported instance methods matching following signature will be exposed for public access:
// func (t *T) ExportedMethod(context.Context, in *struct{...}, out *struct{...}) error
// Registering multiple service using same name is an error.
// Registering service with empty name returns result of Endpoint.Root method
func (ep *Endpoint) Register(name string, inst interface{}, mw ...func(*MethodCall, CallHandler) CallHandler) error {
	if name == "" {
		return ep.Root(name, inst, mw...)
	}
	ep.mx.Lock()
	defer ep.mx.Unlock()
	if _, ok := ep.services[name]; ok {
		return fmt.Errorf("service already registered")
	}
	s, err := newServiceHandler(name, inst, mw...)
	if err != nil {
		return err
	}
	ep.services[name] = s
	return nil
}

// Unregister - remove service from endpoint
func (ep *Endpoint) Unregister(name string) error {
	ep.mx.Lock()
	defer ep.mx.Unlock()
	if _, ok := ep.services[name]; ok {
		delete(ep.services, name)
		return nil
	}
	return fmt.Errorf("service not found: %s", name)
}

// resolve - find service and method handlers matching resolved names
func (ep *Endpoint) resolve(service, method string) (*ServiceHandler, *MethodHandler, error) {
	if service == "" {
		if ep.root == nil {
			err := fmt.Errorf("service not found: %s", method)
			return nil, nil, err
		}
		mh, ok := ep.root.Methods[method]
		if !ok {
			err := fmt.Errorf("method not found: %s", method)
			return nil, nil, err
		}
		return ep.root, mh, nil
	}
	sh, ok := ep.services[service]
	if !ok {
		err := fmt.Errorf("service not found: %s", method)
		return nil, nil, err
	}
	mh, ok := sh.Methods[method]
	if !ok {
		err := fmt.Errorf("method not found: %s", method)
		return nil, nil, err
	}
	return sh, mh, nil
}

// dispatch - resolve method handler and construct method call instance
func (ep *Endpoint) dispatch(cr CallRequest) (*MethodCall, error) {
	service, method := cr.Target()
	sh, mh, err := ep.resolve(service, method)
	if err != nil {
		return nil, err
	}
	param := reflect.New(mh.ReqType)
	if err := cr.Payload(param.Interface()); err != nil {
		return nil, err
	}
	return &MethodCall{
		Request: cr,
		Param:   param,
		Result:  reflect.New(mh.ResType),
		SH:      sh,
		MH:      mh,
		mw:      ep.mw,
	}, nil
}

// Dispatch - resolve request into MethodCall
func (ep *Endpoint) Dispatch(cr CallRequest) (*MethodCall, error) {
	ep.mx.RLock()
	defer ep.mx.RUnlock()
	return ep.dispatch(cr)
}

// createHandler - creates new handler if method signature matches requirements, else returns nil
func createHandler(meth reflect.Method) *MethodHandler {
	// check if method signature match in general
	if meth.PkgPath != "" || meth.Type.NumIn() != 4 || meth.Type.NumOut() != 1 {
		return nil
	}
	// check if return value type is error interface
	if rt := meth.Type.Out(0); rt != typeOfError {
		return nil
	}
	// check if 1st parameter is context interface
	if ct := meth.Type.In(1); ct.Kind() != reflect.Interface || !ct.Implements(typeOfContext) {
		return nil
	}
	// check if 2nd and 3rd parameters are serializable objects
	p2type := meth.Type.In(2)
	if p2type.Kind() != reflect.Ptr || !isExportedOrBuiltin(p2type) {
		return nil
	}
	p3type := meth.Type.In(3)
	if p3type.Kind() != reflect.Ptr || !isExportedOrBuiltin(p3type) {
		return nil
	}
	return &MethodHandler{
		Meth:    meth,
		ReqType: p2type.Elem(),
		ResType: p3type.Elem(),
	}
}

// newServiceHandler - detect and register handler methods in service instance
func newServiceHandler(name string, inst interface{}, mw ...func(*MethodCall, CallHandler) CallHandler) (*ServiceHandler, error) {
	s := &ServiceHandler{
		Name:     name,
		Inst:     reflect.ValueOf(inst),
		InstType: reflect.TypeOf(inst),
		Methods:  make(map[string]*MethodHandler),
		mw:       mw,
	}
	if name == "" {
		s.Name = reflect.Indirect(s.Inst).Type().Name()
		if !isCapitalized(s.Name) {
			return nil, fmt.Errorf("service name required for instance type %s", s.InstType.String())
		}
	}
	n := s.InstType.NumMethod()
	for i := 0; i < n; i++ {
		if mh := createHandler(s.InstType.Method(i)); mh != nil {
			s.Methods[mh.Meth.Name] = mh
		}
	}
	return s, nil
}

// semaphore - used to bound batch execution concurrency or to order calls sequentially
func (ep *Endpoint) semaphore() *semaphore.Weighted {
	size := ep.batch
	if size < 1 {
		size = 1
	}
	return semaphore.NewWeighted(int64(size))
}

// ServeHTTP - decode request body into multiple call requests and execute them sequentially or concurrently
func (ep *Endpoint) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	requests, err := ep.codec.DecodeRequest(r)
	if err != nil {
		ep.codec.EncodeError(w, err)
		return
	}
	var (
		wg      sync.WaitGroup // running call handlers wait group
		l       = len(requests)
		ctx     = r.Context()
		sem     = ep.semaphore()           // concurrency limit semaphore
		calls   = make([]func(), 0, l)     // resolved method calls
		out     = make(chan CallResult)    // result collection channel, closed by call handling completion waiter
		results = make([]CallResult, 0, l) // collection of results
		done    = make(chan struct{})      // closed by result collector when all result producers completed and all results collected
	)
	go func() { // start results collector to allow pushing dispatch errors early
		defer close(done)
		for res := range out {
			results = append(results, res)
		}
	}()
	for _, cr := range requests { // dispatch call requests and create semaphore gated closures
		mc, err := ep.Dispatch(cr)
		if err != nil {
			out <- cr.Result(nil, err)
			continue
		}
		calls = append(calls, func() {
			defer sem.Release(1)
			defer wg.Done()
			if ctx.Err() != nil {
				return
			}
			if res := cr.Result(mc.Invoke(ctx)); res != nil {
				out <- res
			} // discard result if CallRequest.Result returns nil
		})
	}
	for _, call := range calls { // start as much calls as semaphore permits
		if err := ctx.Err(); err != nil {
			ep.codec.EncodeError(w, ctx.Err())
			return
		}
		if err := sem.Acquire(ctx, 1); err != nil {
			ep.codec.EncodeError(w, err)
			return
		}
		wg.Add(1)
		go call()
	}
	go func() { // call handling completion waiter
		defer close(out)
		wg.Wait()
	}()
	select {
	case <-ctx.Done():
		ep.codec.EncodeError(w, ctx.Err())
		return
	case <-done:
		break
	}
	ep.codec.EncodeResults(w, results...)
}
