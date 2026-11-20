package protocol

// FoldName returns name with each ASCII letter A to Z in lowercase and every other
// byte unchanged. Use it before you compare two DNS names or use a DNS name as a key.
//
// DNS case folding is ASCII only (RFC 4343 § 3). A Unicode fold maps the Kelvin
// sign U+212A to k and each invalid UTF-8 byte to U+FFFD, so two different names
// match.
func FoldName(name string) string {
	b := []byte(name)
	for i, c := range b {
		if 'A' <= c && c <= 'Z' {
			b[i] = c + ('a' - 'A')
		}
	}
	return string(b)
}
