package middleware

type contextKey int

// Every context key of this package lives in this block: iota restarts on each
// const declaration, so keys declared apart would silently share the value zero
// and overwrite one another.
const (
	requestIDContextKey contextKey = iota + 1
)
