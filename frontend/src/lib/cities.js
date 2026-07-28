// FALLBACK_CITY_ID is the only city-related constant V2 pages may use as a static
// fallback. The full V2 city list comes from /api/v2/board/cities.
export const FALLBACK_CITY_ID = 'tbilisi';

// Legacy V1 registry. Keep it unchanged while V2 uses the backend registry.
export const CITIES = [
	{ id: 'buenos_aires', label: 'Buenos Aires' },
	{ id: 'sao_paulo',    label: 'São Paulo' },
	{ id: 'nha_trang',   label: 'Nha Trang' },
	{ id: 'da_nang',     label: 'Da Nang' },
	{ id: 'tbilisi',     label: 'Tbilisi' },
	{ id: 'batumi',      label: 'Batumi' },
	{ id: 'almaty',      label: 'Almaty' },
	{ id: 'yerevan',     label: 'Yerevan' },
	{ id: 'moscow',      label: 'Moscow' },
];
