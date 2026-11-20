package protocol

// Request carries a protocol request payload.
type Request struct {
	Key     ProtocolKey
	ID      string
	Version uint16
	Payload []byte
}

// Response carries a protocol response payload.
type Response struct {
	Status  Status
	Payload []byte
}
