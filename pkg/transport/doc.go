// Package transport is the DNS wire layer of QMux. It is the only package that
// imports github.com/miekg/dns.
//
// It builds queries, packs opaque RDATA, applies EDNS(0) and the UDP size limit,
// and matches answers to queries.
package transport
