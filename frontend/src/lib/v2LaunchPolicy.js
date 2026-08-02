export const LAUNCH_DEPENDENCY = 'cannabis';
export const LAUNCH_DEPENDENCIES = [LAUNCH_DEPENDENCY];
export const LAUNCH_CURRENCY = 'LTC';

export function detectLaunchCurrency(address) {
	const value = (address || '').trim();
	if (/^ltc1/i.test(value) || /^[LM]/.test(value)) return LAUNCH_CURRENCY;
	return null;
}

export function isLaunchListing(listing) {
	return listing?.dependency_type === LAUNCH_DEPENDENCY;
}
