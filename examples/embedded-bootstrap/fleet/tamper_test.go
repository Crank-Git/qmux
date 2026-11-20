package fleet

import (
	"bytes"
	"encoding/binary"
	"testing"

	"github.com/Crank-Git/qmux/pkg/codec"
)

// response returns a DNS response for v1.device42.fleet.example. at the protocol key
// (class, typ). The answer RR uses a compression pointer to the question name. The
// response ends with an OPT RR, as a response to an EDNS(0) query does. It returns the
// message and the offset of the RDATA.
func response(t *testing.T, class, typ uint16) ([]byte, int) {
	t.Helper()
	envelope, err := codec.Encode(codec.Envelope{
		Format: 1, ProtocolID: ConfigurationID, Version: 1, MessageType: 1,
		Payload: []byte{0xa0},
		Signature: &codec.SignatureBlock{
			KeyID: make([]byte, 8), Alg: -8, IssuedAt: 1, ExpiresAt: 2,
			Signature: bytes.Repeat([]byte{0x5a}, 64),
		},
	}, codec.Limits{})
	if err != nil {
		t.Fatalf("Encode: %v", err)
	}
	m := []byte{0x12, 0x34, 0x84, 0x00, 0, 1, 0, 1, 0, 0, 0, 1}
	for _, label := range []string{"v1", "device42", "fleet", "example"} {
		m = append(m, byte(len(label)))
		m = append(m, label...)
	}
	m = append(m, 0)
	m = binary.BigEndian.AppendUint16(m, typ)
	m = binary.BigEndian.AppendUint16(m, class)
	m = append(m, 0xc0, 0x0c)
	m = binary.BigEndian.AppendUint16(m, typ)
	m = binary.BigEndian.AppendUint16(m, class)
	m = binary.BigEndian.AppendUint32(m, 60)
	m = binary.BigEndian.AppendUint16(m, uint16(len(envelope)))
	rdata := len(m)
	m = append(m, envelope...)
	m = append(m, 0, 0, 41, 0x04, 0xd0, 0, 0, 0, 0, 0, 0)
	return m, rdata
}

func TestTamperChangesOneByteInTheRDATAOfAConfigurationAnswer(t *testing.T) { // FR-demo-40
	msg, rdata := response(t, 65281, 65280)
	orig := bytes.Clone(msg)
	got := tamper(msg)
	if !bytes.Equal(msg, orig) {
		t.Fatal("tamper changed the buffer of the caller")
	}
	if len(got) != len(orig) {
		t.Fatalf("length = %d, want %d", len(got), len(orig))
	}
	var changed []int
	for i := range got {
		if got[i] != orig[i] {
			changed = append(changed, i)
		}
	}
	if len(changed) != 1 || changed[0] < rdata || changed[0] >= len(orig)-11 {
		t.Fatalf("changed offsets = %v, want one offset in the RDATA at %d to %d", changed, rdata, len(orig)-12)
	}
}

func TestTamperKeepsTheEnvelopeDecodable(t *testing.T) { // FR-demo-41: the failure is the signature, not the CBOR.
	msg, rdata := response(t, 65281, 65280)
	got := tamper(msg)
	if _, err := codec.Decode(got[rdata:len(got)-11], codec.Limits{}); err != nil {
		t.Fatalf("Decode of the changed envelope: %v", err)
	}
}

func TestTamperLeavesAnAnswerOfAnotherProtocolKeyUnchanged(t *testing.T) { // FR-demo-40
	msg, _ := response(t, 65280, 65280)
	if got := tamper(msg); !bytes.Equal(got, msg) {
		t.Fatal("tamper changed a bootstrap answer")
	}
}

func TestTamperLeavesAMessageThatDoesNotParseUnchanged(t *testing.T) { // FR-demo-40
	msg, _ := response(t, 65281, 65280)
	for _, short := range [][]byte{msg[:5], msg[:20], msg[:len(msg)-40]} {
		if got := tamper(short); !bytes.Equal(got, short) {
			t.Fatalf("tamper changed a message of %d bytes", len(short))
		}
	}
}
