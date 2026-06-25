package crypto

import (
	"encoding/json"
	"fmt"
	"net/http"
	"time"
)

// BlockcypherClient talks to blockcypher API for LTC.
type BlockcypherClient struct {
	baseURL    string
	httpClient *http.Client
}

func NewBlockcypherClient(baseURL string) *BlockcypherClient {
	return &BlockcypherClient{
		baseURL: baseURL,
		httpClient: &http.Client{
			Timeout: 15 * time.Second,
		},
	}
}

// GetBalance returns confirmed balance in litoshis for an LTC address.
func (b *BlockcypherClient) GetBalance(address string) (int64, error) {
	url := fmt.Sprintf("%s/addrs/%s/balance", b.baseURL, address)
	resp, err := b.httpClient.Get(url)
	if err != nil {
		return 0, fmt.Errorf("blockcypher balance: %w", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode == 429 {
		return 0, fmt.Errorf("blockcypher rate limited")
	}
	if resp.StatusCode != 200 {
		return 0, fmt.Errorf("blockcypher status %d", resp.StatusCode)
	}

	var data struct {
		Balance int64 `json:"balance"` // confirmed
	}
	if err := json.NewDecoder(resp.Body).Decode(&data); err != nil {
		return 0, fmt.Errorf("blockcypher decode: %w", err)
	}

	return data.Balance, nil
}

// GetTransactions returns transactions for an LTC address.
func (b *BlockcypherClient) GetTransactions(address string) ([]BlockcypherTx, error) {
	url := fmt.Sprintf("%s/addrs/%s/full?limit=10", b.baseURL, address)
	resp, err := b.httpClient.Get(url)
	if err != nil {
		return nil, fmt.Errorf("blockcypher txs: %w", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != 200 {
		return nil, fmt.Errorf("blockcypher txs status %d", resp.StatusCode)
	}

	var data struct {
		Txs []BlockcypherTx `json:"txs"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&data); err != nil {
		return nil, fmt.Errorf("blockcypher txs decode: %w", err)
	}

	return data.Txs, nil
}

type BlockcypherTx struct {
	Hash          string `json:"hash"`
	Confirmations int    `json:"confirmations"`
	Outputs       []struct {
		Addresses []string `json:"addresses"`
		Value     int64    `json:"value"` // litoshis
	} `json:"outputs"`
	Inputs []struct {
		Addresses []string `json:"addresses"`
	} `json:"inputs"`
}

// FindPayment looks for a confirmed payment to the given address.
// Returns the transaction, amount in litoshis, and all unique sender addresses from all inputs.
// LTC transactions can have multiple inputs from different addresses — callers must check all of them.
func (b *BlockcypherClient) FindPayment(address string, minLitoshis int64) (*BlockcypherTx, int64, []string, error) {
	txs, err := b.GetTransactions(address)
	if err != nil {
		return nil, 0, nil, err
	}

	for i := range txs {
		tx := &txs[i]
		if tx.Confirmations < 1 {
			continue
		}
		for _, out := range tx.Outputs {
			for _, addr := range out.Addresses {
				if addr == address && out.Value >= minLitoshis {
					var senders []string
					seen := map[string]bool{}
					for _, inp := range tx.Inputs {
						for _, a := range inp.Addresses {
							if a != "" && !seen[a] {
								senders = append(senders, a)
								seen[a] = true
							}
						}
					}
					return tx, out.Value, senders, nil
				}
			}
		}
	}

	return nil, 0, nil, nil
}
