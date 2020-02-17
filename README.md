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
`Endpoint` maintains metadata collection for set of service instances and their methods collected using `reflect` package when user registers new service instance. All struct methods matching signature pattern of 
```go
func (*ExportedType) ExportedMethod(context.Context, *InType, *OutType) error
``` 
are treated as exported RPC handlers and registered as methods of this service. Parameter names as well as any other methods and properties are ignored.
In order to handle incoming http requests, `Endpoint` uses `HTTPCodec` interface implementation to translate protocol data format into resolvable object and `CallScheduler` instance to invoke resolved method handlers concrurrently or sequentially. First, `Endpoint` decodes request body into set of `CallRequest` objects using `HTTPCodec.DecodeRequest` implementation. `CallRequest` tells `Endpoint` which method of which service caller is looking for, provides method to decode raw parameter data into pointer to specific type instance and constructor for protocol-level response object. Then `Endpoint` schedules successfully resolved calls for background execution using `CallScheduler` instance and awaits results. Package provides `SequentialScheduler` implementation to execute multiple method calls in sequential order and `ConcurrentScheduler` for semaphore-bounded concurrent execution of multiple handlers.

*Shared state access within handler methods implementation is subject to proper synchronization by user, since multiple instances of multiple method calls could be running concurrently.*

`Endpoint` public methods are synchronized using `sync.RWMutex` so it's safe to `Register` and `Unregister` services at runtime.
`NewEndpoint` and `Endpoint.Register` accept variadic list of function parameters acting as simple middleware stack endpoint-wise and service-wise respectively. Middleware function should match `func(*MethodCall, CallHandler) CallHandler` signature and is applied in source order when resolved method call gets actually invoked. `CallHandler` is a function type defined like this: `type CallHandler func(context.Context) (interface{}, error)`

# ToDo
- Add tests
- Improve documentation
- Find a way to generate handlers with go-swagger

# Known limitations and bugs
The only implemented `HTTPCodec` is currently a JSON-RPC 2.0 compatible codec in `jsonrpc` subpackage.
It resolves method specifier into service and method name accordingly by attempting to left split it once with "." separator.
If split fails empty string resolved as service name and original method specifier as method name attempting lookup on `Endpoint` namespace root.
It does not implement any "rpc" namespace introspection (but probably could).

This codec `SHOULD` be spec compliant but there are probably some violations I don't know about.