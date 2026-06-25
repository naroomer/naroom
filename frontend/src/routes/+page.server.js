import { redirect } from '@sveltejs/kit';
import { CITIES } from '$lib/cities.js';

export function load() {
	throw redirect(302, '/board/' + CITIES[0].id);
}
