// Package identity — локальный профиль пользователя без регистрации:
// никнейм, аватар, пароль и ключевая пара WireGuard (Curve25519).
// Приватный ключ хранится на диске зашифрованным от пароля (argon2id + AES-GCM).
package identity

import (
	"crypto/aes"
	"crypto/cipher"
	"crypto/rand"
	"encoding/base64"
	"encoding/hex"
	"errors"
	"io"

	"golang.org/x/crypto/argon2"
	"golang.org/x/crypto/curve25519"

	"meshroom/internal/store"
)

const profileFile = "profile.json"

var (
	// ErrWrongPassword — неверный пароль профиля.
	ErrWrongPassword = errors.New("wrong password")
	// ErrNoProfile — профиль ещё не создан.
	ErrNoProfile = errors.New("no profile")
	// ErrLocked — профиль не разблокирован.
	ErrLocked = errors.New("profile locked")
)

// Profile — данные профиля на диске.
type Profile struct {
	Name   string `json:"name"`
	Avatar string `json:"avatar,omitempty"` // data-URL (маленькое изображение)
	PubKey string `json:"pubkey"`           // base64(32 байта), формат WireGuard

	KDFSalt    string `json:"kdfSalt"`
	KDFTime    uint32 `json:"kdfTime"`
	KDFMemory  uint32 `json:"kdfMemory"`
	KDFThreads uint8  `json:"kdfThreads"`
	PrivNonce  string `json:"privNonce"`
	PrivEnc    string `json:"privEnc"` // AES-GCM(privkey)

	priv []byte // расшифрованный приватный ключ (только в памяти)
}

// Exists сообщает, создан ли профиль.
func Exists() bool { return store.Exists(profileFile) }

// Create создаёт профиль: генерирует ключи WireGuard и шифрует приватный ключ паролем.
func Create(name, avatar, password string) (*Profile, error) {
	if name == "" {
		return nil, errors.New("empty name")
	}
	priv := make([]byte, 32)
	if _, err := io.ReadFull(rand.Reader, priv); err != nil {
		return nil, err
	}
	// клампинг Curve25519 по спецификации WireGuard
	priv[0] &= 248
	priv[31] &= 127
	priv[31] |= 64
	pub, err := curve25519.X25519(priv, curve25519.Basepoint)
	if err != nil {
		return nil, err
	}

	p := &Profile{
		Name:       name,
		Avatar:     avatar,
		PubKey:     base64.StdEncoding.EncodeToString(pub),
		KDFTime:    3,
		KDFMemory:  64 * 1024,
		KDFThreads: 2,
		priv:       priv,
	}
	salt := make([]byte, 16)
	if _, err := io.ReadFull(rand.Reader, salt); err != nil {
		return nil, err
	}
	p.KDFSalt = base64.StdEncoding.EncodeToString(salt)
	key := argon2.IDKey([]byte(password), salt, p.KDFTime, p.KDFMemory, p.KDFThreads, 32)
	block, _ := aes.NewCipher(key)
	gcm, _ := cipher.NewGCM(block)
	nonce := make([]byte, gcm.NonceSize())
	if _, err := io.ReadFull(rand.Reader, nonce); err != nil {
		return nil, err
	}
	p.PrivNonce = base64.StdEncoding.EncodeToString(nonce)
	p.PrivEnc = base64.StdEncoding.EncodeToString(gcm.Seal(nil, nonce, priv, nil))

	if err := store.Save(profileFile, p); err != nil {
		return nil, err
	}
	return p, nil
}

// Load читает профиль с диска (в заблокированном виде).
func Load() (*Profile, error) {
	p := &Profile{}
	if err := store.Load(profileFile, p); err != nil {
		if store.IsNotExist(err) {
			return nil, ErrNoProfile
		}
		return nil, err
	}
	return p, nil
}

// Unlock расшифровывает приватный ключ паролем.
func (p *Profile) Unlock(password string) error {
	salt, err := base64.StdEncoding.DecodeString(p.KDFSalt)
	if err != nil {
		return err
	}
	key := argon2.IDKey([]byte(password), salt, p.KDFTime, p.KDFMemory, p.KDFThreads, 32)
	block, _ := aes.NewCipher(key)
	gcm, _ := cipher.NewGCM(block)
	nonce, err := base64.StdEncoding.DecodeString(p.PrivNonce)
	if err != nil {
		return err
	}
	ct, err := base64.StdEncoding.DecodeString(p.PrivEnc)
	if err != nil {
		return err
	}
	priv, err := gcm.Open(nil, nonce, ct, nil)
	if err != nil {
		return ErrWrongPassword
	}
	p.priv = priv
	return nil
}

// Unlocked сообщает, разблокирован ли профиль.
func (p *Profile) Unlocked() bool { return len(p.priv) == 32 }

// PrivateHex — приватный ключ в hex (формат UAPI WireGuard).
func (p *Profile) PrivateHex() (string, error) {
	if !p.Unlocked() {
		return "", ErrLocked
	}
	return hex.EncodeToString(p.priv), nil
}

// UpdateInfo обновляет ник и аватар и сохраняет профиль.
func (p *Profile) UpdateInfo(name, avatar string) error {
	if name != "" {
		p.Name = name
	}
	if avatar != "" {
		p.Avatar = avatar
	}
	return store.Save(profileFile, p)
}

// PubKeyHex — публичный ключ в hex (формат UAPI WireGuard).
func PubKeyHex(b64 string) (string, error) {
	b, err := base64.StdEncoding.DecodeString(b64)
	if err != nil || len(b) != 32 {
		return "", errors.New("bad pubkey")
	}
	return hex.EncodeToString(b), nil
}
