import { redirect } from '@sveltejs/kit';

export function load({ params }) {
	throw redirect(308, '/v2/listing/' + params.id);
}
