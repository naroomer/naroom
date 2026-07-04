package crypto

import (
	"encoding/json"
	"fmt"
	"net/http"
	"time"
)

// BlockchairClient checks LTC balances via api.blockchair.com.
// No API key required for basic usage; rate limit is ~1 req/sec.
// Used as a fallback when BlockCypher is rate-limited.
type BlockchairClient struct {
	httpClient *http.Client
}

func NewBlockchairClient() *BlockchairClient {
	return &BlockchairClient{
		httpClient: &http.Client{Timeout: 15 * time.Second},
	}
}

// GetLTCBalance returns confirmed balance in litoshis for an LTC address.
func (b *BlockchairClient) GetLTCBalance(address string) (int64, error) {
	url := fmt.Sprintf("https://api.blockchair.com/litecoin/dashboards/address/%s", address)
	resp, err := b.httpClient.Get(url)
	if err != nil {
		return 0, fmt.Errorf("blockchair: %w", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode == 429 {
		return 0, fmt.Errorf("blockchair rate limited")
	}
	if resp.StatusCode != 200 {
		return 0, fmt.Errorf("blockchair status %d", resp.StatusCode)
	}

	var data struct {
		Data map[string]struct {
			Address struct {
				Balance int64 `json:"balance"`
			} `json:"address"`
		} `json:"data"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&data); err != nil {
		return 0, fmt.Errorf("blockchair decode: %w", err)
	}

	for _, v := range data.Data {
		return v.Address.Balance, nil
	}
	return 0, nil
}
