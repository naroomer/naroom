package worker

// PriceFetcher is satisfied by *ncrypto.PriceCache.
// Using an interface here enables test mocking without external API calls.
type PriceFetcher interface {
	BTCPrice() (float64, error)
	LTCPrice() (float64, error)
}
