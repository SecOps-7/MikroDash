package server

import (
	"testing"

	"mikrodash/internal/rbac"
)

// TestNobodyScansWithoutAnAnswerableWriteGrant. mayScan carried two branches
// that answered YES, for sign-in off and for an unavailable grant database, and
// neither could be reached: canPage refuses a router write in both cases first.
// They are gone; this pins the answer they would have contradicted (review loop).
func TestNobodyScansWithoutAnAnswerableWriteGrant(t *testing.T) {
	signInOff := &conn{srv: &Server{rbac: testResolver(t)}, routerID: "r-A",
		sess: &Session{AuthMode: "none", Pages: map[string]string{"wifi-clients": "write"}, Readable: []string{"r-A"}}}
	if signInOff.mayScan() {
		t.Error("sign-in off may scan: a router write needs an account (#97)")
	}
	noDB := &conn{srv: &Server{rbac: rbac.New(nil, nil)}, routerID: "r-A", userID: "u-1",
		sess: &Session{AuthMode: "modern", Pages: map[string]string{"wifi-clients": "write"}, Readable: []string{"r-A"}}}
	if noDB.mayScan() {
		t.Error("with no grant database a scan was allowed; a write fails closed there")
	}
	if (&conn{srv: &Server{}}).mayScan() {
		t.Error("no session may scan")
	}
}
