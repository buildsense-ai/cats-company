package postgres

import "github.com/openchat/openchat/server/db/wikiinjection"

const createWikiInjections = `
CREATE TABLE IF NOT EXISTS wiki_injections (` + wikiinjection.InjectionColumns + `);
CREATE TABLE IF NOT EXISTS wiki_injection_votes (` + wikiinjection.VoteColumns + `);
`

func (a *Adapter) WikiInjections() *wikiinjection.Storage { return wikiinjection.New(a.db, true) }
