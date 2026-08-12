package otp

import "testing"

func TestDecryptLegacyMastodonFixture(t *testing.T) {
	const (
		encrypted = "Fttsy7QAa0edaDfdfSz094rRLAxc8cJweDQ4BsWH/zozcdVA8o9GLqcKhn2b\nGi/V\n"
		iv        = "rys3THICkr60BoWC"
		salt      = "_LMkAGvdg7a+sDIKjI3mR2Q=="
		key       = "100c7faeef00caa29242f6b04156742bf76065771fd4117990c4282b8748ff3d99f8fdae97c982ab5bd2e6756a159121377cce4421f4a8ecd2d67bd7749a3fb4"
	)
	got, err := DecryptLegacyMastodon(encrypted, iv, salt, key)
	if err != nil {
		t.Fatal(err)
	}
	if got != "anotpsecretthatshouldbeencrypted" {
		t.Fatalf("plaintext = %q", got)
	}
}

func TestParseLegacyPaon(t *testing.T) {
	if got, ok := ParseLegacyPaon(" paon-go-totp: abcd ef23 "); !ok || got != "abcd ef23" {
		t.Fatalf("secret = %q ok = %v", got, ok)
	}
	if _, ok := ParseLegacyPaon("paon-go-totp: "); ok {
		t.Fatal("empty legacy Paon secret accepted")
	}
}
