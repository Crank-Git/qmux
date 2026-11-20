package codec

import (
	"bytes"
	"os"
	"path/filepath"
	"testing"
)

// FuzzDecodeEnvelope requires that Decode never panics. It also requires that each
// envelope Decode accepts encodes back to the input bytes. Every v1 golden vector
// is a seed.
func FuzzDecodeEnvelope(f *testing.F) {
	paths, err := filepath.Glob(filepath.Join(vectorDir, "*.cbor"))
	if err != nil {
		f.Fatalf("list vectors: %v", err)
	}
	if len(paths) == 0 {
		f.Fatalf("%s holds no vectors to seed the corpus", vectorDir)
	}
	for _, p := range paths {
		b, err := os.ReadFile(p)
		if err != nil {
			f.Fatalf("read %s: %v", p, err)
		}
		f.Add(b)
	}
	f.Fuzz(func(t *testing.T, b []byte) {
		e, err := Decode(b, Limits{})
		if err != nil {
			return
		}
		again, err := Encode(e, Limits{})
		if err != nil {
			t.Fatalf("Decode accepted % x, but Encode returns %v", b, err)
		}
		if !bytes.Equal(again, b) {
			t.Fatalf("Decode accepted % x, but Encode returns % x", b, again)
		}
	})
}
