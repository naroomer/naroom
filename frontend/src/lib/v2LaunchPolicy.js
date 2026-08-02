export const LAUNCH_DEPENDENCY = 'cannabis';
export const LAUNCH_DEPENDENCIES = [LAUNCH_DEPENDENCY];
export const LAUNCH_CURRENCY = 'LTC';

// Public launch requirements shown to users. Backend policy remains the
// authority for enforcement and can use lower thresholds during acceptance.
export const LAUNCH_DISPLAY_LIMITS = Object.freeze({
	clientPublicMinUSD: 150,
	clientHardFloorUSD: 120,
	helperPreInvoiceMinUSD: 1010,
	helperPostPaymentMinUSD: 1000,
	informerMinUSD: 1000,
});

export function detectLaunchCurrency(address) {
	const value = (address || '').trim();
	if (/^ltc1/i.test(value) || /^[LM]/.test(value)) return LAUNCH_CURRENCY;
	return null;
}

export function isLaunchListing(listing) {
	return listing?.dependency_type === LAUNCH_DEPENDENCY;
}
