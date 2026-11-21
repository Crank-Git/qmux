// Package qmux is the root facade of QMux. QMux runs independent application
// protocols over DNS. Each protocol owns a protocol key, and the QNAME names the
// resource.
//
// This package holds type aliases and constructors only, so that NewServer,
// ProtocolKey, NewClient and Query exist at the module root, as FR-client-41 in
// docs/specs/features/03-client-sdk.md requires. The behavior lives in the packages
// under pkg/.
package qmux

import (
	"github.com/Crank-Git/qmux/pkg/client"
	"github.com/Crank-Git/qmux/pkg/protocol"
	"github.com/Crank-Git/qmux/pkg/server"
)

// ServerConfig is the configuration of an authoritative server. It is server.ServerConfig.
type ServerConfig = server.ServerConfig

// ClientConfig is the configuration of a client. It is client.ClientConfig.
type ClientConfig = client.ClientConfig

// ProtocolKey is the (QCLASS, QTYPE) pair that identifies one protocol. It is
// protocol.ProtocolKey.
type ProtocolKey = protocol.ProtocolKey

// Query names the resource that a client asks for. It is client.Query.
type Query = client.Query

// NewServer returns the authoritative server that server.NewServer returns for cfg.
func NewServer(cfg ServerConfig) *server.Server {
	return server.NewServer(cfg)
}

// NewClient returns the client that client.NewClient returns for cfg.
func NewClient(cfg ClientConfig) *client.Client {
	return client.NewClient(cfg)
}
