-- 订单 -------------------------------------------------------------------

-- name: CreateOrder :one
INSERT INTO orders (trade_no, user_id, plan_id, plan_name, period, kind, amount, discount, balance_used,
                    total, coupon_id, method, source, note, expires_at)
VALUES ($1, $2, $3, $4, $5, $6, $7, $8, $9, $10, $11, $12, $13, $14, $15)
RETURNING *;

-- name: GetOrder :one
SELECT * FROM orders WHERE id = $1;

-- name: GetOrderByTradeNo :one
SELECT * FROM orders WHERE trade_no = $1;

-- name: LockOrderByTradeNo :one
SELECT * FROM orders WHERE trade_no = $1 FOR UPDATE;

-- name: OrderVersionForUpdate :one
SELECT version FROM orders WHERE id = $1 FOR UPDATE;

-- name: ListOrders :many
SELECT o.*, u.email
FROM orders o JOIN users u ON u.id = o.user_id
WHERE (sqlc.narg(status)::text IS NULL OR o.status = sqlc.narg(status)::text)
  AND (sqlc.narg(user_id)::bigint IS NULL OR o.user_id = sqlc.narg(user_id)::bigint)
  AND (sqlc.narg(q)::text IS NULL OR o.trade_no = sqlc.narg(q)::text OR u.email ILIKE '%' || sqlc.narg(q)::text || '%')
  AND (sqlc.narg(before_id)::bigint IS NULL OR o.id < sqlc.narg(before_id)::bigint)
ORDER BY o.id DESC
LIMIT sqlc.arg(row_limit)::integer;

-- name: ListUserOrders :many
SELECT * FROM orders WHERE user_id = $1 ORDER BY id DESC LIMIT $2;

-- name: SetOrderPaid :one
UPDATE orders SET status = 'paid', method = $2, gateway_trade_no = $3, days = $4, prev_expires_at = $5,
    paid_at = now(), cancelled_at = NULL, note = CASE WHEN sqlc.arg(note)::text = '' THEN note ELSE sqlc.arg(note)::text END,
    updated_at = now(), version = version + 1
WHERE id = $1
RETURNING *;

-- name: SetOrderCancelled :one
UPDATE orders SET status = 'cancelled', cancelled_at = now(), updated_at = now(), version = version + 1
WHERE id = $1 AND status = 'pending'
RETURNING *;

-- name: SetOrderRefunded :one
UPDATE orders SET status = 'refunded', refund_amount = $2, refund_days = $3, refunded_at = now(),
    note = CASE WHEN sqlc.arg(note)::text = '' THEN note ELSE sqlc.arg(note)::text END,
    updated_at = now(), version = version + 1
WHERE id = $1
RETURNING *;

-- name: SetOrderMethod :exec
UPDATE orders SET method = $2, updated_at = now() WHERE id = $1 AND status = 'pending';

-- name: DueOrderTimeouts :many
SELECT trade_no FROM orders WHERE status = 'pending' AND expires_at < now() ORDER BY id LIMIT $1;

-- name: CountUserCouponUses :one
SELECT count(*) FROM orders WHERE coupon_id = $1 AND user_id = $2 AND status IN ('pending', 'paid');

-- name: CountPaidOrdersOfUser :one
SELECT count(*) FROM orders WHERE user_id = $1 AND status IN ('paid', 'refunded');

-- name: PendingRefundApprovals :many
SELECT id, targets FROM change_plans
WHERE kind = 'order.refund' AND status = 'pending_approval';

-- name: InsertImportedOrder :exec
INSERT INTO orders (trade_no, user_id, plan_id, plan_name, period, kind, amount, discount, balance_used, total,
                    status, method, source, created_at, expires_at, paid_at)
VALUES ($1, $2, $3, $4, $5, $6, $7, $8, $9, $10, 'paid', $11, 'xboard', $12, $12, $13)
ON CONFLICT (trade_no) DO NOTHING;

-- 支付回调 ---------------------------------------------------------------

-- name: InsertPaymentCallback :exec
INSERT INTO payment_callbacks (method, trade_no, verified, result, payload) VALUES ($1, $2, $3, $4, $5);

-- name: ListPaymentCallbacks :many
SELECT * FROM payment_callbacks WHERE trade_no = $1 ORDER BY id DESC LIMIT 20;

-- 余额 -------------------------------------------------------------------

-- name: AddUserBalance :one
UPDATE users SET balance = balance + sqlc.arg(delta)::bigint, updated_at = now()
WHERE id = sqlc.arg(id)::bigint
RETURNING balance;

-- name: LockUserForUpdate :one
SELECT * FROM users WHERE id = $1 FOR UPDATE;

-- name: ApplyUserPurchase :one
UPDATE users SET plan_id = $2, transfer_bytes = $3, expires_at = $4, reset_anchor = $5, next_reset_at = $6,
    used_up = CASE WHEN sqlc.arg(reset_used)::boolean THEN 0 ELSE used_up END,
    used_down = CASE WHEN sqlc.arg(reset_used)::boolean THEN 0 ELSE used_down END,
    over_quota_at = CASE WHEN sqlc.arg(reset_used)::boolean THEN NULL ELSE over_quota_at END,
    last_reset_at = CASE WHEN sqlc.arg(reset_used)::boolean THEN now() ELSE last_reset_at END,
    updated_at = now(), version = version + 1
WHERE id = $1
RETURNING *;

-- name: SetUserExpiry :exec
UPDATE users SET expires_at = $2, updated_at = now(), version = version + 1 WHERE id = $1;

-- 优惠券 -----------------------------------------------------------------

-- name: CreateCoupon :one
INSERT INTO coupons (code, name, kind, value, plan_ids, periods, max_uses, per_user, starts_at, ends_at, enabled)
VALUES ($1, $2, $3, $4, $5, $6, $7, $8, $9, $10, $11)
RETURNING *;

-- name: UpdateCoupon :one
UPDATE coupons SET name = $2, kind = $3, value = $4, plan_ids = $5, periods = $6, max_uses = $7, per_user = $8,
    starts_at = $9, ends_at = $10, enabled = $11, updated_at = now(), version = version + 1
WHERE id = $1
RETURNING *;

-- name: GetCoupon :one
SELECT * FROM coupons WHERE id = $1;

-- name: GetCouponByCode :one
SELECT * FROM coupons WHERE lower(code) = lower($1);

-- name: CouponVersionForUpdate :one
SELECT version FROM coupons WHERE id = $1 FOR UPDATE;

-- name: LockCoupon :one
SELECT * FROM coupons WHERE id = $1 FOR UPDATE;

-- name: ListCoupons :many
SELECT * FROM coupons ORDER BY id DESC;

-- name: AddCouponUse :exec
UPDATE coupons SET used = GREATEST(used + sqlc.arg(delta)::integer, 0) WHERE id = sqlc.arg(id)::bigint;

-- 邀请返佣 ---------------------------------------------------------------

-- name: SetUserInviteCode :exec
UPDATE users SET invite_code = $2 WHERE id = $1 AND invite_code IS NULL;

-- name: SetUserReferral :exec
UPDATE users SET invited_by = $2, register_ip = $3 WHERE id = $1;

-- name: GetUserByInviteCode :one
SELECT * FROM users WHERE invite_code = $1;

-- name: CountInvitees :one
SELECT count(*) FROM users WHERE invited_by = $1;

-- name: CountRecentInviteesInSubnet :one
SELECT count(*) FROM users
WHERE invited_by = sqlc.arg(inviter_id)::bigint AND created_at > sqlc.arg(since)::timestamptz
  AND register_ip <> '' AND register_ip LIKE sqlc.arg(prefix)::text || '%';

-- name: CreateCommission :one
INSERT INTO commissions (inviter_id, invitee_id, order_id, amount, status, reason, release_at)
VALUES ($1, $2, $3, $4, $5, $6, $7)
ON CONFLICT (order_id) DO NOTHING
RETURNING *;

-- name: GetCommissionByOrder :one
SELECT * FROM commissions WHERE order_id = $1 FOR UPDATE;

-- name: SetCommissionStatus :exec
UPDATE commissions SET status = $2, reason = $3,
    settled_at = CASE WHEN $2 = 'settled' THEN now() ELSE settled_at END,
    updated_at = now(), version = version + 1
WHERE id = $1;

-- name: DueCommissions :many
SELECT * FROM commissions WHERE status = 'pending' AND release_at <= now() ORDER BY id LIMIT $1 FOR UPDATE SKIP LOCKED;

-- name: FrozenCommissionsOf :many
SELECT * FROM commissions WHERE inviter_id = $1 AND status = 'frozen' ORDER BY id FOR UPDATE;

-- name: ListCommissions :many
SELECT c.*, inv.email AS inviter_email, iv.email AS invitee_email, o.trade_no
FROM commissions c
JOIN users inv ON inv.id = c.inviter_id
LEFT JOIN users iv ON iv.id = c.invitee_id
JOIN orders o ON o.id = c.order_id
WHERE (sqlc.narg(status)::text IS NULL OR c.status = sqlc.narg(status)::text)
  AND (sqlc.narg(inviter_id)::bigint IS NULL OR c.inviter_id = sqlc.narg(inviter_id)::bigint)
  AND (sqlc.narg(before_id)::bigint IS NULL OR c.id < sqlc.narg(before_id)::bigint)
ORDER BY c.id DESC
LIMIT sqlc.arg(row_limit)::integer;

-- name: CommissionTotals :one
SELECT COALESCE(sum(amount) FILTER (WHERE status = 'pending'), 0)::bigint AS pending,
       COALESCE(sum(amount) FILTER (WHERE status = 'frozen'), 0)::bigint AS frozen,
       COALESCE(sum(amount) FILTER (WHERE status = 'settled'), 0)::bigint AS settled
FROM commissions WHERE (sqlc.narg(inviter_id)::bigint IS NULL OR inviter_id = sqlc.narg(inviter_id)::bigint);

-- name: SettingVersionForUpdate :one
SELECT (extract(epoch FROM updated_at) * 1000000)::bigint FROM settings WHERE key = $1 FOR UPDATE;
