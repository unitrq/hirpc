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
	"reflect"
	"sync"
	"unicode"
)

var (
	// reflect.Type of Context and error
	typeOfContext = reflect.TypeOf((*context.Context)(nil)).Elem()
	typeOfError   = reflect.TypeOf((*error)(nil)).Elem()
)

// CallHandler - method call handling function
type CallHandler func(context.Context) (interface{}, error)

// CallRequest - method call request provided by transport layer implementation.
// This object tells Endpoint which service and method to look up and which parameter type
// incoming data should match when constructing new method call context.
type CallRequest interface {
	ServiceMethod() (string, string) // returns service and method names respectively
	Parameter(interface{}) error     // decodes parameter into value or returns error
}

// CallResult - represents value or error as call result
// This implementation expects normal method call result value pointer can never be succesfully cast to error interface.
// If val is castable to error, it is treated as transport codec level application error
type CallResult struct {
	val interface{}
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
// MethodCall stores all objects required to invoke function reference produced by reflect package.
// Instead of executing dispatched request directly, Endpoint returns this frozen state object
// to allow calling side to precisely schedule execution of every single method call.
// This allows calling side to enforce required call execution scheduling policies for example to allow batching multiple
// method calls into single request/response roundtrip.
type MethodCall struct {
	CallRequest CallRequest                                  // call request object provided by transport layer handler used to dispatch
	SH          *ServiceHandler                              // service owning target method
	MH          *MethodHandler                               // target method handler
	Param       reflect.Value                                // deserialized parameter
	Result      reflect.Value                                // allocated result container
	mw          []func(*MethodCall, CallHandler) CallHandler // middlewares applied to this call
}

// Endpoint - resolves CallRequest's into *MethodCalls against registered services
type Endpoint struct {
	mx       sync.RWMutex                                 // used for synchronized service (de-)registration and lookup
	services map[string]*ServiceHandler                   // registered services
	mw       []func(*MethodCall, CallHandler) CallHandler // globally applied middleware
	root     *ServiceHandler                              // this service is exposed as namespace root when dispatching method call if set (when service name == "")
}

// Result - returns call result or error
func (cr CallResult) Result() (interface{}, error) {
	if err, ok := cr.val.(error); ok {
		return nil, err
	}
	return cr.val, nil
}

// ServiceName - returns name of call receiver service
func (call *MethodCall) ServiceName() string {
	return call.SH.Name
}

// MethodName - returns name of call receiver method
func (call *MethodCall) MethodName() string {
	return call.MH.Meth.Name
}

// ParamIf - returns interface pointer to call parameter value
func (call *MethodCall) ParamIf() interface{} {
	return call.Param.Interface()
}

// ResultIf - returns interface pointer to call result value
func (call *MethodCall) ResultIf() interface{} {
	return call.Result.Interface()
}

// invoke - invokes actual target method in call context
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
	return call.ResultIf(), nil
}

// Sync - block until target method returns
func (call *MethodCall) Sync(ctx context.Context) CallResult {
	handler := call.invoke
	for _, mw := range call.mw {
		handler = mw(call, handler)
	}
	res, err := handler(ctx)
	if err != nil {
		return CallResult{err}
	}
	return CallResult{res}
}

// Async - invoke method in background goroutine
func (call *MethodCall) Async(ctx context.Context) chan CallResult {
	c := make(chan CallResult)
	go func() {
		defer close(c)
		c <- call.Sync(ctx)
	}()
	return c
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

// NewEndpoint - create new service dispatcher
func NewEndpoint(mw ...func(*MethodCall, CallHandler) CallHandler) *Endpoint {
	ep := &Endpoint{
		services: make(map[string]*ServiceHandler),
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

// Root - set service as namespace root
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

// Register - register service in endpoint
func (ep *Endpoint) Register(name string, inst interface{}, mw ...func(*MethodCall, CallHandler) CallHandler) error {
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
func (ep *Endpoint) dispatch(req CallRequest) (*MethodCall, error) {
	sh, mh, err := ep.resolve(req.ServiceMethod())
	if err != nil {
		return nil, err
	}
	param := reflect.New(mh.ReqType)
	if err := req.Parameter(param.Interface()); err != nil {
		return nil, err
	}
	result := reflect.New(mh.ResType)
	return &MethodCall{
		CallRequest: req,
		Param:       param,
		Result:      result,
		SH:          sh,
		MH:          mh,
		mw:          ep.mw,
	}, nil
}

// Dispatch - resolve request into MethodCall
func (ep *Endpoint) Dispatch(req CallRequest) (*MethodCall, error) {
	ep.mx.RLock()
	defer ep.mx.RUnlock()
	return ep.dispatch(req)
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
	// if len(s.Methods) == 0 {
	// 	log.Println("service instance have no handler methods")
	// }
	return s, nil
}
