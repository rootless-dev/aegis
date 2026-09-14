package key

// Ref is the pair a JWS header carries: which key signed, and under which alg.
type Ref struct {
	KID       string
	Algorithm Algorithm
}
