// Package main implements qmux-probe, the probe command of the interop harness.
//
// qmux-probe sends one probe for each combination of path, transport, class and type,
// one at a time. The direct path is the qmuxd of --server. Each --resolver name=addr
// adds one resolver path, and its queries carry RD=1. Each probe asks for a unique
// QNAME <nonce>.<zone> with retries disabled. The trust anchors of --trust-anchors
// verify every answer.
//
// Each probe gives one record with exactly one outcome: ok, rcode, timeout, network,
// mismatch or verify. With --observation-log, qmux-probe reads the observation log of
// qmuxd 500 ms after the last probe and records whether each probe reached the
// authoritative server. --output writes the records as a JSON array, and --markdown
// prints the interop matrix as a markdown table.
//
// The exit code is 0 when every probe has a record, whatever the outcomes are. With
// --require-direct, the exit code is 1 when a probe on the direct path has an outcome
// other than ok. A usage error exits 64.
//
// The specification is docs/specs/features/09-interop-harness.md.
package main
