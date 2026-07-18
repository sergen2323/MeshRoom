package proto

import (
	"bufio"
	"crypto/cipher"
	"crypto/rand"
	"crypto/sha256"
	"encoding/base64"
	"encoding/binary"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"sync"
	"time"

	"golang.org/x/crypto/chacha20poly1305"
)

// SecureConn шифрует \n-JSON-сообщения поверх TCP секретом комнаты (PSK).
// Кадр: [4 байта длина][nonce 24][ciphertext]. Ключ = SHA-256(PSK).
// Это защищает control-канал (чат, ключи, эндпоинты) от чтения и подмены;
// сам WG-трафик шифруется собственным Noise-протоколом WireGuard.
type SecureConn struct {
	c    net.Conn
	aead cipher.AEAD
	r    *bufio.Reader
	wmu  sync.Mutex
}

const maxFrame = 4 << 20 // 4 МБ: хватает на аватары в истории чата

// NewSecureConn оборачивает соединение шифрованием от PSK (base64url).
func NewSecureConn(c net.Conn, psk string) (*SecureConn, error) {
	raw, err := base64.RawURLEncoding.DecodeString(psk)
	if err != nil {
		return nil, fmt.Errorf("bad psk: %w", err)
	}
	key := sha256.Sum256(raw)
	aead, err := chacha20poly1305.NewX(key[:])
	if err != nil {
		return nil, err
	}
	return &SecureConn{c: c, aead: aead, r: bufio.NewReaderSize(c, 64<<10)}, nil
}

// Send шифрует и отправляет одно сообщение. Безопасен для конкурентных вызовов.
func (s *SecureConn) Send(t string, data any) error {
	plain, err := Enc(t, data)
	if err != nil {
		return err
	}
	s.wmu.Lock()
	defer s.wmu.Unlock()
	nonce := make([]byte, s.aead.NonceSize())
	if _, err := io.ReadFull(rand.Reader, nonce); err != nil {
		return err
	}
	ct := s.aead.Seal(nil, nonce, plain, nil)
	frame := make([]byte, 4+len(nonce)+len(ct))
	binary.BigEndian.PutUint32(frame, uint32(len(nonce)+len(ct)))
	copy(frame[4:], nonce)
	copy(frame[4+len(nonce):], ct)
	_ = s.c.SetWriteDeadline(time.Now().Add(15 * time.Second))
	_, err = s.c.Write(frame)
	return err
}

// Recv читает и расшифровывает одно сообщение.
// deadline == 0 — ждать без ограничения времени.
func (s *SecureConn) Recv(deadline time.Duration) (*Envelope, error) {
	if deadline > 0 {
		_ = s.c.SetReadDeadline(time.Now().Add(deadline))
	} else {
		_ = s.c.SetReadDeadline(time.Time{})
	}
	var lenBuf [4]byte
	if _, err := io.ReadFull(s.r, lenBuf[:]); err != nil {
		return nil, err
	}
	n := binary.BigEndian.Uint32(lenBuf[:])
	if n > maxFrame || n < uint32(s.aead.NonceSize()) {
		return nil, errors.New("bad frame size")
	}
	buf := make([]byte, n)
	if _, err := io.ReadFull(s.r, buf); err != nil {
		return nil, err
	}
	nonce := buf[:s.aead.NonceSize()]
	plain, err := s.aead.Open(nil, nonce, buf[s.aead.NonceSize():], nil)
	if err != nil {
		return nil, errors.New("decrypt failed (wrong room key?)")
	}
	e := &Envelope{}
	if err := json.Unmarshal(plain, e); err != nil {
		return nil, err
	}
	return e, nil
}

// Close закрывает соединение.
func (s *SecureConn) Close() error { return s.c.Close() }

// RemoteAddr — адрес собеседника.
func (s *SecureConn) RemoteAddr() net.Addr { return s.c.RemoteAddr() }
