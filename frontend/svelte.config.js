import adapter from '@sveltejs/adapter-node';

export default {
	kit: {
		adapter: adapter()
	},
	compilerOptions: {
		runes: true
	}
};
