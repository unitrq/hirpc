# hirpc - dynamic RPC service dispatcher for net/http handler interface
Package hirpc provide `Endpoint` type - simple dynamic dispatch service registry for user-defined structs. Most of it was inspired by [github.com/gorilla/rpc](https://github.com/gorilla/rpc). It allows user to expose exported struct methods matching specific signature pattern as a set of named RPC services using standard library net/http request handler interface.
`Endpoint` maintains collection of named service instances and their exposed methods as flat namespace with methods identified by service and method names.
When registering struct as a service with `Register` method, `Endpoint` relies on `reflect` package to extract, validate and store user type metadata for later use.
All struct methods matching signature pattern of 
```go
func (t *T) ExportedMethod(context.Context, in *struct{...}, out *struct{...}) error
``` 
are considered as exported RPC handlers and exposed as methods of this service. Parameter names as well as any other methods and properties are ignored.
*Shared state access within handler methods is subject to proper synchronization by user, since multiple instances of multiple method calls could be running concurrently.*
In order to invoke call handler, service and method are looked up and method call context gets constructed with `Dispatch(req CallRequest) (*MethodCall, error)` method from `CallRequest` interface
```go
// CallRequest - method call request provided by transport layer implementation.
type CallRequest interface {
	ServiceMethod() (string, string) // returns service and method names respectively
	Parameter(interface{}) error     // decodes parameter into value or returns error
}
```
`CallRequest` tells `Endpoint` which method of which service is called and provides method to decode raw parameter data into pointer to specific type instance.
If method lookup succeeds and parameter type matches receiver defined by handler signature, `Endpoint` constructs `MethodCall` object which represents single method invocation context with decoded parameter value and newly allocated result receiver value. This `MethodCall` object is then used to execute actual procedure call handler and retrieve result as `CallResult` object.
`CallResult` is a simple wrapper struct over `interface{}` value used to represent either error-castable object which is treated as call terminated with error, or serializable object which is treated as normal method call result.

`Endpoint` also provides `NewHTTPHandler(codec HTTPCodec, batch int) (*HTTPHandler, error)` method which returns `net/http` handler interface implementation. `batch` parameter defines how many method calls are allowed to run concurrently per request. `codec` parameter defines actual RPC protocol implementation used over http request transport, providing handler a way to decode http request payload into some RPC method calls and to encode one or many results back into response maintaining mapping between each possible call in request and its result in response:
```go
// HTTPCodec - translates http request into set of marked requests and writes one or many results into response stream
type HTTPCodec interface {
	DecodeRequest(*http.Request) (map[string]CallRequest, error)
	EncodeResponse(http.ResponseWriter, *string, CallResult)
	EncodeResponses(http.ResponseWriter, map[string]CallResult)
}
```

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

Create `Endpoint`, register service, create handler for JSON-RPC protocol codec and start http server
```go
func main() {
	ep := hirpc.NewEndpoint()
	es := &EchoService{}
	ep.Register("echo", es)
	eh, err := ep.NewHTTPHandler(jsonrpc.DefaultCodec, hirpc.DefaultBatchLimit)
	if err != nil {
		log.Fatalln(err)
	}
	srv := &http.Server{
		Addr:    ":8000",
		Handler: eh,
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

# ToDo
- Add tests
- Improve documentation