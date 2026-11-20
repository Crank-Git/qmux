// Package main implements the QMux DNS server daemon.
//
// Commands:
//
//	qmuxd serve [--config <file>] [--listen <addr>]
//	qmuxd version
//	qmuxd help
//
// qmuxd serve reads the configuration file at --config. The default is ./qmuxd.json.
// The --listen flag replaces the listen field of the file. qmuxd serve answers over UDP
// and TCP, and it signs every envelope. On SIGINT or SIGTERM, it shuts down and exits
// with code 0.
//
// An optional probe section of the configuration file holds the fields zone, classes
// and types. For each class and type pair, qmuxd serves the probe protocol
// org.qmux.probe, version 1. The probe answers every QNAME at or below the probe zone
// with the SHA-256 digest of the lowercase QNAME. Class 3 and class 4 need
// allow_reserved_classes, and type 16 needs allow_non_private_types. Only the probe
// section can use type 16.
//
// qmuxd serve exits with code 1 when a listener does not bind. It exits with code 2
// when the configuration file or the signing key does not load. It exits with code 64
// on a usage error.
//
// qmuxd version prints the module version and the Go version.
package main
