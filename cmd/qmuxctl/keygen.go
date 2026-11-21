package main

import (
	"crypto/ed25519"
	"errors"
	"flag"
	"fmt"
	"io"
	"io/fs"
	"os"

	"github.com/Crank-Git/qmux/pkg/security"
)

// runKeygen runs qmuxctl keygen with the flags in args and returns the exit code.
func runKeygen(name string, args []string, stdout, stderr io.Writer) int {
	usage := name + " keygen"
	synopsis := usage + " --out <prefix>"
	exits := "0 files written, 1 a file exists or a write failed, 64 usage error."
	var out string
	fset := flag.NewFlagSet(usage, flag.ContinueOnError)
	// runKeygen prints the error and the usage, so that --help goes to stdout and an
	// error goes to stderr once.
	fset.SetOutput(io.Discard)
	fset.StringVar(&out, "out", "", "path prefix of the files <prefix>.key and <prefix>.anchor.json (required)")
	err := fset.Parse(args)
	switch {
	case errors.Is(err, flag.ErrHelp):
		if printUsage(stdout, synopsis, exits, fset) != nil {
			return 1
		}
		return exitResult
	case err == nil && fset.NArg() > 0:
		err = fmt.Errorf("unexpected argument %q", fset.Arg(0))
	case err == nil && out == "":
		err = errors.New("missing flag --out")
	}
	if err != nil {
		// The exit code is 64 whether or not stderr accepts the write.
		_, _ = fmt.Fprintf(stderr, "error: %v\n", err)
		_ = printUsage(stderr, synopsis, exits, fset)
		return exitUsage
	}

	id, err := keygen(out+".key", out+".anchor.json")
	if err != nil {
		// The exit code is 1 whether or not stderr accepts the write.
		_, _ = fmt.Fprintf(stderr, "error: %v\n", err)
		return 1
	}
	if _, err := fmt.Fprintf(stdout, "wrote %s.key (mode 0600)\nwrote %s.anchor.json\nkey_id %x\n", out, out, id); err != nil {
		// A caller that reads stdout must not see exit 0 with missing output.
		return 1
	}
	return exitResult
}

// keygen writes a new signing key to keyPath and its trust anchor file to anchorPath,
// and returns the key ID. The trust anchor allows every protocol ID.
//
// keygen returns an error and writes no file when either file exists. When the trust
// anchor file fails to write, keygen removes the key file, so a retry can succeed.
func keygen(keyPath, anchorPath string) ([8]byte, error) {
	for _, p := range []string{keyPath, anchorPath} {
		if _, err := os.Lstat(p); err == nil {
			return [8]byte{}, fmt.Errorf("keygen: %s exists", p)
		} else if !errors.Is(err, fs.ErrNotExist) {
			return [8]byte{}, fmt.Errorf("keygen: %w", err)
		}
	}
	k, err := security.GenerateKey()
	if err != nil {
		return [8]byte{}, fmt.Errorf("keygen: %w", err)
	}
	// WritePrivateKey creates the file with O_EXCL, so a key file that appears after
	// the check above is never replaced.
	if err := security.WritePrivateKey(keyPath, k); err != nil {
		return [8]byte{}, fmt.Errorf("keygen: %w", err)
	}
	pub := k.Public().(ed25519.PublicKey)
	id := security.KeyID(pub)
	if err := security.WriteTrustAnchors(anchorPath, []security.TrustAnchor{{KeyID: id, PublicKey: pub}}); err != nil {
		rerr := os.Remove(keyPath)
		return [8]byte{}, fmt.Errorf("keygen: %w", errors.Join(err, rerr))
	}
	return id, nil
}
