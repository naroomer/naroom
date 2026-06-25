package crypto

import (
	"crypto/sha256"
	"database/sql"
	"fmt"

	"github.com/btcsuite/btcd/btcutil"
	"github.com/btcsuite/btcd/btcutil/base58"
	"github.com/btcsuite/btcd/btcutil/hdkeychain"
	"github.com/btcsuite/btcd/chaincfg"
)

// litecoinMainNetParams — минимальный набор параметров для LTC P2PKH адресов.
var litecoinMainNetParams = &chaincfg.Params{
	Name:             "ltc-mainnet",
	PubKeyHashAddrID: 0x30, // L...
	ScriptHashAddrID: 0x32, // M...
}

// HDWallet деривирует BTC/LTC адреса из xpub/zpub для приёма платежей.
type HDWallet struct {
	db         *sql.DB
	btcKey     *hdkeychain.ExtendedKey
	btcSegwit  bool // true → генерировать bech32 (bc1...) вместо legacy (1...)
	ltcKey     *hdkeychain.ExtendedKey
	ltcSegwit  bool // true → генерировать bech32 LTC (ltc1...) вместо legacy (L...)
}

// zpubVersions: известные SegWit-версии extended public key → заменяем на xpub-версию
// чтобы hdkeychain мог распарсить, а адрес генерируем сами через P2WPKH.
var segwitPubVersions = map[[4]byte]bool{
	{0x04, 0xB2, 0x47, 0x46}: true, // BTC zpub (BIP84)
	{0x04, 0x88, 0xB2, 0x1E}: false, // BTC xpub (BIP44) — legacy
	{0x01, 0xB2, 0x6E, 0xF6}: true, // LTC zpub (BIP84)
	{0x01, 0x9D, 0xA4, 0x62}: false, // LTC xpub — legacy
}

// btcXpubVersion — стандартная xpub версия mainnet
var btcXpubVersion = [4]byte{0x04, 0x88, 0xB2, 0x1E}

// normaliseExtKey принимает xpub или zpub (BTC/LTC), возвращает hdkeychain.ExtendedKey
// и флаг segwit. zpub конвертируется в xpub заменой версии (ключи идентичны).
func normaliseExtKey(pub string) (*hdkeychain.ExtendedKey, bool, error) {
	raw := base58.Decode(pub)
	if len(raw) != 82 {
		return nil, false, fmt.Errorf("invalid key length %d", len(raw))
	}

	var ver [4]byte
	copy(ver[:], raw[:4])

	isSegwit, known := segwitPubVersions[ver]
	if !known {
		return nil, false, fmt.Errorf("unknown key version %x", ver)
	}

	if isSegwit {
		// Заменяем версию на стандартный xpub чтобы hdkeychain мог прочитать
		copy(raw[:4], btcXpubVersion[:])
		h1 := sha256.Sum256(raw[:78])
		h2 := sha256.Sum256(h1[:])
		copy(raw[78:], h2[:4])
		pub = base58.Encode(raw)
	}

	key, err := hdkeychain.NewKeyFromString(pub)
	if err != nil {
		return nil, false, err
	}
	return key, isSegwit, nil
}

// NewHDWallet создаёт HDWallet. Если ключ пустой — работает в dev-режиме
// (возвращает placeholder-адреса). Принимает xpub и zpub форматы.
func NewHDWallet(db *sql.DB, btcXpub, ltcXpub string) (*HDWallet, error) {
	w := &HDWallet{db: db}

	if btcXpub != "" {
		key, segwit, err := normaliseExtKey(btcXpub)
		if err != nil {
			return nil, fmt.Errorf("parse BTC key: %w", err)
		}
		w.btcKey = key
		w.btcSegwit = segwit
	}

	if ltcXpub != "" {
		key, segwit, err := normaliseExtKey(ltcXpub)
		if err != nil {
			return nil, fmt.Errorf("parse LTC key: %w", err)
		}
		w.ltcKey = key
		w.ltcSegwit = segwit
	}

	return w, nil
}

// NextBTCAddress возвращает следующий уникальный BTC адрес.
// Если ключ — zpub, генерирует bech32 (bc1...). Иначе legacy (1...).
func (w *HDWallet) NextBTCAddress() (address string, index uint32, err error) {
	index, err = w.incrementIndex("BTC")
	if err != nil {
		return "", 0, err
	}

	if w.btcKey == nil {
		return fmt.Sprintf("btc_dev_%d", index), index, nil
	}

	child, err := w.btcKey.Derive(index)
	if err != nil {
		return "", 0, fmt.Errorf("derive BTC[%d]: %w", index, err)
	}

	if w.btcSegwit {
		pubKey, err := child.ECPubKey()
		if err != nil {
			return "", 0, fmt.Errorf("BTC pubkey[%d]: %w", index, err)
		}
		addr, err := btcutil.NewAddressWitnessPubKeyHash(
			btcutil.Hash160(pubKey.SerializeCompressed()),
			&chaincfg.MainNetParams,
		)
		if err != nil {
			return "", 0, fmt.Errorf("BTC bech32[%d]: %w", index, err)
		}
		return addr.EncodeAddress(), index, nil
	}

	addr, err := child.Address(&chaincfg.MainNetParams)
	if err != nil {
		return "", 0, fmt.Errorf("BTC address[%d]: %w", index, err)
	}
	return addr.EncodeAddress(), index, nil
}

// NextLTCAddress возвращает следующий уникальный LTC адрес.
// Если ключ — zpub, генерирует bech32 (ltc1...). Иначе legacy (L...).
func (w *HDWallet) NextLTCAddress() (address string, index uint32, err error) {
	index, err = w.incrementIndex("LTC")
	if err != nil {
		return "", 0, err
	}

	if w.ltcKey == nil {
		return fmt.Sprintf("ltc_dev_%d", index), index, nil
	}

	child, err := w.ltcKey.Derive(index)
	if err != nil {
		return "", 0, fmt.Errorf("derive LTC[%d]: %w", index, err)
	}

	if w.ltcSegwit {
		pubKey, err := child.ECPubKey()
		if err != nil {
			return "", 0, fmt.Errorf("LTC pubkey[%d]: %w", index, err)
		}
		// LTC bech32: hrp = "ltc"
		ltcSegwitParams := &chaincfg.Params{Bech32HRPSegwit: "ltc"}
		addr, err := btcutil.NewAddressWitnessPubKeyHash(
			btcutil.Hash160(pubKey.SerializeCompressed()),
			ltcSegwitParams,
		)
		if err != nil {
			return "", 0, fmt.Errorf("LTC bech32[%d]: %w", index, err)
		}
		return addr.EncodeAddress(), index, nil
	}

	addr, err := child.Address(litecoinMainNetParams)
	if err != nil {
		return "", 0, fmt.Errorf("LTC address[%d]: %w", index, err)
	}
	return addr.EncodeAddress(), index, nil
}

// incrementIndex атомарно увеличивает счётчик и возвращает текущее значение.
func (w *HDWallet) incrementIndex(currency string) (uint32, error) {
	tx, err := w.db.Begin()
	if err != nil {
		return 0, fmt.Errorf("incrementIndex begin: %w", err)
	}
	defer tx.Rollback()

	var idx uint32
	err = tx.QueryRow(`SELECT next_index FROM invoice_index WHERE currency = ?`, currency).Scan(&idx)
	if err == sql.ErrNoRows {
		idx = 0
		if _, err = tx.Exec(`INSERT INTO invoice_index (currency, next_index) VALUES (?, 1)`, currency); err != nil {
			return 0, err
		}
	} else if err != nil {
		return 0, err
	} else {
		if _, err = tx.Exec(`UPDATE invoice_index SET next_index = next_index + 1 WHERE currency = ?`, currency); err != nil {
			return 0, err
		}
	}

	return idx, tx.Commit()
}
