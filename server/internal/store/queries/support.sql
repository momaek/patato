-- 工单 -------------------------------------------------------------------

-- name: CreateTicket :one
INSERT INTO tickets (user_id, subject) VALUES ($1, $2) RETURNING *;

-- name: GetTicket :one
SELECT * FROM tickets WHERE id = $1;

-- name: TicketVersionForUpdate :one
SELECT version FROM tickets WHERE id = $1 FOR UPDATE;

-- name: ListTickets :many
SELECT t.*, u.email, (d.ticket_id IS NOT NULL)::boolean AS has_draft
FROM tickets t
JOIN users u ON u.id = t.user_id
LEFT JOIN ticket_drafts d ON d.ticket_id = t.id
WHERE (sqlc.narg(status)::text IS NULL OR t.status = sqlc.narg(status)::text)
  AND (sqlc.narg(needs_human)::boolean IS NULL OR t.needs_human = sqlc.narg(needs_human)::boolean)
  AND (sqlc.narg(user_id)::bigint IS NULL OR t.user_id = sqlc.narg(user_id)::bigint)
  AND (sqlc.narg(before_id)::bigint IS NULL OR t.id < sqlc.narg(before_id)::bigint)
ORDER BY t.id DESC
LIMIT sqlc.arg(row_limit)::integer;

-- name: ListUserTickets :many
SELECT * FROM tickets WHERE user_id = $1 ORDER BY last_message_at DESC LIMIT 100;

-- name: AddTicketMessage :one
INSERT INTO ticket_messages (ticket_id, author, operator_id, body, drafted_by)
VALUES ($1, $2, $3, $4, $5)
RETURNING *;

-- name: ListTicketMessages :many
SELECT m.*, o.name AS operator_name
FROM ticket_messages m LEFT JOIN operators o ON o.id = m.operator_id
WHERE m.ticket_id = $1 ORDER BY m.id;

-- name: SetTicketStatus :one
UPDATE tickets SET status = $2, needs_human = $3, needs_human_reason = $4,
    closed_at = CASE WHEN $2 = 'closed' THEN now() ELSE NULL END,
    last_message_at = CASE WHEN sqlc.arg(touch)::boolean THEN now() ELSE last_message_at END,
    updated_at = now(), version = version + 1
WHERE id = $1
RETURNING *;

-- name: UpsertTicketDraft :one
INSERT INTO ticket_drafts (ticket_id, body, drafted_by, basis, change_id, updated_at)
VALUES ($1, $2, $3, $4, $5, now())
ON CONFLICT (ticket_id) DO UPDATE SET body = EXCLUDED.body, drafted_by = EXCLUDED.drafted_by,
    basis = EXCLUDED.basis, change_id = EXCLUDED.change_id, updated_at = now()
RETURNING *;

-- name: GetTicketDraft :one
SELECT * FROM ticket_drafts WHERE ticket_id = $1;

-- name: DeleteTicketDraft :exec
DELETE FROM ticket_drafts WHERE ticket_id = $1;

-- name: TicketCounts :one
SELECT count(*) FILTER (WHERE status = 'open') AS open,
       count(*) FILTER (WHERE status <> 'closed' AND needs_human) AS needs_human
FROM tickets;
