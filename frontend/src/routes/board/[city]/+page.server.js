const BACKEND = 'http://localhost:8080';

export async function load({ params }) {
	const city = params.city;

	let listings = [];
	try {
		const res = await fetch(`${BACKEND}/board/${city}`);
		if (res.ok) listings = await res.json();
	} catch {
		// backend недоступен — показываем пустую доску
	}

	return { city, listings };
}
