package server

import "testing"

// WHO MAY OWN WHAT, and the gap this closes.
//
// The browser used to send `owner: "install"` for every new channel, so a
// non-administrator pressing Add Channel was refused with a 403 and had no way
// to make a channel of their own at all — which is the whole point of a channel
// being owned by a user. The list reply now carries `canManageInstall` and the
// form asks for the ownership the viewer may actually have.
//
// `mayTouchChannel` is the rule itself, and it is the one place the four write
// routes consult, so this is where it is worth pinning.
func TestOnlyAnAdministratorMayOwnAnInstallChannel(t *testing.T) {
	s, _, _ := scopedRoutersServer(t)

	// carol in this harness has router:manage but NOT system:principals, so she
	// is not a global administrator. The harness's own tests rely on that.
	sess := &Session{Username: "carol"}
	if s.isGlobalAdmin(sess) {
		t.Skip("this harness's session is an administrator; the check below would prove nothing")
	}

	if s.mayTouchChannel(sess, "_install") {
		t.Error("a non-administrator was allowed to own an install-wide channel")
	}
	// AND SHE MAY STILL HAVE HER OWN. Refusing both would leave her unable to
	// be notified at all, which is worse than the 403 this replaced.
	own := s.webUserID(sess)
	if own != "" && !s.mayTouchChannel(sess, own) {
		t.Error("a user was refused ownership of their own channel")
	}
	// AND NOT SOMEBODY ELSE'S.
	if s.mayTouchChannel(sess, "some-other-user-id") {
		t.Error("a user was allowed to touch another person's channel")
	}
}
