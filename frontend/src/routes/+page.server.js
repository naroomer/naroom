import { redirect } from '@sveltejs/kit';

export function load() {
	throw redirect(308, '/v2/board/buenos_aires');
}
