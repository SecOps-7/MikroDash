package server

// authFor is an Auth that knows one token. Tests used to write a session into
// the Node-era validator's cache; the validator is the local resolver now, so a
// test installs one.
func authFor(token string, sess *Session) *Auth {
	a := NewAuth()
	a.SetLocal(func(t string) (*Session, bool) {
		if t == token {
			return sess, true
		}
		return nil, false
	})
	return a
}
