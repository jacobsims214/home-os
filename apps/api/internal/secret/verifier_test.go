package secret

import (
	"bytes"
	"context"
	"strings"
	"testing"
)

func TestHashVerifier_RoundTrip(t *testing.T) {
	ctx := context.Background()
	v := bytes.Repeat([]byte{7}, 32)

	a, err := hashVerifier(ctx, v)
	if err != nil {
		t.Fatal(err)
	}
	b, err := hashVerifier(ctx, v)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.HasPrefix(a, "$argon2id$v=19$m=19456,t=2,p=1$") {
		t.Fatalf("unexpected PHC prefix: %q", a[:30])
	}
	if a == b {
		t.Fatal("two hashes of the same verifier are equal: salt is not random")
	}

	ok, rehash, err := checkVerifier(ctx, a, v)
	if err != nil || !ok || rehash {
		t.Fatalf("check(correct) = ok %v rehash %v err %v", ok, rehash, err)
	}
	wrong := bytes.Repeat([]byte{8}, 32)
	ok, _, err = checkVerifier(ctx, a, wrong)
	if err != nil || ok {
		t.Fatalf("check(wrong) = ok %v err %v", ok, err)
	}
}

func TestCheckVerifier_NeedsRehashOnOldParams(t *testing.T) {
	ctx := context.Background()
	v := bytes.Repeat([]byte{1}, 32)
	saved := currentParams
	currentParams = argonParams{memoryKiB: 8 * 1024, time: 1, threads: 1}
	old, err := hashVerifier(ctx, v)
	currentParams = saved
	if err != nil {
		t.Fatal(err)
	}
	ok, rehash, err := checkVerifier(ctx, old, v)
	if err != nil || !ok || !rehash {
		t.Fatalf("check(old params) = ok %v rehash %v err %v", ok, rehash, err)
	}
}

func TestParsePHC_Rejects(t *testing.T) {
	good, err := hashVerifier(context.Background(), bytes.Repeat([]byte{2}, 32))
	if err != nil {
		t.Fatal(err)
	}
	parts := strings.Split(good, "$")
	salt, hash := parts[4], parts[5]
	cases := map[string]string{
		"empty":          "",
		"argon2i":        "$argon2i$v=19$m=19456,t=2,p=1$" + salt + "$" + hash,
		"bad version":    "$argon2id$v=16$m=19456,t=2,p=1$" + salt + "$" + hash,
		"huge memory":    "$argon2id$v=19$m=4194304,t=2,p=1$" + salt + "$" + hash,
		"huge time":      "$argon2id$v=19$m=19456,t=1000,p=1$" + salt + "$" + hash,
		"zero threads":   "$argon2id$v=19$m=19456,t=2,p=0$" + salt + "$" + hash,
		"missing param":  "$argon2id$v=19$m=19456,t=2$" + salt + "$" + hash,
		"duplicate":      "$argon2id$v=19$m=19456,m=1,t=2$" + salt + "$" + hash,
		"unknown param":  "$argon2id$v=19$m=19456,t=2,p=1,x=1$" + salt + "$" + hash,
		"bad salt b64":   "$argon2id$v=19$m=19456,t=2,p=1$!!$" + hash,
		"short hash":     "$argon2id$v=19$m=19456,t=2,p=1$" + salt + "$AAAA",
		"too many parts": good + "$x",
	}
	for name, enc := range cases {
		t.Run(name, func(t *testing.T) {
			if _, _, _, err := parsePHC(enc); err == nil {
				t.Fatalf("parsePHC accepted %s", name)
			}
		})
	}
	if _, _, _, err := parsePHC(good); err != nil {
		t.Fatalf("parsePHC rejected a good string: %v", err)
	}
}

func TestValidClientVerifier(t *testing.T) {
	for n, want := range map[int]bool{0: false, 15: false, 16: true, 32: true, 128: true, 129: false} {
		if got := validClientVerifier(make([]byte, n)); got != want {
			t.Errorf("len %d: got %v want %v", n, got, want)
		}
	}
}

func TestClientVerifier_PrefersNewField(t *testing.T) {
	if got := clientVerifier([]byte("new"), []byte("old")); string(got) != "new" {
		t.Fatalf("got %q", got)
	}
	if got := clientVerifier(nil, []byte("old")); string(got) != "old" {
		t.Fatalf("got %q", got)
	}
}
