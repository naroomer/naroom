import { redirect } from '@sveltejs/kit';

export function load() {
	throw redirect(308, '/v2/new?fresh=1');
}
