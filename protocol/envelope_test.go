package protocol

import "testing"

func TestEnvelopeRoundTrip(t *testing.T) {
	key, err := DeriveKey("correct horse battery staple")
	if err != nil {
		t.Fatalf("DeriveKey: %v", err)
	}

	plaintext := []byte("hello from the client role")
	sealed, err := SealEnvelope(key, plaintext)
	if err != nil {
		t.Fatalf("SealEnvelope: %v", err)
	}

	got, ok, err := OpenEnvelope(key, sealed)
	if err != nil {
		t.Fatalf("OpenEnvelope: unexpected error: %v", err)
	}
	if !ok {
		t.Fatal("OpenEnvelope: ok=false for a freshly sealed envelope")
	}
	if string(got) != string(plaintext) {
		t.Fatalf("OpenEnvelope: got %q, want %q", got, plaintext)
	}
}

func TestEnvelopeTamperedCiphertextRejected(t *testing.T) {
	key, _ := DeriveKey("correct horse battery staple")
	sealed, err := SealEnvelope(key, []byte("some batch bytes"))
	if err != nil {
		t.Fatalf("SealEnvelope: %v", err)
	}

	// Flip a byte inside the ciphertext (past magic+nonce) so GCM auth must fail.
	tampered := append([]byte(nil), sealed...)
	last := len(tampered) - 1
	tampered[last] ^= 0xFF

	_, ok, err := OpenEnvelope(key, tampered)
	if err != nil {
		t.Fatalf("OpenEnvelope: unexpected error on tampered data: %v", err)
	}
	if ok {
		t.Fatal("OpenEnvelope: ok=true for tampered ciphertext, want false")
	}
}

func TestEnvelopeWrongKeyRejected(t *testing.T) {
	key1, _ := DeriveKey("secret-one")
	key2, _ := DeriveKey("secret-two")

	sealed, err := SealEnvelope(key1, []byte("payload"))
	if err != nil {
		t.Fatalf("SealEnvelope: %v", err)
	}

	_, ok, err := OpenEnvelope(key2, sealed)
	if err != nil {
		t.Fatalf("OpenEnvelope: unexpected error with wrong key: %v", err)
	}
	if ok {
		t.Fatal("OpenEnvelope: ok=true with the wrong key, want false")
	}
}

func TestEnvelopeNotAnEnvelope(t *testing.T) {
	key, _ := DeriveKey("correct horse battery staple")

	cases := [][]byte{
		nil,
		[]byte("short"),
		[]byte("this is definitely not a vtel envelope, wrong magic entirely"),
	}
	for _, data := range cases {
		_, ok, err := OpenEnvelope(key, data)
		if err != nil {
			t.Fatalf("OpenEnvelope(%q): unexpected error: %v", data, err)
		}
		if ok {
			t.Fatalf("OpenEnvelope(%q): ok=true, want false", data)
		}
	}
}

func TestDeriveKeyDeterministicAndDistinct(t *testing.T) {
	k1, err := DeriveKey("same-secret")
	if err != nil {
		t.Fatalf("DeriveKey: %v", err)
	}
	k2, err := DeriveKey("same-secret")
	if err != nil {
		t.Fatalf("DeriveKey: %v", err)
	}
	if string(k1) != string(k2) {
		t.Fatal("DeriveKey: same secret produced different keys")
	}
	if len(k1) != keyLen {
		t.Fatalf("DeriveKey: got %d-byte key, want %d", len(k1), keyLen)
	}

	k3, err := DeriveKey("different-secret")
	if err != nil {
		t.Fatalf("DeriveKey: %v", err)
	}
	if string(k1) == string(k3) {
		t.Fatal("DeriveKey: different secrets produced the same key")
	}

	if _, err := DeriveKey(""); err == nil {
		t.Fatal(`DeriveKey(""): expected an error, got nil`)
	}
}
