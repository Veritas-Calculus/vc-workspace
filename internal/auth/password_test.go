package auth

import "testing"

func TestPasswordRoundTrip(t *testing.T) {
	encoded, err := HashPassword("correct horse battery staple")
	if err != nil {
		t.Fatal(err)
	}
	valid, err := VerifyPassword(encoded, "correct horse battery staple")
	if err != nil || !valid {
		t.Fatalf("expected password to verify, valid=%v err=%v", valid, err)
	}
	valid, err = VerifyPassword(encoded, "wrong password")
	if err != nil || valid {
		t.Fatalf("expected wrong password to fail, valid=%v err=%v", valid, err)
	}
}

func TestOpaqueTokenIsRandom(t *testing.T) {
	left, err := OpaqueToken(32)
	if err != nil {
		t.Fatal(err)
	}
	right, err := OpaqueToken(32)
	if err != nil {
		t.Fatal(err)
	}
	if left == right || len(left) < 40 {
		t.Fatal("tokens should be long and distinct")
	}
}

func TestParseBearerTokenRequiresSchemeAndSingleToken(t *testing.T) {
	for _, test := range []struct {
		header string
		want   string
		ok     bool
	}{
		{header: "Bearer agent-token", want: "agent-token", ok: true},
		{header: "bearer\tagent-token", want: "agent-token", ok: true},
		{header: "agent-token", ok: false},
		{header: "Basic agent-token", ok: false},
		{header: "Bearer", ok: false},
		{header: "Bearer first second", ok: false},
	} {
		token, ok := ParseBearerToken(test.header)
		if ok != test.ok || token != test.want {
			t.Fatalf("ParseBearerToken(%q) = %q, %v; want %q, %v", test.header, token, ok, test.want, test.ok)
		}
	}
}
