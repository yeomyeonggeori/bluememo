export const site = {
	name: 'bluememo',
	tagline: 'Long-term memory for an agent serving one person, in one SQLite file.',
	description:
		'bluememo keeps what an agent learns about one person in a single SQLite file: it settles what it hears into self-contained sentences, recalls without a model call, and forgets under pressure.',
	origin: 'https://bluememo.intern.kim',
	repository: { owner: 'yeomyeonggeori', name: 'bluememo', branch: 'main' },
	color: { light: '#1d4ed8', dark: '#60a5fa' },
	gettingStarted: ['index', 'quickstart'],
	icons: {
		index: 'Compass',
		quickstart: 'Rocket',
		concepts: 'Shapes',
		lifecycle: 'Workflow',
		ports: 'Plug',
		configuration: 'SlidersHorizontal',
		questions: 'MessageCircleQuestion',
	} as Record<string, string>,
	groupDescriptions: {} as Record<string, string>,
};
