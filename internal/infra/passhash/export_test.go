package passhash

var Encode = encode

// NewWithCeilings lets the fuzz target bound what a generated input can cost.
func NewWithCeilings(params Params, memory, time uint32) (*Hasher, error) {
	return newWithCeilings(params, memory, time)
}
