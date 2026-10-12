package transport

// Auth helpers verify member keys and stamp overlay identity into Status.

import (
	"net"
	"time"

	ecrypto "loft/internal/crypto"
	"loft/internal/identity"
	"loft/internal/mux"
	"loft/internal/wire"
)

// AuthKeys is the node’s member keyring. Empty (or nil) keeps the shared
// LOFT_TOKEN as the client secret so an existing node does not lock out
// until keys are issued. Non-empty: clients authenticate with a member key;
// LOFT_TOKEN remains the node secret (mask, controller).
var AuthKeys *identity.Ring

// clientTokens returns member keys, or fallback (LOFT_TOKEN) when the ring is empty.
func clientTokens(fallback []byte) [][]byte {
	if AuthKeys != nil && AuthKeys.Len() > 0 {
		return AuthKeys.Tokens()
	}
	if len(fallback) > 0 {
		return [][]byte{fallback}
	}
	return nil
}

// frontAuth checks blob against clientTokens and the nonce cache.
func frontAuth(fallback, blob []byte, seen *ecrypto.Nonces) (token, nonce, rest []byte, err error) {
	return ecrypto.CheckAuthAny(clientTokens(fallback), blob, seen)
}

// verifyAny reports whether hello.SessionID matches any clientTokens HMAC.
func verifyAny(fallback []byte, h clientHello) bool {
	for _, tok := range clientTokens(fallback) {
		if verifySessionID(tok, h) {
			return true
		}
	}
	return false
}

// stampOverlayAuth binds the session to the overlay after Hello decode.
func stampOverlayAuth(st *wire.Status, rest []byte, sess *mux.Session, remote net.Addr, token []byte) {
	if NetSwitch == nil || st == nil {
		return
	}
	h, _ := wire.DecodeHello(rest)
	if AuthKeys != nil && AuthKeys.Len() > 0 {
		id := AuthKeys.IDFor(token)
		if id == "" {
			st.Code = wire.AuthBadKey
			return
		}
		h.MemberID = id
	}
	NetSwitch.Bind(st, h, sess, remote)
}

// finishStatus mints a resume ticket and stamps overlay identity onto st.
func finishStatus(st *wire.Status, token []byte, sid string, rest []byte, sess *mux.Session, remote net.Addr) {
	if st == nil {
		return
	}
	if len(token) > 0 && sid != "" {
		st.Ticket = ecrypto.MakeTicket(token, sid, 24*time.Hour)
	}
	stampOverlayAuth(st, rest, sess, remote, token)
}
