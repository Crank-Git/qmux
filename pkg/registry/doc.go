// Package registry manages protocol registrations with input validation.
//
// The registry accepts a protocol key, ID, versions, and handler. It validates
// each input in order: handler, ID, versions, key. It stores the registration
// and rejects duplicates.
package registry
