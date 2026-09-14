package key

type JWK struct {
	Kty string `json:"kty"`
	Use string `json:"use"`
	Alg string `json:"alg"`
	Kid string `json:"kid"`

	// RSA.
	N string `json:"n,omitempty"`
	E string `json:"e,omitempty"`

	// EC.
	Crv string `json:"crv,omitempty"`
	X   string `json:"x,omitempty"`
	Y   string `json:"y,omitempty"`
}

func (k *Key) JWK() (JWK, error) {
	members, err := requiredMembers(k.publicKey)
	if err != nil {
		return JWK{}, err
	}

	return JWK{
		Kty: members["kty"],
		Use: k.purpose.String(),
		Alg: k.algorithm.String(),
		Kid: k.kid,
		N:   members["n"],
		E:   members["e"],
		Crv: members["crv"],
		X:   members["x"],
		Y:   members["y"],
	}, nil
}
