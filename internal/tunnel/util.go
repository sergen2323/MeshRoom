package tunnel

import (
	"encoding/base64"
	"encoding/hex"
)

// hexDecode переводит hex-ключ из UAPI в base64 — формат, в котором
// ключи ходят в протоколе комнаты и в UI.
func hexDecode(h string) (string, error) {
	raw, err := hex.DecodeString(h)
	if err != nil {
		return "", err
	}
	return base64.StdEncoding.EncodeToString(raw), nil
}
