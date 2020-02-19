package main

import (
	"context"
	"log"
	"net/http"
	"sync/atomic"

	"github.com/unitrq/hirpc"
	"github.com/unitrq/hirpc/jsonrpc"
)

// EchoService - RPC handler struct
type EchoService struct {
	ops uint64
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

// Echo - RPC handler method
func (es *EchoService) Echo(ctx context.Context, req *EchoRequest, res *EchoReply) error {
	atomic.AddUint64(&es.ops, 1)
	res.Echo = req.Value
	return nil
}

// Count - RPC handler method
func (es *EchoService) Count(ctx context.Context, _ *struct{}, res *Count) error {
	defer atomic.AddUint64(&es.ops, 1)
	res.Value = atomic.LoadUint64(&es.ops)
	return nil
}

func main() {
	es := &EchoService{}
	ep := hirpc.NewEndpoint(jsonrpc.Codec, nil)
	ep.Register("echo", es)
	srv := &http.Server{
		Addr:    ":8000",
		Handler: ep,
	}
	if err := srv.ListenAndServe(); err != nil {
		log.Println(err)
	}
}
