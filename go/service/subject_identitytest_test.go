//go:build oa_identitytest

package service

import (
	identity "github.com/openabstractions/abstraction-identity"
	"github.com/openabstractions/abstraction-identity/identitytest"
	"testing"
)

func TestSubjectIdentityUnknownUIDRefused(t *testing.T) {
	peer := identitytest.New(t, identitytest.Facts{User: identity.User{Kind: "posix", UID: -1}})[0]
	if forbidden, err := (&Host{owner: "-1"}).admits(peer); err != nil || !forbidden {
		t.Fatal(forbidden, err)
	}
}
