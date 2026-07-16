process.env.MOCK_CATS_SCENARIO ||= 'showcase';
process.env.MOCK_CATS_ECHO ||= '1';

await import('./local-onboarding-mock-server.mjs');
