package cryptobox

import (
	"regexp"
	"testing"
)

// Invite codes are the one secret members type or paste; the hub hashes the lowercased path, so a code must be
// lowercase base32 and exactly 26 characters or the link never matches (see docs/adr/0001).
func TestInviteCodeShape(t *testing.T) {
	re := regexp.MustCompile(`^[a-z2-7]{26}$`)
	seen := map[string]bool{}
	for i := 0; i < 200; i++ {
		c := NewInviteCode()
		if !re.MatchString(c) {
			t.Fatalf("code %q is not 26 lowercase base32 characters", c)
		}
		if seen[c] {
			t.Fatalf("duplicate code %q", c)
		}
		seen[c] = true
	}
}

func TestHashTokenIsDeterministicAndOpaque(t *testing.T) {
	a, b := HashToken("abc"), HashToken("abc")
	if a != b {
		t.Fatal("same input, different hash")
	}
	if a == "abc" || len(a) < 32 {
		t.Fatalf("hash %q looks like the input", a)
	}
	if HashToken("abd") == a {
		t.Fatal("different input, same hash")
	}
}

func TestSealWithCodeRoundTripAndWrongCode(t *testing.T) {
	type keys struct{ Password, Salt string }
	in := keys{Password: NewCircleSecret(), Salt: NewCircleSecret()}
	code := NewInviteCode()
	sealed, err := SealWithCode(code, in)
	if err != nil {
		t.Fatal(err)
	}
	var out keys
	if err := OpenWithCode(code, sealed, &out); err != nil {
		t.Fatal(err)
	}
	if out != in {
		t.Fatalf("round trip changed the keys: %+v vs %+v", out, in)
	}
	var bad keys
	if err := OpenWithCode(NewInviteCode(), sealed, &bad); err == nil {
		t.Fatal("a different code opened the blob")
	}
}

func TestSealToDeviceOnlyThatDeviceOpens(t *testing.T) {
	dev, err := NewDeviceKeys()
	if err != nil {
		t.Fatal(err)
	}
	other, _ := NewDeviceKeys()
	sealed, err := SealToDevice(dev.Public, map[string]string{"k": "v"})
	if err != nil {
		t.Fatal(err)
	}
	var out map[string]string
	if err := OpenFromDevice(dev, sealed, &out); err != nil || out["k"] != "v" {
		t.Fatalf("device could not open its own grant: %v %v", err, out)
	}
	if err := OpenFromDevice(other, sealed, &out); err == nil {
		t.Fatal("another device opened the grant")
	}
}

func TestSignVerify(t *testing.T) {
	pub, priv := NewSigningKey()
	msg := []byte(SHA256Hex([]byte("release bundle")))
	sig, err := Sign(priv, msg)
	if err != nil {
		t.Fatal(err)
	}
	if !Verify(pub, msg, sig) {
		t.Fatal("valid signature rejected")
	}
	if Verify(pub, []byte("tampered"), sig) {
		t.Fatal("signature accepted for a different message")
	}
	otherPub, _ := NewSigningKey()
	if Verify(otherPub, msg, sig) {
		t.Fatal("signature accepted under another key")
	}
}

func TestEncryptWithKeyRoundTrip(t *testing.T) {
	key := RandomBytes(32)
	ct, err := EncryptWithKey(key, []byte("hello"))
	if err != nil {
		t.Fatal(err)
	}
	pt, err := DecryptWithKey(key, ct)
	if err != nil || string(pt) != "hello" {
		t.Fatalf("decrypt: %v %q", err, pt)
	}
	if _, err := DecryptWithKey(RandomBytes(32), ct); err == nil {
		t.Fatal("wrong key decrypted")
	}
}
