export const V2_SAMPLES = [
	{
		id: 'sample_cannabis',
		dependency_type: 'cannabis',
		help_type: 'crisis',
		languages: ['EN', 'RU'],
		display_name: 'Quiet Harbor · A7KM',
		age_key: 'v2.rep.days_short',
		age_n: 12,
		positive_count: 0,
		negative_count: 0
	},
	{
		id: 'sample_cocaine',
		dependency_type: 'cocaine',
		help_type: 'just_talk',
		languages: ['EN', 'ES'],
		display_name: 'Clear Path · R4NX',
		age_key: 'v2.rep.months_short',
		age_n: 3,
		positive_count: 2,
		negative_count: 0
	},
	{
		id: 'sample_alcohol',
		dependency_type: 'alcohol',
		help_type: 'relapse_prevention',
		languages: ['EN', 'KA'],
		display_name: 'Still River · K9TW',
		age_key: 'v2.rep.weeks_short',
		age_n: 5,
		positive_count: 1,
		negative_count: 0
	}
];

export function sampleListing(id, city) {
	const sample = V2_SAMPLES.find((item) => item.id === id);
	if (!sample) return null;

	return {
		id: sample.id,
		city,
		dependency_type: sample.dependency_type,
		help_type: sample.help_type,
		languages: sample.languages,
		display_name: sample.display_name,
		urgency: 'can_wait',
		client_reputation: {
			member_since: Math.floor(Date.now() / 1000) - sample.age_n * (
				sample.age_key === 'v2.rep.months_short' ? 30 * 86400 :
				sample.age_key === 'v2.rep.weeks_short' ? 7 * 86400 :
				86400
			),
			positive_count: sample.positive_count,
			negative_count: sample.negative_count
		}
	};
}
