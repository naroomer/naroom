package crypto

import (
	"encoding/json"
	"fmt"
	"net/http"
	"sync"
	"time"
)

// PriceCache caches USD→BTC/LTC rates.
type PriceCache struct {
	mu       sync.RWMutex
	btcPrice float64 // 1 BTC in USD
	ltcPrice float64 // 1 LTC in USD
	updated  time.Time
	ttl      time.Duration
}

func NewPriceCache(ttl time.Duration) *PriceCache {
	return &PriceCache{ttl: ttl}
}

// SetDevPrices seeds fixed prices for dev/test mode, avoiding real API calls.
func (pc *PriceCache) SetDevPrices(btcUSD, ltcUSD float64) {
	pc.mu.Lock()
	defer pc.mu.Unlock()
	pc.btcPrice = btcUSD
	pc.ltcPrice = ltcUSD
	pc.updated = time.Now()
}

// BTCPrice returns current BTC price in USD.
func (pc *PriceCache) BTCPrice() (float64, error) {
	return pc.getBTCPrice()
}

// LTCPrice returns current LTC price in USD.
func (pc *PriceCache) LTCPrice() (float64, error) {
	return pc.getLTCPrice()
}

// BTCAmount converts USD to BTC amount string.
func (pc *PriceCache) BTCAmount(usd float64) (string, error) {
	price, err := pc.getBTCPrice()
	if err != nil {
		return "", err
	}
	btc := usd / price
	return fmt.Sprintf("%.8f", btc), nil
}

// LTCAmount converts USD to LTC amount string.
func (pc *PriceCache) LTCAmount(usd float64) (string, error) {
	price, err := pc.getLTCPrice()
	if err != nil {
		return "", err
	}
	ltc := usd / price
	return fmt.Sprintf("%.8f", ltc), nil
}

func (pc *PriceCache) getBTCPrice() (float64, error) {
	pc.mu.RLock()
	if time.Since(pc.updated) < pc.ttl && pc.btcPrice > 0 {
		p := pc.btcPrice
		pc.mu.RUnlock()
		return p, nil
	}
	pc.mu.RUnlock()
	return pc.refreshBTC()
}

func (pc *PriceCache) getLTCPrice() (float64, error) {
	pc.mu.RLock()
	if time.Since(pc.updated) < pc.ttl && pc.ltcPrice > 0 {
		p := pc.ltcPrice
		pc.mu.RUnlock()
		return p, nil
	}
	pc.mu.RUnlock()
	return pc.refreshLTC()
}

func (pc *PriceCache) refreshBTC() (float64, error) {
	// mempool.space price API
	resp, err := http.Get("https://mempool.space/api/v1/prices")
	if err != nil {
		return 0, fmt.Errorf("btc price: %w", err)
	}
	defer resp.Body.Close()

	var data struct {
		USD float64 `json:"USD"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&data); err != nil {
		return 0, fmt.Errorf("btc price decode: %w", err)
	}

	pc.mu.Lock()
	pc.btcPrice = data.USD
	pc.updated = time.Now()
	pc.mu.Unlock()
	return data.USD, nil
}

func (pc *PriceCache) refreshLTC() (float64, error) {
	resp, err := http.Get("https://api.blockcypher.com/v1/ltc/main")
	if err != nil {
		return 0, fmt.Errorf("ltc price: %w", err)
	}
	defer resp.Body.Close()

	// blockcypher doesn't provide USD price directly,
	// so we use a simple coingecko fallback
	resp2, err := http.Get("https://api.coingecko.com/api/v3/simple/price?ids=litecoin&vs_currencies=usd")
	if err != nil {
		return 0, fmt.Errorf("ltc price: %w", err)
	}
	defer resp2.Body.Close()

	var data struct {
		Litecoin struct {
			USD float64 `json:"usd"`
		} `json:"litecoin"`
	}
	if err := json.NewDecoder(resp2.Body).Decode(&data); err != nil {
		return 0, fmt.Errorf("ltc price decode: %w", err)
	}

	pc.mu.Lock()
	pc.ltcPrice = data.Litecoin.USD
	pc.updated = time.Now()
	pc.mu.Unlock()
	return data.Litecoin.USD, nil
}
