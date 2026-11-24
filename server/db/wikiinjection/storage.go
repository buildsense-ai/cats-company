package wikiinjection

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"strconv"
	"strings"
)

const InjectionColumns = `bot_uid bigint not null, event_id varchar(64) not null,
	topic_id varchar(128) not null, detail text not null, primary key(bot_uid, event_id)`
const VoteColumns = `bot_uid bigint not null, event_id varchar(64) not null,
	viewer_uid bigint not null, wiki_id varchar(128) not null, vote smallint not null check (vote in (-1,1)),
	primary key(bot_uid, event_id, viewer_uid, wiki_id),
	foreign key(bot_uid, event_id) references wiki_injections(bot_uid, event_id) on delete cascade`

type Vote struct {
	WikiID string `json:"wiki_id"`
	Up     int    `json:"up"`
	Down   int    `json:"down"`
}

type Storage struct {
	db       *sql.DB
	postgres bool
}

func New(db *sql.DB, postgres bool) *Storage { return &Storage{db: db, postgres: postgres} }

func (s *Storage) bind(query string) string {
	if !s.postgres {
		return query
	}
	parts := strings.Split(query, "?")
	var bound strings.Builder
	bound.WriteString(parts[0])
	for i, part := range parts[1:] {
		bound.WriteByte('$')
		bound.WriteString(strconv.Itoa(i + 1))
		bound.WriteString(part)
	}
	return bound.String()
}

func (s *Storage) Save(ctx context.Context, bot int64, event, topic string, detail json.RawMessage) error {
	query := `insert into wiki_injections(bot_uid,event_id,topic_id,detail) values (?,?,?,?)`
	if s.postgres {
		query += ` on conflict(bot_uid,event_id) do nothing`
	} else {
		query += ` on duplicate key update event_id=wiki_injections.event_id`
	}
	_, err := s.db.ExecContext(ctx, s.bind(query), bot, event, topic, string(detail))
	return err
}

func (s *Storage) Load(ctx context.Context, bot int64, event string) (string, json.RawMessage, error) {
	var topic, detail string
	err := s.db.QueryRowContext(ctx, s.bind(`select topic_id,detail from wiki_injections where bot_uid=? and event_id=?`), bot, event).Scan(&topic, &detail)
	if errors.Is(err, sql.ErrNoRows) {
		return "", nil, nil
	}
	return topic, json.RawMessage(detail), err
}

func (s *Storage) ViewerVotes(ctx context.Context, bot int64, event string, viewer int64) (map[string]int, error) {
	rows, err := s.db.QueryContext(ctx, s.bind(`select wiki_id,vote from wiki_injection_votes where bot_uid=? and event_id=? and viewer_uid=?`), bot, event, viewer)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	votes := make(map[string]int)
	for rows.Next() {
		var wiki string
		var vote int
		if err := rows.Scan(&wiki, &vote); err != nil {
			return nil, err
		}
		votes[wiki] = vote
	}
	return votes, rows.Err()
}

func (s *Storage) SetVote(ctx context.Context, bot int64, event string, viewer int64, wiki string, vote int) error {
	if vote == 0 {
		_, err := s.db.ExecContext(ctx, s.bind(`delete from wiki_injection_votes where bot_uid=? and event_id=? and viewer_uid=? and wiki_id=?`), bot, event, viewer, wiki)
		return err
	}
	query := `insert into wiki_injection_votes(bot_uid,event_id,viewer_uid,wiki_id,vote) values (?,?,?,?,?)`
	if s.postgres {
		query += ` on conflict(bot_uid,event_id,viewer_uid,wiki_id) do update set vote=excluded.vote`
	} else {
		query += ` on duplicate key update vote=values(vote)`
	}
	_, err := s.db.ExecContext(ctx, s.bind(query), bot, event, viewer, wiki, vote)
	return err
}

func (s *Storage) Totals(ctx context.Context, bot int64, events []string) (map[string][]Vote, error) {
	args := make([]any, 1, 1+len(events))
	args[0] = bot
	for _, event := range events {
		args = append(args, event)
	}
	query := `select event_id,wiki_id,sum(case when vote=1 then 1 else 0 end),
		sum(case when vote=-1 then 1 else 0 end) from wiki_injection_votes
		where bot_uid=? and event_id in (` + strings.TrimSuffix(strings.Repeat("?,", len(events)), ",") + `) group by event_id,wiki_id`
	rows, err := s.db.QueryContext(ctx, s.bind(query), args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	totals := make(map[string][]Vote)
	for rows.Next() {
		var event string
		var vote Vote
		if err := rows.Scan(&event, &vote.WikiID, &vote.Up, &vote.Down); err != nil {
			return nil, err
		}
		totals[event] = append(totals[event], vote)
	}
	return totals, rows.Err()
}
