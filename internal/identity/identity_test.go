package identity

import (
	"os"
	"testing"

	"meshroom/internal/store"
)

func TestMain(m *testing.M) {
	// изолируем каталог данных от настоящего профиля пользователя
	tmp, _ := os.MkdirTemp("", "mr-test-*")
	os.Setenv("XDG_CONFIG_HOME", tmp)
	os.Setenv("HOME", tmp)
	os.Setenv("AppData", tmp)
	code := m.Run()
	os.RemoveAll(tmp)
	os.Exit(code)
}

func TestCreateUnlock(t *testing.T) {
	p, err := Create("Серёжа", "", "secret123")
	if err != nil {
		t.Fatal(err)
	}
	if !p.Unlocked() {
		t.Fatal("fresh profile must be unlocked")
	}
	priv1, err := p.PrivateHex()
	if err != nil || len(priv1) != 64 {
		t.Fatalf("bad priv: %q %v", priv1, err)
	}

	loaded, err := Load()
	if err != nil {
		t.Fatal(err)
	}
	if loaded.Unlocked() {
		t.Fatal("loaded profile must be locked")
	}
	if err := loaded.Unlock("wrong"); err != ErrWrongPassword {
		t.Fatalf("want ErrWrongPassword, got %v", err)
	}
	if err := loaded.Unlock("secret123"); err != nil {
		t.Fatal(err)
	}
	priv2, _ := loaded.PrivateHex()
	if priv1 != priv2 {
		t.Fatal("private key changed after reload")
	}
	if loaded.PubKey != p.PubKey {
		t.Fatal("pubkey mismatch")
	}
	_ = store.Dir()
}
