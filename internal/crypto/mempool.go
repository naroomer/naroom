package crypto

import (
	"encoding/json"
	"fmt"
	"net/http"
	"time"
)

// MempoolClient talks to mempool.space API for BTC.
type MempoolClient struct {
	baseURL    string
	httpClient *http.Client
}

func NewMempoolClient(baseURL string) *MempoolClient {
	return &MempoolClient{
		baseURL: baseURL,
		httpClient: &http.Client{
			Timeout: 15 * time.Second,
		},
	}
}

// GetBalance returns confirmed balance in satoshis for a BTC address.
func (m *MempoolClient) GetBalance(address string) (int64, error) {
	url := fmt.Sprintf("%s/address/%s", m.baseURL, address)
	req, err := http.NewRequest("GET", url, nil)
	if err != nil {
		return 0, fmt.Errorf("mempool balance: %w", err)
	}
	req.Header.Set("User-Agent", "Mozilla/5.0 (compatible; naroom/1.0)")
	resp, err := m.httpClient.Do(req)
	if err != nil {
		return 0, fmt.Errorf("mempool balance: %w", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode == 429 {
		return 0, fmt.Errorf("mempool rate limited")
	}
	if resp.StatusCode != 200 {
		return 0, fmt.Errorf("mempool status %d", resp.StatusCode)
	}

	var data struct {
		ChainStats struct {
			FundedSum int64 `json:"funded_txo_sum"`
			SpentSum  int64 `json:"spent_txo_sum"`
		} `json:"chain_stats"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&data); err != nil {
		return 0, fmt.Errorf("mempool decode: %w", err)
	}

	return data.ChainStats.FundedSum - data.ChainStats.SpentSum, nil
}

// GetReceivedByAddress checks if a specific address received any transaction.
func (m *MempoolClient) GetReceivedByAddress(address string) ([]MempoolTx, error) {
	url := fmt.Sprintf("%s/address/%s/txs", m.baseURL, address)
	resp, err := m.httpClient.Get(url)
	if err != nil {
		return nil, fmt.Errorf("mempool txs: %w", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != 200 {
		return nil, fmt.Errorf("mempool txs status %d", resp.StatusCode)
	}

	var txs []MempoolTx
	if err := json.NewDecoder(resp.Body).Decode(&txs); err != nil {
		return nil, fmt.Errorf("mempool txs decode: %w", err)
	}
	return txs, nil
}

// MempoolTx is a simplified transaction from mempool.space.
type MempoolTx struct {
	TxID   string `json:"txid"`
	Status struct {
		Confirmed   bool  `json:"confirmed"`
		BlockHeight int64 `json:"block_height"`
	} `json:"status"`
	Vout []struct {
		ScriptPubkeyAddress string `json:"scriptpubkey_address"`
		Value               int64  `json:"value"` // satoshis
	} `json:"vout"`
	Vin []struct {
		Prevout struct {
			ScriptPubkeyAddress string `json:"scriptpubkey_address"`
		} `json:"prevout"`
	} `json:"vin"`
}

// FindPayment looks for a confirmed payment of at least minSatoshis to the given address.
// Returns the transaction, amount in satoshis, and all unique sender addresses from all inputs.
// BTC transactions can have multiple inputs from different addresses — callers must check all of them.
func (m *MempoolClient) FindPayment(address string, minSatoshis int64) (*MempoolTx, int64, []string, error) {
	txs, err := m.GetReceivedByAddress(address)
	if err != nil {
		return nil, 0, nil, err
	}

	for i := range txs {
		tx := &txs[i]
		if !tx.Status.Confirmed {
			continue
		}
		for _, vout := range tx.Vout {
			if vout.ScriptPubkeyAddress == address && vout.Value >= minSatoshis {
				var senders []string
				seen := map[string]bool{}
				for _, vin := range tx.Vin {
					if addr := vin.Prevout.ScriptPubkeyAddress; addr != "" && !seen[addr] {
						senders = append(senders, addr)
						seen[addr] = true
					}
				}
				return tx, vout.Value, senders, nil
			}
		}
	}

	return nil, 0, nil, nil
}
