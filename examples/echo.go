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

// Echo - Echo method parameter type
type Echo struct {
	Value string `json:"value"`
}

// Echo - RPC handler method
func (es *EchoService) Echo(ctx context.Context, req *Echo, res *Echo) error {
	defer atomic.AddUint64(&es.ops, 1)
	res.Value = req.Value
	return nil
}

// Count - Count method parameter type
type Count struct {
	Value uint64 `json:"value"`
}

// Count - RPC handler method
func (es *EchoService) Count(ctx context.Context, _ *struct{}, res *Count) error {
	defer atomic.AddUint64(&es.ops, 1)
	res.Value = atomic.LoadUint64(&es.ops)
	return nil
}

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