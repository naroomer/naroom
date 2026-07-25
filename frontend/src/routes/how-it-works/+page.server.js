import { redirect } from '@sveltejs/kit';
import { CITIES } from '$lib/cities.js';

export function load() {
	throw redirect(308, '/v2/board/' + CITIES[0].id);
}
