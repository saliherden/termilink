package security

import (
	"testing"
)

func TestIsAllowed(t *testing.T) {
	a := New(100, []int64{100, 456})

	cases := []struct {
		name   string
		userID int64
		want   bool
	}{
		{"owner", 100, true},
		{"whitelisted", 456, true},
		{"unknown", 789, false},
		{"zero", 0, false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := a.IsAllowed(tc.userID); got != tc.want {
				t.Fatalf("IsAllowed(%d) = %v, want %v", tc.userID, got, tc.want)
			}
		})
	}
}

func TestIsOwner(t *testing.T) {
	a := New(100, []int64{100, 456})
	if !a.IsOwner(100) {
		t.Fatal("owner should be recognized")
	}
	if a.IsOwner(456) {
		t.Fatal("non-owner should not be owner")
	}
	if a.IsOwner(0) {
		t.Fatal("zero should not be owner")
	}
}

func TestNilAuthorizerDenies(t *testing.T) {
	var a *Authorizer
	if a.IsAllowed(123) {
		t.Fatal("nil authorizer must deny all")
	}
	if a.IsOwner(123) {
		t.Fatal("nil authorizer must not own anyone")
	}
}

func TestNewFromConfig(t *testing.T) {
	a, err := NewFromConfig([]byte("allowed_users:\n  - 42\n"))
	if err != nil {
		t.Fatal(err)
	}
	if !a.IsAllowed(42) {
		t.Fatal("NewFromConfig did not parse allowed_users")
	}
	if !a.IsOwner(42) {
		t.Fatal("NewFromConfig should default owner to first allowed user")
	}
	if a.IsAllowed(1) {
		t.Fatal("NewFromConfig leaked an unauthorized user")
	}
}

func TestNewFromConfigExplicitOwner(t *testing.T) {
	a, err := NewFromConfig([]byte("owner: 7\nallowed_users:\n  - 7\n  - 42\n"))
	if err != nil {
		t.Fatal(err)
	}
	if !a.IsOwner(7) {
		t.Fatal("explicit owner not recognized")
	}
	if a.IsOwner(42) {
		t.Fatal("worker should not be owner")
	}
	if !a.IsAllowed(42) {
		t.Fatal("worker should still be allowed")
	}
}
