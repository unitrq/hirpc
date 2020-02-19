# hirpc - dynamic RPC service dispatcher for net/http handler interface
Package hirpc provide `Endpoint` type - simple dynamic dispatch service registry for user defined structs. Most of it was inspired by [github.com/gorilla/rpc](https://github.com/gorilla/rpc). It allows user to expose exported struct methods matching specific signature pattern as a set of named RPC services using standard library net/http request handler interface and specific protocol codec adapter.

Key differences from `gorilla/rpc`, `net/rpc` and other implementations are compact implementation with minimal api and middleware interface modeled after `net/http` api but exposing method call context to user callbacks. This allows users to implement constraining patterns like rate-limiting or per-service and per-method access authorization in simple declarative manner.

# Usage
### Simple JSON-RPC 2.0 server
To build a simple Echo RPC service handler define handler service and parameter types
```go
type EchoService struct {
	ops uint64
} 

// EchoRequest - Echo method input parameter type
type EchoRequest struct {
	Value string `json:"value"`
}

// EchoReply - Echo method output parameter type
type EchoReply struct {
	Echo string `json:"echo"`
}

type Count struct {
	Value uint64 `json:"value"`
}
```

Define handler methods
```go

func (es *EchoService) Echo(ctx context.Context, req *EchoRequest, res *EchoReply) error {
	defer atomic.AddUint64(&es.ops, 1)
	res.Value = req.Value
	return nil
}

func (es *EchoService) Count(ctx context.Context, _ *struct{}, res *Count) error {
	atomic.AddUint64(&es.ops, 1)
	res.Value = atomic.LoadUint64(&es.ops)
	return nil
}

```

Create `Endpoint` using `jsonrpc` protocol codec and start http server
```go
func main() {
	es := &EchoService{}
	ep := hirpc.NewEndpoint(jsonrpc.Codec, hirpc.DefaultCallScheduler)
	ep.Register("echo", es)
	srv := &http.Server{
		Addr:    ":8000",
		Handler: ep,
	}
	if err := srv.ListenAndServe(); err != nil {
		log.Println(err)
	}
}
```

This is basically all required bits to get JSON-RPC 2.0 compatible server up and running, ready to serve procedure calls over http:
```
$ curl -H 'Content-Type: application/json;' --data-binary \
'[{"jsonrpc":"2.0","id":"12345","method":"echo.Echo","params": {"value": "123"}}, {"jsonrpc":"2.0","id":"23456","method":"echo.Count","params": {}}]' \
http://localhost:8000/
[{"id":"12345","jsonrpc":"2.0","result":{"value":"123"}},{"id":"23456","jsonrpc":"2.0","result":{"value":1}}]

```

### More complex example using middleware
To improve on previous example we'll add middleware to enforce validation of incoming parameter data before method call if parameter type implements specific interface.

Add `Validator` interface and implement `Validate` method for `EchoRequest`
```go
// Validator - allows to validate underlying value
type Validator interface {
	Validate() error
}

// Validate - validate attribute values
func (e *EchoRequest) Validate() error {
	if len(e.Value) == 0 {
		return fmt.Errorf("value attribute is required")
	}
	return nil
}

```
Define middleware constructor
```go
// validator - constructs middleware that validates method input parameter if it implements Validator interface
func validator(cc *hirpc.CallContext, h hirpc.CallHandler) hirpc.CallHandler {
	return func(ctx context.Context) (interface{}, error) {
		param := cc.Param.Interface()
		if val, ok := param.(Validator); ok {
			if err := val.Validate(); err != nil {
				return nil, err
			}
		}
		return h(ctx)
	}
}

```
Then pass middleware to endpoint or service
```go

func main() {
	es := &EchoService{}
	ep := hirpc.NewEndpoint(jsonrpc.Codec, hirpc.DefaultCallScheduler, validator)
	ep.Register("echo", es)
	srv := &http.Server{
		Addr:    ":8000",
		Handler: ep,
	}
	if err := srv.ListenAndServe(); err != nil {
		log.Println(err)
	}
}

```

This way input data validation is enforced just by implementing `Validator` interface on specific type:
```
$ curl -H 'Content-Type: application/json;' \
--data-binary '[{"jsonrpc":"2.0","id":"12345","method":"echo.Count","params": {}}, {"jsonrpc":"2.0","id":"23456","method":"echo.Echo","params": {"value": ""}}]' \
http://localhost:8000/    
[{"id":"23456","jsonrpc":"2.0","result":{"value":1}},{"id":"23456","jsonrpc":"2.0","error":{"code":-32000,"message":"value attribute is required"}}]                                                                                   
```

# Description
`Endpoint` maintains metadata collection for set of service instances and their methods collected using `reflect` package when user registers new service instance with `Endpoint.Register` method. All struct methods matching signature pattern of 
```go
func (*ExportedType) ExportedMethod(context.Context, *InType, *OutType) error
``` 
are treated as exported RPC handlers and registered as methods of this service. Input and output parameters should be pointers to the types used protocol codec can (de)serialize. All other methods and properties are ignored.

Service registered with empty name or using `Endpoint.Root` method is a namespace root service, `Endpoint` uses it to handle method calls with empty service name if set. Registering new service under same name discards previously registered instance.

`Endpoint` public methods are synchronized using `sync.RWMutex` so it's safe to `Register` and `Unregister` services at runtime.

### HTTP request handling
In order to handle incoming http requests, `Endpoint` uses `HTTPCodec` interface implementation to translate protocol data format into resolvable object and `CallScheduler` instance to invoke resolved method handlers concurrently or sequentially.

`Endpoint` starts by decoding request body into set of `CallRequest` objects using `HTTPCodec.DecodeRequest` implementation. `CallRequest` tells `Endpoint` which method of which service caller is looking for, provides method to decode raw parameter data into pointer to specific type instance and constructor for protocol-level response object.

Decoded `CallRequest` objects get resolved against service registry by `Endpoint.Dispatch` method using service and method name. `Dispatch` looks up requested method, allocates new value of input type and tries to decode payload into receiver. If deserialization succeeds, it constructs `CallHandler` capturing allocated input and output parameter instances in closure, wrapping it into middleware chain if necessary. Lookup failures and parameter deserialization errors treated the same way as handler returning error.

Then `Endpoint` schedules successfully resolved calls for background execution using `CallScheduler` instance and wraps results using `CallRequest.Result` response constructor to construct complete http response with `HTTPCodec.EncodeResults`.

Package provides `SequentialScheduler` implementation to execute multiple method calls in sequential order and `ConcurrentScheduler` for semaphore-bounded concurrent execution of multiple handlers within request.

*Shared state access within handler methods is subject to proper synchronization by user, since multiple instances of multiple method calls could be running concurrently.*

### Middleware
`NewEndpoint`, `Endpoint.Use`, `Endpoint.Register` and `Endpoint.Root` functions accept variadic list of functions with
```go
func(*CallContext, CallHandler) CallHandler
```
signature used as middleware constructors applied to prepared method call context when handler execution is about to start.
`CallContext` object provides access to service and method names, allocated parameter values and `CallRequest` instance to user middleware.
When call handler starts, functions invoked in order of appearance, endpoint middleware invoked first.

`CallHandler` type represents execution of single method call with specific input and output parameter values defined like this:
```go
type CallHandler func(context.Context) (interface{}, error)
```

`NewEndpoint` and `Endpoint.Use` register endpoint-level middleware applied to every method call of every service, while `Endpoint.Register` and `Endpoint.Root` register service-level middleware applied only to methods of one specific service.

# ToDo
- Add tests
- Improve documentation
- Find a way to generate handlers with go-swagger

# Known limitations and bugs
The only implemented `HTTPCodec` is currently a JSON-RPC 2.0 compatible codec in `jsonrpc` subpackage.
Method specifier resolved into service and method name accordingly by attempting to left split it once with "." separator.
If split fails empty string resolved as service name and original method specifier as method name attempting lookup method on `Endpoint` namespace root service.

It does not implement any `rpc` namespace introspection (but probably could).

Maximum allowed request body size is set to 2097152 bytes by `maxBodySize` constant in codec source.
