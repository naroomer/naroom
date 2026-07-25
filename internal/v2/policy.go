package v2

// V2BalancePolicy holds all configurable USD balance thresholds for the V2 subsystem.
// Use DefaultV2BalancePolicy() to get the production defaults.
// Inject a custom policy via SetPolicy on each service/handler.
type V2BalancePolicy struct {
	// ClientPublicMinUSD is the minimum balance shown to the public on listing cards.
	ClientPublicMinUSD float64
	// ClientHardFloorUSD is the hard floor for post-payment balance recheck.
	ClientHardFloorUSD float64
	// HelperPostPaymentMinUSD is the minimum balance required after Helper payment.
	HelperPostPaymentMinUSD float64
	// InformerMinUSD is the minimum balance required for Informer subscription.
	InformerMinUSD float64
}

// HelperPreInvoiceMinUSD returns the pre-invoice balance floor: HelperPostPaymentMinUSD + 10.
// The $10 buffer covers the invoice fee so the post-payment balance stays above the floor.
func (p V2BalancePolicy) HelperPreInvoiceMinUSD() float64 {
	return p.HelperPostPaymentMinUSD + 10.0
}

// DefaultV2BalancePolicy returns the production balance policy.
func DefaultV2BalancePolicy() V2BalancePolicy {
	return V2BalancePolicy{
		ClientPublicMinUSD:      150.0,
		ClientHardFloorUSD:      120.0,
		HelperPostPaymentMinUSD: 1000.0,
		InformerMinUSD:          1000.0,
	}
}
