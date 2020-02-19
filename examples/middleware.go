package main

import (
	"context"
	"fmt"
	"log"
	"net/http"
	"sync/atomic"

	"github.com/unitrq/hirpc"
	"github.com/unitrq/hirpc/jsonrpc"
)

// EchoService - RPC handler service
type EchoService struct {
	ops uint64
}

// Validator - allows to validate underlying value
type Validator interface {
	Validate() error
}

// Count - Count method parameter type
type Count struct {
	Value uint64 `json:"value"`
}

// EchoRequest - Echo method input parameter type
type EchoRequest struct {
	Value string `json:"value"`
}

// EchoReply - Echo method output parameter type
type EchoReply struct {
	Echo string `json:"echo"`
}

// Validate - validate attribute values
func (e *EchoRequest) Validate() error {
	if len(e.Value) == 0 {
		return fmt.Errorf("value attribute is required")
	}
	return nil
}

// Echo - basic echo method
func (es *EchoService) Echo(ctx context.Context, in *EchoRequest, out *EchoReply) error {
	out.Echo = in.Value
	return nil
}

// Count - returns call counter
func (es *EchoService) Count(ctx context.Context, _ *struct{}, res *Count) error {
	atomic.AddUint64(&es.ops, 1)
	res.Value = atomic.LoadUint64(&es.ops)
	return nil
}

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

func main() {
	es := &EchoService{}
	ep := hirpc.NewEndpoint(jsonrpc.Codec, nil, validator)
	ep.Register("echo", es)
	srv := &http.Server{
		Addr:    ":8000",
		Handler: ep,
	}
	if err := srv.ListenAndServe(); err != nil {
		log.Println(err)
	}
}
