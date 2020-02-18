# hirpc - dynamic RPC service dispatcher for net/http handler interface
Package hirpc provide `Endpoint` type - simple dynamic dispatch service registry for user defined structs. Most of it was inspired by [github.com/gorilla/rpc](https://github.com/gorilla/rpc). It allows user to expose exported struct methods matching specific signature pattern as a set of named RPC services using standard library net/http request handler interface and specific protocol codec adapter.
# Usage
Define handler service and parameter types
```go
type EchoService struct {
	ops uint64
} 

type Echo struct {
	Value string `json:"value"`
}

type Count struct {
	Value uint64 `json:"value"`
}
```

Define handler methods
```go

func (es *EchoService) Echo(ctx context.Context, req *Echo, res *Echo) error {
	defer atomic.AddUint64(&es.ops, 1)
	res.Value = req.Value
	return nil
}

func (es *EchoService) Count(ctx context.Context, _ *struct{}, res *Count) error {
	defer atomic.AddUint64(&es.ops, 1)
	res.Value = atomic.LoadUint64(&es.ops)
	return nil
}

```

Create `Endpoint` using `jsonrpc` protocol codec and start http server
```go
func main() {
	es := &EchoService{}
	ep := hirpc.NewEndpoint(jsonrpc.DefaultCodec, hirpc.DefaultCallScheduler)
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
# Description
`Endpoint` maintains metadata collection for set of service instances and their methods collected using `reflect` package when user registers new service instance with `Endpoint.Register` method. All struct methods matching signature pattern of 
```go
func (*ExportedType) ExportedMethod(context.Context, *InType, *OutType) error
``` 
are treated as exported RPC handlers and registered as methods of this service. Parameter names as well as any other methods and properties are ignored.

In order to handle incoming http requests, `Endpoint` uses `HTTPCodec` interface implementation to translate protocol data format into resolvable object and `CallScheduler` instance to invoke resolved method handlers concurrently or sequentially.
`Endpoint` starts by decoding request body into set of `CallRequest` objects using `HTTPCodec.DecodeRequest` implementation.

`CallRequest` tells `Endpoint` which method of which service caller is looking for, provides method to decode raw parameter data into pointer to specific type instance and constructor for protocol-level response object.
Decoded `CallRequest` objects get resolved against service registry using `Dispatch` method described below.

Then `Endpoint` schedules successfully resolved calls for background execution using `CallScheduler` instance, awaits results and pass them to `HTTPCodec.EncodeResults` to construct complete http response.

Package provides `SequentialScheduler` implementation to execute multiple method calls in sequential order and `ConcurrentScheduler` for semaphore-bounded concurrent execution of multiple handlers within request.

*Shared state access within handler methods implementation is subject to proper synchronization by user, since multiple instances of multiple method calls could be running concurrently.*

`NewEndpoint`, `Endpoint.Use`, `Endpoint.Register` and `Endpoint.Root` functions accept variadic list of functions with `func(*MethodCall, CallHandler) CallHandler` signature used as middleware constructors applied to prepared method call context when handler execution is about to start.
When call handling starts, functions invoked in order of appearance, endpoint middleware invoked first.

`CallHandler` type represents execution of single method call with specific input and output parameter values defined like this: `type CallHandler func(context.Context) (interface{}, error)`.

`Endpoint.Dispatch` finds requested method reflected handler and construct execution context in `MethodCall` instance, `CallHandler` value resolved by `Endpoint.Dispatch` is `Invoke` method of `MethodCall`.
`MethodCall` object captures call dispatch context and list of middleware wrappers applied to this call, exposing service and method metadata introspected by `reflect` package to user middleware.

`NewEndpoint` and `Endpoint.Use` register endpoint-level middleware applied to every method call of every service, while `Endpoint.Register` and `Endpoint.Root` register service-level middleware applied only to methods of one specific service.
`Endpoint` public methods are synchronized using `sync.RWMutex` so it's safe to `Register` and `Unregister` services at runtime.

Service registered with empty name or using `Endpoint.Root` method is a namespace root service, `Endpoint` dispatches calls with empty service name to namespace root if it's not nil. Registering new service under same name discards previously registered instance.

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
