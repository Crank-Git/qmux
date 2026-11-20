// Package qmux is the root facade of QMux. QMux runs independent application
// protocols over DNS. Each protocol owns a protocol key, and the QNAME names the
// resource.
//
// This package holds type aliases and constructors only, so that NewServer,
// ProtocolKey, NewClient and Query compile as the brief shows them. The behavior
// lives in the packages under pkg/.
package qmux
