import { error } from '@sveltejs/kit';

export async function load({ params }) {
	const res = await fetch(`http://localhost:8080/listing/${params.id}`);
	if (!res.ok) {
		throw error(404, 'Listing not found');
	}
	const listing = await res.json();
	return { listing };
}
