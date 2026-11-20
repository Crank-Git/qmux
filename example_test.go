package qmux_test

import (
	"context"
	"fmt"
	"log"
	"os"
	"os/signal"

	"github.com/Crank-Git/qmux"
	"github.com/Crank-Git/qmux/pkg/protocol"
)

// firmwareHandler answers the firmware metadata of a device.
type firmwareHandler struct{}

// Handle returns the firmware metadata for the device that req.Labels names.
func (firmwareHandler) Handle(ctx context.Context, req *protocol.Request) (*protocol.Response, error) {
	return &protocol.Response{Payload: []byte("firmware 1.4.2 for " + req.Labels[0])}, nil
}

// Info returns the protocol ID and the protocol versions of the firmware protocol.
func (firmwareHandler) Info() protocol.Info {
	return protocol.Info{ID: "org.qmux.firmware", Versions: []uint16{1}}
}

// ExampleNewServer serves the firmware protocol at the protocol key 65280/65280.
// The example has no Output comment, because ListenAndServe runs until an interrupt.
func ExampleNewServer() {
	srv := qmux.NewServer(qmux.ServerConfig{
		Address: "127.0.0.1:5353",
		Zones:   []string{"fleet.example."},
	})
	key := qmux.ProtocolKey{Class: 65280, Type: 65280}
	if err := srv.Register(key, firmwareHandler{}); err != nil {
		log.Fatal(err)
	}

	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt)
	defer stop()
	if err := srv.ListenAndServe(ctx); err != nil {
		log.Fatal(err)
	}
}

// ExampleNewClient asks the server of ExampleNewServer for the firmware metadata.
// The example has no Output comment, because it needs a running server.
func ExampleNewClient() {
	c := qmux.NewClient(qmux.ClientConfig{Resolver: "127.0.0.1:5353"})
	res, err := c.Query(context.Background(), qmux.Query{
		Name:  "device42.fleet.example.",
		Class: 65280,
		Type:  65280,
	})
	if err != nil {
		log.Fatal(err)
	}
	fmt.Printf("%s\n", res.Payload)
}
