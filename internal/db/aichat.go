package db

// The assistant's conversation history, per person per router.
//
// ── WHY IT IS STORED AT ALL, HAVING DELIBERATELY NOT BEEN ───────────────────
//
// The AI Agent page kept its transcript in the browser and nowhere else, and
// said so on screen. That made every question a first question: the assistant
// could not be asked "and what about the other one", because it had never heard
// the first. Persisting it reverses that decision, and it carries the cost the
// original reasoning named — a transcript holds host names, addresses and the
// shape of somebody's network, and it now lands in mikrodash.db and in every
// backup of that file.
//
// ── SCOPED TO A PERSON AND A ROUTER ─────────────────────────────────────────
//
// Both halves matter. A thread that followed a router switch would replay one
// device's discussion while the model answered about another, which reads as
// confident nonsense rather than as an obvious fault. And a transcript belongs
// to the person who typed it: `user_id` is part of the key, not merely recorded,
// so one operator's questions are not another's to read.
//
// ── IT FOLLOWS audit_events, NOT THE METRICS ────────────────────────────────
//
// Deliberately absent from `routerDataTables` in purge.go, and recorded in
// `routerPurgeExcluded` with the reason: removing a router clears its
// time-series data, and doing the same here would erase conversations as a side
// effect of fleet maintenance. Age removes a thread, and so does the operator
// clearing their own — which is the distinction purge.go already draws between a
// record nobody may withdraw and data somebody owns.

import (
	"errors"
	"fmt"
	"time"
)

// AIMessage is one turn of a conversation.
//
// TOOL CALLS ARE NOT TURNS. Only what the operator can see is kept: their
// questions and the assistant's answers. A tool result is raw router rows, stale
// by the next question and bulky on disk, and replaying one would hand the model
// yesterday's table as though it were current.
type AIMessage struct {
	TS   int64
	Role string
	Text string
}

const (
	AIRoleUser      = "user"
	AIRoleAssistant = "assistant"
)

// AppendAIMessage records one turn.
//
// A FAILURE HERE MUST NOT COST THE ANSWER. The caller logs and carries on: the
// operator asked a question and is owed the reply, and a database that cannot be
// written is a reason to lose the history rather than the conversation.
func (d *DB) AppendAIMessage(userID, routerID, role, text string) error {
	if d == nil || d.sql == nil {
		return errors.New("db not open")
	}
	if userID == "" || routerID == "" {
		return fmt.Errorf("ai message: user %q router %q, both are part of the key", userID, routerID)
	}
	if role != AIRoleUser && role != AIRoleAssistant {
		// The column carries a CHECK, so a bad role would fail at SQLite as a
		// constraint error naming no value. Refusing here says which one was wrong.
		return fmt.Errorf("ai message: role %q is neither %q nor %q", role, AIRoleUser, AIRoleAssistant)
	}
	if text == "" {
		return nil // nothing was said; nothing to remember
	}
	_, err := d.sql.Exec(
		`INSERT INTO ai_messages (ts, user_id, router_id, role, text) VALUES (?, ?, ?, ?, ?)`,
		time.Now().UnixMilli(), userID, routerID, role, text)
	return err
}

// RecentAIMessages returns the last `limit` turns of one thread, OLDEST FIRST.
//
// ── THE ORDER IS THE WHOLE POINT OF THE SUBQUERY ────────────────────────────
//
// "The most recent ten" needs a descending sort, and a prompt needs them in the
// order they were said. Sorting ascending and taking ten would return the ten
// OLDEST turns — a conversation that never advances past its opening, which is
// worse than no history because it looks like history.
func (d *DB) RecentAIMessages(userID, routerID string, limit int) ([]AIMessage, error) {
	if d == nil || d.sql == nil {
		return nil, errors.New("db not open")
	}
	if userID == "" || routerID == "" || limit <= 0 {
		return nil, nil
	}
	rows, err := d.sql.Query(
		`SELECT ts, role, text FROM (
           SELECT ts, id, role, text FROM ai_messages
           WHERE user_id = ? AND router_id = ?
           ORDER BY ts DESC, id DESC
           LIMIT ?
         ) ORDER BY ts ASC, id ASC`,
		userID, routerID, limit)
	if err != nil {
		return nil, err
	}
	defer func() { _ = rows.Close() }()

	out := []AIMessage{}
	for rows.Next() {
		var m AIMessage
		if err := rows.Scan(&m.TS, &m.Role, &m.Text); err != nil {
			return nil, err
		}
		out = append(out, m)
	}
	return out, rows.Err()
}

// DeleteAIThread removes one person's conversation about one router.
//
// Scoped to both, so Clear can never reach another operator's thread or another
// router's. That is also why a row id is never the handle: there is no shape of
// call here that aims a delete at somebody else's history.
func (d *DB) DeleteAIThread(userID, routerID string) (int64, error) {
	if d == nil || d.sql == nil {
		return 0, errors.New("db not open")
	}
	if userID == "" || routerID == "" {
		return 0, nil
	}
	res, err := d.sql.Exec(
		`DELETE FROM ai_messages WHERE user_id = ? AND router_id = ?`, userID, routerID)
	if err != nil {
		return 0, err
	}
	return res.RowsAffected()
}
