// checkaddr — выводит первые 5 BTC и LTC адресов из xpub/zpub.
// Запуск: BTC_XPUB=zpub... LTC_XPUB=Ltub... go run ./cmd/checkaddr
package main

import (
	"crypto/sha256"
	"fmt"
	"os"

	"github.com/btcsuite/btcd/btcutil"
	"github.com/btcsuite/btcd/btcutil/base58"
	"github.com/btcsuite/btcd/btcutil/hdkeychain"
	"github.com/btcsuite/btcd/chaincfg"
)

var segwitPubVersions = map[[4]byte]bool{
	{0x04, 0xB2, 0x47, 0x46}: true,  // BTC zpub
	{0x04, 0x88, 0xB2, 0x1E}: false, // BTC xpub
	{0x01, 0xB2, 0x6E, 0xF6}: true,  // LTC zpub
	{0x01, 0x9D, 0xA4, 0x62}: false, // LTC xpub
}

var btcXpubVersion = [4]byte{0x04, 0x88, 0xB2, 0x1E}

func normalise(pub string) (*hdkeychain.ExtendedKey, bool, error) {
	raw := base58.Decode(pub)
	if len(raw) != 82 {
		return nil, false, fmt.Errorf("invalid key length %d", len(raw))
	}
	var ver [4]byte
	copy(ver[:], raw[:4])
	isSegwit, known := segwitPubVersions[ver]
	if !known {
		return nil, false, fmt.Errorf("unknown version %x", ver)
	}
	if isSegwit {
		copy(raw[:4], btcXpubVersion[:])
		h1 := sha256.Sum256(raw[:78])
		h2 := sha256.Sum256(h1[:])
		copy(raw[78:], h2[:4])
		pub = base58.Encode(raw)
	}
	key, err := hdkeychain.NewKeyFromString(pub)
	return key, isSegwit, err
}

func btcAddr(key *hdkeychain.ExtendedKey, index uint32, segwit bool) string {
	child, err := key.Derive(index)
	if err != nil {
		return fmt.Sprintf("ERROR: %v", err)
	}
	if segwit {
		pk, _ := child.ECPubKey()
		addr, _ := btcutil.NewAddressWitnessPubKeyHash(
			btcutil.Hash160(pk.SerializeCompressed()), &chaincfg.MainNetParams)
		return addr.EncodeAddress()
	}
	addr, _ := child.Address(&chaincfg.MainNetParams)
	return addr.EncodeAddress()
}

func ltcAddr(key *hdkeychain.ExtendedKey, index uint32, segwit bool) string {
	child, err := key.Derive(index)
	if err != nil {
		return fmt.Sprintf("ERROR: %v", err)
	}
	if segwit {
		pk, _ := child.ECPubKey()
		ltcParams := &chaincfg.Params{Bech32HRPSegwit: "ltc"}
		addr, _ := btcutil.NewAddressWitnessPubKeyHash(
			btcutil.Hash160(pk.SerializeCompressed()), ltcParams)
		return addr.EncodeAddress()
	}
	ltcLegacyParams := &chaincfg.Params{PubKeyHashAddrID: 0x30}
	addr, _ := child.Address(ltcLegacyParams)
	return addr.EncodeAddress()
}

func main() {
	btcXpub := os.Getenv("BTC_XPUB")
	ltcXpub := os.Getenv("LTC_XPUB")

	if btcXpub != "" {
		key, segwit, err := normalise(btcXpub)
		if err != nil {
			fmt.Printf("BTC key error: %v\n", err)
		} else {
			fmt.Println("=== BTC addresses (что сервер выставит в инвойсах) ===")
			for i := uint32(0); i < 5; i++ {
				fmt.Printf("  [%d] %s\n", i, btcAddr(key, i, segwit))
			}
			fmt.Println()

			// Также показываем адреса через external chain (0/i) — стандарт Trezor
			ext, err2 := key.Derive(0)
			if err2 == nil {
				fmt.Println("=== BTC через external chain /0/i (стандарт Trezor receive) ===")
				for i := uint32(0); i < 5; i++ {
					fmt.Printf("  [0/%d] %s\n", i, btcAddr(ext, i, segwit))
				}
				fmt.Println()
			}
		}
	}

	if ltcXpub != "" {
		key, segwit, err := normalise(ltcXpub)
		if err != nil {
			fmt.Printf("LTC key error: %v\n", err)
		} else {
			fmt.Println("=== LTC addresses (что сервер выставит в инвойсах) ===")
			for i := uint32(0); i < 5; i++ {
				fmt.Printf("  [%d] %s\n", i, ltcAddr(key, i, segwit))
			}
			fmt.Println()

			ext, err2 := key.Derive(0)
			if err2 == nil {
				fmt.Println("=== LTC через external chain /0/i (стандарт Trezor receive) ===")
				for i := uint32(0); i < 5; i++ {
					fmt.Printf("  [0/%d] %s\n", i, ltcAddr(ext, i, segwit))
				}
				fmt.Println()
			}
		}
	}

	fmt.Println("Сравни адреса из секции 'что сервер выставит' с адресами в Trezor Suite.")
	fmt.Println("Если совпадают — всё ок. Если совпадают только /0/i — нужно исправить код.")
}
