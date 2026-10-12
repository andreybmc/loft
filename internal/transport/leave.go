package transport

// CmdLeave batch helpers: encode and AEAD-seal a Leave so GET/WS can end the session.

import (
	ecrypto "loft/internal/crypto"
	"loft/internal/wire"
)

// encodeLeave is a one-frame mux batch with CmdLeave.
func encodeLeave() ([]byte, error) {
	return wire.EncodeBatch([]wire.Frame{{Cmd: wire.CmdLeave}})
}

// sealLeave AEAD-seals that batch (GET uses AAD = session id).
func sealLeave(key, aad []byte) ([]byte, error) {
	raw, err := encodeLeave()
	if err != nil {
		return nil, err
	}
	return ecrypto.Seal(key, raw, aad)
}
