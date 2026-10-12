package transport

// ClientHello peek, SNI/key_share parse, and loft session_id HMAC.

import (
	"crypto/hmac"
	"crypto/sha256"
	"encoding/binary"
	"errors"
	"io"
	"strings"

	"golang.org/x/crypto/hkdf"
)

// clientHello is the fields decoy and mask need from a TLS ClientHello.
type clientHello struct {
	Random    []byte
	SessionID []byte
	SNI       string
	KeyShare  []byte // X25519 public, 32 bytes
	ALPNs     []string
}

// readClientHello concatenates handshake records until one ClientHello is complete.
func readClientHello(r io.Reader) (raw []byte, h clientHello, err error) {
	var hs []byte
	for {
		rec, err := readTLSRecord(r)
		if err != nil {
			return raw, h, err
		}
		raw = append(raw, rec...)
		if rec[0] != 0x16 {
			return raw, h, errors.New("not tls handshake")
		}
		hs = append(hs, rec[5:]...)
		if len(hs) < 4 {
			continue
		}
		n := int(hs[1])<<16 | int(hs[2])<<8 | int(hs[3])
		if n < 0 || n > 16<<10 {
			return raw, h, errors.New("bad hello length")
		}
		if len(hs) >= 4+n {
			h, err = parseClientHello(hs[:4+n])
			return raw, h, err
		}
	}
}

// readTLSRecord reads one TLS record (header plus body).
func readTLSRecord(r io.Reader) ([]byte, error) {
	hdr := make([]byte, 5)
	if _, err := io.ReadFull(r, hdr); err != nil {
		return nil, err
	}
	n := int(binary.BigEndian.Uint16(hdr[3:5]))
	if n < 0 || n > 16<<10 {
		return hdr, errors.New("bad record length")
	}
	body := make([]byte, n)
	if _, err := io.ReadFull(r, body); err != nil {
		return append(hdr, body[:0]...), err
	}
	return append(hdr, body...), nil
}

// writeTLSRecord writes one TLS record of typ.
func writeTLSRecord(w io.Writer, typ byte, payload []byte) error {
	hdr := []byte{typ, 0x03, 0x03, byte(len(payload) >> 8), byte(len(payload))}
	_, err := w.Write(append(hdr, payload...))
	return err
}

// parseClientHello extracts random, session_id, SNI, ALPN, and X25519 key_share.
func parseClientHello(body []byte) (clientHello, error) {
	var h clientHello
	if len(body) < 38 || body[0] != 0x01 {
		return h, errors.New("not client hello")
	}
	// handshake header 4, version 2, random 32
	off := 4 + 2
	if off+32 > len(body) {
		return h, errors.New("short random")
	}
	h.Random = append([]byte(nil), body[off:off+32]...)
	off += 32
	if off >= len(body) {
		return h, errors.New("short sid")
	}
	sidLen := int(body[off])
	off++
	if off+sidLen > len(body) {
		return h, errors.New("short sid body")
	}
	h.SessionID = append([]byte(nil), body[off:off+sidLen]...)
	off += sidLen
	if off+2 > len(body) {
		return h, nil
	}
	csLen := int(binary.BigEndian.Uint16(body[off : off+2]))
	off += 2 + csLen
	if off >= len(body) {
		return h, nil
	}
	cmLen := int(body[off])
	off++
	off += cmLen
	if off+2 > len(body) {
		return h, nil
	}
	extLen := int(binary.BigEndian.Uint16(body[off : off+2]))
	off += 2
	end := off + extLen
	if end > len(body) {
		end = len(body)
	}
	for off+4 <= end {
		typ := binary.BigEndian.Uint16(body[off : off+2])
		ln := int(binary.BigEndian.Uint16(body[off+2 : off+4]))
		off += 4
		if off+ln > end {
			break
		}
		if typ == 0 && ln >= 5 { // SNI
			list := body[off : off+ln]
			if len(list) >= 5 && list[2] == 0 {
				nlen := int(binary.BigEndian.Uint16(list[3:5]))
				if 5+nlen <= len(list) {
					h.SNI = string(list[5 : 5+nlen])
				}
			}
		}
		if typ == 16 { // ALPN
			h.ALPNs = parseALPN(body[off : off+ln])
		}
		if typ == 51 { // key_share
			h.KeyShare = parseX25519Share(body[off : off+ln])
		}
		off += ln
	}
	return h, nil
}

// parseALPN decodes the ALPN extension payload.
func parseALPN(b []byte) []string {
	if len(b) < 2 {
		return nil
	}
	n := int(binary.BigEndian.Uint16(b[0:2]))
	off := 2
	end := 2 + n
	if end > len(b) {
		end = len(b)
	}
	var out []string
	for off < end {
		ln := int(b[off])
		off++
		if off+ln > end {
			break
		}
		out = append(out, string(b[off:off+ln]))
		off += ln
	}
	return out
}

// parseX25519Share returns the 32-byte X25519 public from key_share, or nil.
func parseX25519Share(b []byte) []byte {
	if len(b) < 2 {
		return nil
	}
	n := int(binary.BigEndian.Uint16(b[0:2]))
	off := 2
	end := 2 + n
	if end > len(b) {
		end = len(b)
	}
	for off+4 <= end {
		group := binary.BigEndian.Uint16(b[off : off+2])
		ln := int(binary.BigEndian.Uint16(b[off+2 : off+4]))
		off += 4
		if off+ln > end {
			break
		}
		if group == 0x001d && ln == 32 { // x25519
			return append([]byte(nil), b[off:off+32]...)
		}
		off += ln
	}
	return nil
}

// sessionHMAC is 32 bytes of HKDF, indistinguishable from Chrome's random
// session_id. It binds token, client_random, SNI and key_share so a leaked
// HMAC(token, random) recipe from older drafts does not match.
func sessionHMAC(token []byte, h clientHello) []byte {
	info := append([]byte("loft-sid-v2\x00"), []byte(strings.ToLower(h.SNI))...)
	info = append(info, h.KeyShare...)
	out := make([]byte, 32)
	if _, err := io.ReadFull(hkdf.New(sha256.New, token, h.Random, info), out); err != nil {
		m := hmac.New(sha256.New, token)
		m.Write(h.Random)
		return m.Sum(nil)[:32]
	}
	return out
}

// verifySessionID reports whether h.SessionID equals sessionHMAC(token, h).
func verifySessionID(token []byte, h clientHello) bool {
	if len(h.SessionID) != 32 || len(h.Random) != 32 {
		return false
	}
	return hmac.Equal(h.SessionID, sessionHMAC(token, h))
}
