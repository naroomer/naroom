import { error } from '@sveltejs/kit';

const BACKEND = process.env.BACKEND_URL ?? 'http://localhost:8080';

export async function load({ params }) {
	const res = await fetch(`${BACKEND}/listing/${params.id}`);
	if (!res.ok) {
		throw error(404, 'Listing not found');
	}
	const listing = await res.json();
	return { listing };
}
