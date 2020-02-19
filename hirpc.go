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

var (
	// reflect.Type of Context and error
	typeOfContext = reflect.TypeOf((*context.Context)(nil)).Elem()
	typeOfError   = reflect.TypeOf((*error)(nil)).Elem()
	// DefaultCallScheduler - default call execution scheduler
	DefaultCallScheduler = &SequentialScheduler{}
)

// CallHandler - method call handling function
type CallHandler func(context.Context) (interface{}, error)

// CallRequest - HTTPCodec single method call request object
type CallRequest interface {
	Target() (string, string)              // decode call request target service and method name
	Payload(interface{}) error             // decode call request parameter payload into specific type pointer
	Result(interface{}, error) interface{} // construct result object specific for this request and protocol
}

// HTTPCodec - request adapter interface implementing protocol validation and data (de-)serialization
type HTTPCodec interface {
	EncodeError(http.ResponseWriter, error)             // send http response representing single error message
	EncodeResults(http.ResponseWriter, ...interface{})  // send http response representing one or more call results
	DecodeRequest(*http.Request) ([]CallRequest, error) // read and close http request body and decode one or more method call requests to execute, return (nil, nil) if request is valid but no calls was decoded
}

// CallScheduler - used to execute multiple method calls decoded from request untill all methods finish or context gets cancelled
type CallScheduler interface {
	Execute(context.Context, []func()) error
}

// MethodHandler - stores reflected method function reference and specific signature request/response types.
type MethodHandler struct {
	Meth    reflect.Method // method pointer
	ReqType reflect.Type   // signature parameter type
	ResType reflect.Type   // signature result type
}

// ServiceHandler - stores service instance, type, service-level middleware and method handlers collection.
type ServiceHandler struct {
	Name     string                                        // name used to reference service in registry namespace
	Methods  map[string]*MethodHandler                     // descriptors for methods with RPC handler compatible signature
	Inst     reflect.Value                                 // pointer to service instance
	InstType reflect.Type                                  // type of service instance
	mw       []func(*CallContext, CallHandler) CallHandler // service-specific call handlers
}

// CallContext - method call dispatch context.
// Captures information passed by Endpoint into user middleware applied to handler.
// Exposes source call request protocol object and handler method information.
type CallContext struct {
	Request CallRequest   // dispatch source provided by codec, used to construct codec response for this call
	Service string        // target service name
	Method  string        // target method name
	Param   reflect.Value // deserialized parameter
	Result  reflect.Value // allocated result container
}

// Endpoint - net/http request handler and RPC service registry.
// Decodes http requests using HTTPCodec and schedules procedure calls for execution using CallScheduler.
type Endpoint struct {
	mx       sync.RWMutex                                  // used for synchronized service (de-)registration and lookup
	services map[string]*ServiceHandler                    // registered services
	mw       []func(*CallContext, CallHandler) CallHandler // globally applied middleware
	root     *ServiceHandler                               // this service is exposed as namespace root when dispatching method call if set (when service name == "")
	codec    HTTPCodec                                     // transport protocol request and response codec
	sched    CallScheduler                                 // request call execution scheduler
}

// SequentialScheduler - sequential execution call scheduler.
type SequentialScheduler struct {
}

// ConcurrentScheduler - concurrently executing call scheduler.
type ConcurrentScheduler struct {
	Limit int // concurrency limit per request
}

// Execute - executes handlers in order of appearance.
func (ss *SequentialScheduler) Execute(ctx context.Context, handlers []func()) error {
	for _, handle := range handlers {
		if err := ctx.Err(); err != nil {
			return err
		}
		handle()
	}
	return nil
}

// Execute - executes multiple handlers in background limiting concurrency by semaphore instance.
func (cs *ConcurrentScheduler) Execute(ctx context.Context, handlers []func()) error {
	var wg sync.WaitGroup
	limit := cs.Limit
	if limit < 1 {
		limit = 1
	}
	sem := semaphore.NewWeighted(int64(limit))
	for _, h := range handlers {
		if err := ctx.Err(); err != nil {
			return err
		}
		if err := sem.Acquire(ctx, 1); err != nil {
			return err
		}
		wg.Add(1)
		handle := h
		go func() {
			defer sem.Release(1)
			defer wg.Done()
			if ctx.Err() == nil {
				handle()
			}
		}()
	}
	wg.Wait()
	return nil
}

// isCapitalized - returns true if string starts with uppercase.
func isCapitalized(s string) bool {
	return s != "" && unicode.IsUpper(rune(s[0]))
}

// isExportedOrBuiltin - returns true if a type is exported or a builtin.
func isExportedOrBuiltin(t reflect.Type) bool {
	for t.Kind() == reflect.Ptr {
		t = t.Elem()
	}
	return isCapitalized(t.Name()) || t.PkgPath() == ""
}

// NewEndpoint - creates new RPC endpoint, returns nil if codec or sched is nil, registers endpoint-level middleware if provided.
func NewEndpoint(codec HTTPCodec, sched CallScheduler, mw ...func(*CallContext, CallHandler) CallHandler) *Endpoint {
	if codec == nil || sched == nil {
		return nil
	}
	for left, right := 0, len(mw)-1; left < right; left, right = left+1, right-1 {
		mw[left], mw[right] = mw[right], mw[left]
	}
	ep := &Endpoint{
		services: make(map[string]*ServiceHandler),
		codec:    codec,
		sched:    sched,
		mw:       mw,
	}
	return ep
}

// Service - looks up service handler by name.
func (ep *Endpoint) Service(name string) (*ServiceHandler, bool) {
	ep.mx.RLock()
	defer ep.mx.RUnlock()
	if s, ok := ep.services[name]; ok {
		return s, true
	}
	return nil, false
}

// Services - returns copy of service handlers map.
func (ep *Endpoint) Services() map[string]*ServiceHandler {
	ep.mx.RLock()
	defer ep.mx.RUnlock()
	services := make(map[string]*ServiceHandler, len(ep.services))
	for k, v := range ep.services {
		services[k] = v
	}
	return services
}

// Use - replaces endpoint middleware list.
func (ep *Endpoint) Use(mw ...func(*CallContext, CallHandler) CallHandler) {
	for left, right := 0, len(mw)-1; left < right; left, right = left+1, right-1 {
		mw[left], mw[right] = mw[right], mw[left]
	}
	ep.mx.Lock()
	defer ep.mx.Unlock()
	ep.mw = mw
}

// Root - registers RPC handler instance as namespace root.
// This service is used for method lookup when dispatched service name is empty.
func (ep *Endpoint) Root(name string, inst interface{}, mw ...func(*CallContext, CallHandler) CallHandler) error {
	ep.mx.Lock()
	defer ep.mx.Unlock()
	s, err := newServiceHandler("", inst, mw...)
	if err != nil {
		return err
	}
	ep.root = s
	return nil
}

// Register - registers RPC handler instance by service name.
// All exported instance methods matching following signature will be exposed for public access:
// func (*ExportedType) ExportedMethod(context.Context, *InType, *OutType) error
// Registering service with empty name returns result of Endpoint.Root method.
func (ep *Endpoint) Register(name string, inst interface{}, mw ...func(*CallContext, CallHandler) CallHandler) error {
	if name == "" {
		return ep.Root(name, inst, mw...)
	}
	ep.mx.Lock()
	defer ep.mx.Unlock()
	s, err := newServiceHandler(name, inst, mw...)
	if err != nil {
		return err
	}
	ep.services[name] = s
	return nil
}

// Unregister - remove service from endpoint.
func (ep *Endpoint) Unregister(name string) error {
	ep.mx.Lock()
	defer ep.mx.Unlock()
	if _, ok := ep.services[name]; ok {
		delete(ep.services, name)
		return nil
	}
	return fmt.Errorf("service not found: %s", name)
}

// resolve - finds service and method handlers matching resolved names.
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

// Dispatch - resolves service and method handlers and constructs CallHandler closure.
func (ep *Endpoint) Dispatch(cr CallRequest) (CallHandler, error) {
	ep.mx.RLock()
	defer ep.mx.RUnlock()
	service, method := cr.Target()
	sh, mh, err := ep.resolve(service, method)
	if err != nil {
		return nil, err
	}
	param := reflect.New(mh.ReqType)
	if err := cr.Payload(param.Interface()); err != nil {
		return nil, err
	}
	result := reflect.New(mh.ResType)
	mw := append([]func(*CallContext, CallHandler) CallHandler(nil), sh.mw...)
	mw = append(mw, ep.mw...)
	fn := mh.Meth.Func
	handler := func(ctx context.Context) (interface{}, error) {
		errVal := fn.Call([]reflect.Value{
			sh.Inst,
			reflect.ValueOf(ctx),
			param,
			result,
		})
		if eIf := errVal[0].Interface(); eIf != nil {
			return nil, eIf.(error)
		}
		return result.Interface(), nil
	}
	cc := &CallContext{
		Request: cr,
		Param:   param,
		Result:  result,
		Service: service,
		Method:  method,
	}
	for _, mw := range mw {
		handler = mw(cc, handler)
	}
	return handler, nil
}

// newMethodHandler - creates new handler if method signature matches requirements, else returns nil.
func newMethodHandler(meth reflect.Method) *MethodHandler {
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

// newServiceHandler - detects and registers handler methods in service instance matching signature pattern.
func newServiceHandler(name string, inst interface{}, mw ...func(*CallContext, CallHandler) CallHandler) (*ServiceHandler, error) {
	// reverse middleware order
	for left, right := 0, len(mw)-1; left < right; left, right = left+1, right-1 {
		mw[left], mw[right] = mw[right], mw[left]
	}
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
			return nil, fmt.Errorf("instance type %s not exported", s.InstType.String())
		}
	}
	n := s.InstType.NumMethod()
	for i := 0; i < n; i++ {
		if mh := newMethodHandler(s.InstType.Method(i)); mh != nil {
			s.Methods[mh.Meth.Name] = mh
		}
	}
	return s, nil
}

// ServeHTTP - decode request body into multiple call requests and execute them sequentially or concurrently.
func (ep *Endpoint) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	requests, err := ep.codec.DecodeRequest(r)
	if err != nil {
		ep.codec.EncodeError(w, err)
		return
	}
	if requests == nil {
		ep.codec.EncodeResults(w)
		return
	}
	var (
		l       = len(requests)
		ctx     = r.Context()
		calls   = make([]func(), 0, l)   // resolved method calls
		out     = make(chan interface{}) // result collection channel
		done    = make(chan struct{})    // closed when all results collected
		results = make([]interface{}, 0, l)
	)
	go func() { // start results collector to allow pushing dispatch errors early
		defer close(done)
		for res := range out {
			results = append(results, res)
		}
	}()
	for _, cr := range requests { // dispatch call requests and create semaphore gated closures
		call, err := ep.Dispatch(cr)
		if err != nil {
			out <- cr.Result(nil, err)
			continue
		}
		calls = append(calls, func() { // wrap call result into protocol response
			if res := cr.Result(call(ctx)); res != nil {
				out <- res
			} // discard result if CallRequest.Result returns nil
		})
	}
	e := ep.sched.Execute(ctx, calls) // wait until handlers complete
	close(out)                        // stop result collector
	<-done                            // wait for it to complete
	if e != nil || ctx.Err() != nil {
		ep.codec.EncodeError(w, e)
		return
	}
	ep.codec.EncodeResults(w, results...)
}
