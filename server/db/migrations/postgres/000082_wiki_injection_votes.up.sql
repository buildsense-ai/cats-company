CREATE TABLE IF NOT EXISTS wiki_injections (
    bot_uid bigint not null, event_id varchar(64) not null,
    topic_id varchar(128) not null, detail text not null, primary key(bot_uid, event_id)
);
CREATE TABLE IF NOT EXISTS wiki_injection_votes (
    bot_uid bigint not null, event_id varchar(64) not null,
    viewer_uid bigint not null, wiki_id varchar(128) not null, vote smallint not null check (vote in (-1,1)),
    primary key(bot_uid, event_id, viewer_uid, wiki_id),
    foreign key(bot_uid, event_id) references wiki_injections(bot_uid, event_id) on delete cascade
);
