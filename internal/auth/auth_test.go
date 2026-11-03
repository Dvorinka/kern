package auth

import "testing"

func TestPasswordRoundTrip(t *testing.T) {
	hash, err := HashPassword("correct horse battery staple")
	if err != nil {
		t.Fatal(err)
	}
	if !VerifyPassword(hash, "correct horse battery staple") {
		t.Fatal("valid password rejected")
	}
	if VerifyPassword(hash, "wrong password") {
		t.Fatal("invalid password accepted")
	}
	if VerifyPassword("garbage", "x") {
		t.Fatal("malformed hash accepted")
	}
	if _, err := HashPassword("short"); err == nil {
		t.Fatal("short password accepted")
	}
}

func TestTokensAreUniqueAndHashed(t *testing.T) {
	a, hashA, err := NewToken(APIPrefix)
	if err != nil {
		t.Fatal(err)
	}
	b, hashB, _ := NewToken(APIPrefix)
	if a == b || hashA == hashB {
		t.Fatal("tokens are not unique")
	}
	if hashA != HashToken(a) || len(hashA) != 64 {
		t.Fatal("token hash mismatch")
	}
	if TokenPrefix(a) != a[:12] {
		t.Fatal("unexpected prefix")
	}
}
