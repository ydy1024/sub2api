//go:build integration

package repository

import (
	"context"
	"fmt"
	"testing"
	"time"

	dbent "github.com/Wei-Shaw/sub2api/ent"
	"github.com/Wei-Shaw/sub2api/internal/service"
	"github.com/stretchr/testify/require"
)

func querySingleFloat(t *testing.T, ctx context.Context, client *dbent.Client, query string, args ...any) float64 {
	t.Helper()
	rows, err := client.QueryContext(ctx, query, args...)
	require.NoError(t, err)
	defer func() { _ = rows.Close() }()

	require.True(t, rows.Next(), "expected one row")
	var value float64
	require.NoError(t, rows.Scan(&value))
	require.NoError(t, rows.Err())
	return value
}

func querySingleInt(t *testing.T, ctx context.Context, client *dbent.Client, query string, args ...any) int {
	t.Helper()
	rows, err := client.QueryContext(ctx, query, args...)
	require.NoError(t, err)
	defer func() { _ = rows.Close() }()

	require.True(t, rows.Next(), "expected one row")
	var value int
	require.NoError(t, rows.Scan(&value))
	require.NoError(t, rows.Err())
	return value
}

func TestAffiliateRepository_TransferQuotaToBalance_UsesClaimedQuotaBeforeClear(t *testing.T) {
	ctx := context.Background()
	tx := testEntTx(t)
	txCtx := dbent.NewTxContext(ctx, tx)
	client := tx.Client()

	repo := NewAffiliateRepository(client, integrationDB)

	u := mustCreateUser(t, client, &service.User{
		Email:        fmt.Sprintf("affiliate-transfer-%d@example.com", time.Now().UnixNano()),
		PasswordHash: "hash",
		Role:         service.RoleUser,
		Status:       service.StatusActive,
		Balance:      5.5,
		Concurrency:  5,
	})

	affCode := fmt.Sprintf("AFF%09d", time.Now().UnixNano()%1_000_000_000)
	_, err := client.ExecContext(txCtx, `
INSERT INTO user_affiliates (user_id, aff_code, aff_quota, aff_history_quota, created_at, updated_at)
VALUES ($1, $2, $3, $3, NOW(), NOW())`, u.ID, affCode, 12.34)
	require.NoError(t, err)

	transferred, balance, err := repo.TransferQuotaToBalance(txCtx, u.ID)
	require.NoError(t, err)
	require.InDelta(t, 12.34, transferred, 1e-9)
	require.InDelta(t, 17.84, balance, 1e-9)

	affQuota := querySingleFloat(t, txCtx, client,
		"SELECT aff_quota::double precision FROM user_affiliates WHERE user_id = $1", u.ID)
	require.InDelta(t, 0.0, affQuota, 1e-9)

	persistedBalance := querySingleFloat(t, txCtx, client,
		"SELECT balance::double precision FROM users WHERE id = $1", u.ID)
	require.InDelta(t, 17.84, persistedBalance, 1e-9)

	ledgerCount := querySingleInt(t, txCtx, client,
		"SELECT COUNT(*) FROM user_affiliate_ledger WHERE user_id = $1 AND action = 'transfer'", u.ID)
	require.Equal(t, 1, ledgerCount)

	rows, err := client.QueryContext(txCtx, `
SELECT amount::double precision,
       balance_after::double precision,
       aff_quota_after::double precision,
       aff_frozen_quota_after::double precision,
       aff_history_quota_after::double precision
FROM user_affiliate_ledger
WHERE user_id = $1 AND action = 'transfer'
LIMIT 1`, u.ID)
	require.NoError(t, err)
	defer func() { _ = rows.Close() }()
	require.True(t, rows.Next(), "expected transfer ledger")
	var amount, balanceAfter, quotaAfter, frozenAfter, historyAfter float64
	require.NoError(t, rows.Scan(&amount, &balanceAfter, &quotaAfter, &frozenAfter, &historyAfter))
	require.InDelta(t, 12.34, amount, 1e-9)
	require.InDelta(t, 17.84, balanceAfter, 1e-9)
	require.InDelta(t, 0.0, quotaAfter, 1e-9)
	require.InDelta(t, 0.0, frozenAfter, 1e-9)
	require.InDelta(t, 12.34, historyAfter, 1e-9)
}

// TestAffiliateRepository_AccrueQuota_ReusesOuterTransaction guards the
// cross-layer tx propagation invariant: when AccrueQuota is called with a ctx
// that already carries a transaction (via dbent.NewTxContext), repo.withTx
// must reuse that tx rather than opening a nested one. If this invariant
// breaks, AccrueQuota would commit independently and survive a rollback of
// the outer tx, which would violate payment_fulfillment's all-or-nothing
// semantics.
func TestAffiliateRepository_AccrueQuota_ReusesOuterTransaction(t *testing.T) {
	ctx := context.Background()

	outerTx, err := integrationEntClient.Tx(ctx)
	require.NoError(t, err, "begin outer tx")
	// Defensive cleanup: if any require.* below fires before the explicit
	// Rollback, this prevents the tx from leaking until container teardown.
	// Rollback is idempotent at the driver level (extra rollback returns an
	// error we ignore).
	t.Cleanup(func() { _ = outerTx.Rollback() })
	client := outerTx.Client()
	txCtx := dbent.NewTxContext(ctx, outerTx)

	inviter := mustCreateUser(t, client, &service.User{
		Email:        fmt.Sprintf("affiliate-inviter-%d@example.com", time.Now().UnixNano()),
		PasswordHash: "hash",
		Role:         service.RoleUser,
		Status:       service.StatusActive,
		Concurrency:  5,
	})
	invitee := mustCreateUser(t, client, &service.User{
		Email:        fmt.Sprintf("affiliate-invitee-%d@example.com", time.Now().UnixNano()+1),
		PasswordHash: "hash",
		Role:         service.RoleUser,
		Status:       service.StatusActive,
		Concurrency:  5,
	})

	repo := NewAffiliateRepository(client, integrationDB)
	_, err = repo.EnsureUserAffiliate(txCtx, inviter.ID)
	require.NoError(t, err)
	_, err = repo.EnsureUserAffiliate(txCtx, invitee.ID)
	require.NoError(t, err)

	bound, err := repo.BindInviter(txCtx, invitee.ID, inviter.ID)
	require.NoError(t, err)
	require.True(t, bound, "invitee must bind to inviter")

	applied, err := repo.AccrueQuota(txCtx, inviter.ID, invitee.ID, 3.5, 0, nil)
	require.NoError(t, err)
	require.True(t, applied, "AccrueQuota must report applied=true")

	// Visible inside the outer tx.
	innerQuota := querySingleFloat(t, txCtx, client,
		"SELECT aff_quota::double precision FROM user_affiliates WHERE user_id = $1", inviter.ID)
	require.InDelta(t, 3.5, innerQuota, 1e-9)

	// Roll back the outer tx; if AccrueQuota had opened its own inner tx and
	// committed it, the rows would still be visible to the global client.
	require.NoError(t, outerTx.Rollback())

	rows, err := integrationEntClient.QueryContext(ctx,
		"SELECT COUNT(*) FROM user_affiliates WHERE user_id IN ($1, $2)",
		inviter.ID, invitee.ID)
	require.NoError(t, err)
	defer func() { _ = rows.Close() }()
	require.True(t, rows.Next())
	var postRollbackCount int
	require.NoError(t, rows.Scan(&postRollbackCount))
	require.Equal(t, 0, postRollbackCount,
		"AccrueQuota must propagate the outer tx — found persisted rows after rollback")
}

func TestAffiliateRepository_TransferQuotaToBalance_EmptyQuota(t *testing.T) {
	ctx := context.Background()
	tx := testEntTx(t)
	txCtx := dbent.NewTxContext(ctx, tx)
	client := tx.Client()

	repo := NewAffiliateRepository(client, integrationDB)

	u := mustCreateUser(t, client, &service.User{
		Email:        fmt.Sprintf("affiliate-empty-%d@example.com", time.Now().UnixNano()),
		PasswordHash: "hash",
		Role:         service.RoleUser,
		Status:       service.StatusActive,
		Balance:      3.21,
		Concurrency:  5,
	})

	affCode := fmt.Sprintf("AFF%09d", time.Now().UnixNano()%1_000_000_000)
	_, err := client.ExecContext(txCtx, `
INSERT INTO user_affiliates (user_id, aff_code, aff_quota, aff_history_quota, created_at, updated_at)
VALUES ($1, $2, 0, 0, NOW(), NOW())`, u.ID, affCode)
	require.NoError(t, err)

	transferred, balance, err := repo.TransferQuotaToBalance(txCtx, u.ID)
	require.ErrorIs(t, err, service.ErrAffiliateQuotaEmpty)
	require.InDelta(t, 0.0, transferred, 1e-9)
	require.InDelta(t, 0.0, balance, 1e-9)

	persistedBalance := querySingleFloat(t, txCtx, client,
		"SELECT balance::double precision FROM users WHERE id = $1", u.ID)
	require.InDelta(t, 3.21, persistedBalance, 1e-9)
}

// makeThawFixture creates a minimal test universe for thaw-logic tests:
// an inviter, an invitee, their user_affiliates rows, and a single frozen
// ledger entry with the given unlock_consume_required and baseline.
// Returns the inviter user, invitee user, and the ledger entry ID.
func makeThawFixture(
	t *testing.T,
	ctx context.Context,
	client *dbent.Client,
	inviterFrozenQuota float64,
	unlockConsumeRequired float64,
	unlockConsumeBaseline float64,
) (inviter *service.User, invitee *service.User, ledgerID int64) {
	t.Helper()
	ts := time.Now().UnixNano()

	inviter = mustCreateUser(t, client, &service.User{
		Email:        fmt.Sprintf("thaw-inviter-%d@example.com", ts),
		PasswordHash: "hash", Role: service.RoleUser,
		Status: service.StatusActive, Concurrency: 5,
	})
	invitee = mustCreateUser(t, client, &service.User{
		Email:        fmt.Sprintf("thaw-invitee-%d@example.com", ts+1),
		PasswordHash: "hash", Role: service.RoleUser,
		Status: service.StatusActive, Concurrency: 5,
	})

	affCode := fmt.Sprintf("THAW%09d", ts%1_000_000_000)
	_, err := client.ExecContext(ctx, `
INSERT INTO user_affiliates (user_id, aff_code, inviter_id, aff_quota, aff_frozen_quota, aff_history_quota, created_at, updated_at)
VALUES ($1, $2, NULL, 0, $3, $3, NOW(), NOW())
ON CONFLICT (user_id) DO UPDATE SET aff_frozen_quota=$3, aff_history_quota=$3`,
		inviter.ID, affCode, inviterFrozenQuota)
	require.NoError(t, err)

	_, err = client.ExecContext(ctx, `
INSERT INTO user_affiliates (user_id, aff_code, inviter_id, created_at, updated_at)
VALUES ($1, $2, $3, NOW(), NOW())
ON CONFLICT (user_id) DO UPDATE SET inviter_id=$3`,
		invitee.ID, fmt.Sprintf("INV%09d", ts%1_000_000_000), inviter.ID)
	require.NoError(t, err)

	var id int64
	rows, err := client.QueryContext(ctx, `
INSERT INTO user_affiliate_ledger
    (user_id, action, amount, source_user_id, unlock_consume_required, unlock_consume_baseline, created_at, updated_at)
VALUES ($1, 'accrue', $2, $3, $4, $5, NOW(), NOW())
RETURNING id`,
		inviter.ID, inviterFrozenQuota, invitee.ID, unlockConsumeRequired, unlockConsumeBaseline)
	require.NoError(t, err)
	defer func() { _ = rows.Close() }()
	require.True(t, rows.Next())
	require.NoError(t, rows.Scan(&id))
	ledgerID = id
	return inviter, invitee, ledgerID
}

// TestThawFrozenQuota_BelowThreshold verifies that a ledger entry whose
// invitee has not yet consumed enough stays frozen after ThawFrozenQuota.
func TestThawFrozenQuota_BelowThreshold(t *testing.T) {
	ctx := context.Background()
	tx := testEntTx(t)
	txCtx := dbent.NewTxContext(ctx, tx)
	client := tx.Client()

	inviter, invitee, _ := makeThawFixture(t, txCtx, client, 15, 100, 0)

	// invitee has consumed 99.99 — just short of the 100 threshold
	_, err := client.ExecContext(txCtx,
		"UPDATE users SET total_consumed = 99.99 WHERE id = $1", invitee.ID)
	require.NoError(t, err)

	thawed, err := thawFrozenQuotaTx(txCtx, client, inviter.ID)
	require.NoError(t, err)
	require.InDelta(t, 0.0, thawed, 1e-9, "must not thaw below threshold")

	frozen := querySingleFloat(t, txCtx, client,
		"SELECT aff_frozen_quota::double precision FROM user_affiliates WHERE user_id=$1", inviter.ID)
	require.InDelta(t, 15.0, frozen, 1e-9, "frozen quota must be unchanged")
}

// TestThawFrozenQuota_AtExactThreshold verifies that consuming exactly the
// threshold (not one cent more) triggers a thaw — the condition is >=.
func TestThawFrozenQuota_AtExactThreshold(t *testing.T) {
	ctx := context.Background()
	tx := testEntTx(t)
	txCtx := dbent.NewTxContext(ctx, tx)
	client := tx.Client()

	inviter, invitee, ledgerID := makeThawFixture(t, txCtx, client, 15, 100, 0)

	_, err := client.ExecContext(txCtx,
		"UPDATE users SET total_consumed = 100 WHERE id = $1", invitee.ID)
	require.NoError(t, err)

	thawed, err := thawFrozenQuotaTx(txCtx, client, inviter.ID)
	require.NoError(t, err)
	require.InDelta(t, 15.0, thawed, 1e-9, "must thaw exactly at threshold")

	quota := querySingleFloat(t, txCtx, client,
		"SELECT aff_quota::double precision FROM user_affiliates WHERE user_id=$1", inviter.ID)
	require.InDelta(t, 15.0, quota, 1e-9)

	frozen := querySingleFloat(t, txCtx, client,
		"SELECT aff_frozen_quota::double precision FROM user_affiliates WHERE user_id=$1", inviter.ID)
	require.InDelta(t, 0.0, frozen, 1e-9)

	// ledger entry's unlock_consume_required must be cleared
	nullCount := querySingleInt(t, txCtx, client,
		"SELECT COUNT(*) FROM user_affiliate_ledger WHERE id=$1 AND unlock_consume_required IS NULL", ledgerID)
	require.Equal(t, 1, nullCount, "ledger unlock_consume_required must be NULL after thaw")
}

// TestThawFrozenQuota_Idempotent verifies that calling thaw a second time on
// an already-thawed entry returns 0 and does not double-credit the quota.
func TestThawFrozenQuota_Idempotent(t *testing.T) {
	ctx := context.Background()
	tx := testEntTx(t)
	txCtx := dbent.NewTxContext(ctx, tx)
	client := tx.Client()

	inviter, invitee, _ := makeThawFixture(t, txCtx, client, 15, 100, 0)

	_, err := client.ExecContext(txCtx,
		"UPDATE users SET total_consumed = 100 WHERE id = $1", invitee.ID)
	require.NoError(t, err)

	// First thaw
	thawed1, err := thawFrozenQuotaTx(txCtx, client, inviter.ID)
	require.NoError(t, err)
	require.InDelta(t, 15.0, thawed1, 1e-9)

	// Second thaw — must be idempotent
	thawed2, err := thawFrozenQuotaTx(txCtx, client, inviter.ID)
	require.NoError(t, err)
	require.InDelta(t, 0.0, thawed2, 1e-9, "second thaw must return 0")

	quota := querySingleFloat(t, txCtx, client,
		"SELECT aff_quota::double precision FROM user_affiliates WHERE user_id=$1", inviter.ID)
	require.InDelta(t, 15.0, quota, 1e-9, "quota must not be double-counted")
}

// TestThawFrozenQuota_BaselineIncrementSemantics verifies that the unlock
// condition compares against the DELTA since accrue time, not against the
// absolute total_consumed value.  An invitee who had already consumed 500
// before this recharge should need to consume an additional 100 (reaching 600)
// to unlock — the baseline snapshot prevents old consumption from counting.
func TestThawFrozenQuota_BaselineIncrementSemantics(t *testing.T) {
	ctx := context.Background()
	tx := testEntTx(t)
	txCtx := dbent.NewTxContext(ctx, tx)
	client := tx.Client()

	// baseline=500 means: the invitee had already consumed 500 at accrue time.
	// required=100 means: 100 new units needed on top of that.
	inviter, invitee, _ := makeThawFixture(t, txCtx, client, 20, 100, 500)

	// Set invitee's total to 500 — no new consumption yet.
	_, err := client.ExecContext(txCtx,
		"UPDATE users SET total_consumed = 500 WHERE id = $1", invitee.ID)
	require.NoError(t, err)

	thawed, err := thawFrozenQuotaTx(txCtx, client, inviter.ID)
	require.NoError(t, err)
	require.InDelta(t, 0.0, thawed, 1e-9, "must not thaw: delta is 0, need 100")

	// Advance to 600 — delta of 100 meets the threshold exactly.
	_, err = client.ExecContext(txCtx,
		"UPDATE users SET total_consumed = 600 WHERE id = $1", invitee.ID)
	require.NoError(t, err)

	thawed, err = thawFrozenQuotaTx(txCtx, client, inviter.ID)
	require.NoError(t, err)
	require.InDelta(t, 20.0, thawed, 1e-9, "must thaw: delta is exactly 100")
}

// TestThawFrozenQuota_NullSourceUserID verifies that entries whose
// source_user_id was set to NULL (invitee deleted, ON DELETE SET NULL) are
// thawed immediately, preventing them from being locked forever.
func TestThawFrozenQuota_NullSourceUserID(t *testing.T) {
	ctx := context.Background()
	tx := testEntTx(t)
	txCtx := dbent.NewTxContext(ctx, tx)
	client := tx.Client()

	inviter, _, ledgerID := makeThawFixture(t, txCtx, client, 7, 100, 0)

	// Simulate invitee deletion by setting source_user_id to NULL.
	_, err := client.ExecContext(txCtx,
		"UPDATE user_affiliate_ledger SET source_user_id=NULL WHERE id=$1", ledgerID)
	require.NoError(t, err)

	thawed, err := thawFrozenQuotaTx(txCtx, client, inviter.ID)
	require.NoError(t, err)
	require.InDelta(t, 7.0, thawed, 1e-9, "NULL source_user_id must thaw immediately")

	quota := querySingleFloat(t, txCtx, client,
		"SELECT aff_quota::double precision FROM user_affiliates WHERE user_id=$1", inviter.ID)
	require.InDelta(t, 7.0, quota, 1e-9)
}

// TestVoidFrozenRebateByOrderID verifies that VoidFrozenRebateByOrderID:
//   - zeroes the frozen quota of the inviter for the matching order
//   - writes a void ledger entry for audit
//   - leaves already-thawed entries untouched
//   - is idempotent on re-call
func TestVoidFrozenRebateByOrderID(t *testing.T) {
	ctx := context.Background()
	tx := testEntTx(t)
	txCtx := dbent.NewTxContext(ctx, tx)
	client := tx.Client()

	inviter, invitee, _ := makeThawFixture(t, txCtx, client, 15, 100, 0)

	// Insert a real payment order so the FK constraint is satisfied.
	var orderID int64
	rows, err := client.QueryContext(txCtx, `
INSERT INTO payment_orders
    (user_id, user_email, amount, pay_amount, status, expires_at, created_at, updated_at)
VALUES ($1, $2, 100, 100, 'COMPLETED', NOW() + INTERVAL '1 hour', NOW(), NOW())
RETURNING id`,
		invitee.ID, invitee.Email)
	require.NoError(t, err)
	defer func() { _ = rows.Close() }()
	require.True(t, rows.Next())
	require.NoError(t, rows.Scan(&orderID))
	_ = rows.Close()

	// Attach the real order ID to the ledger entry.
	_, err = client.ExecContext(txCtx,
		"UPDATE user_affiliate_ledger SET source_order_id=$1 WHERE user_id=$2 AND source_user_id=$3",
		orderID, inviter.ID, invitee.ID)
	require.NoError(t, err)

	repo := NewAffiliateRepository(client, integrationDB)
	voided, amount, err := repo.VoidFrozenRebateByOrderID(txCtx, orderID)
	require.NoError(t, err)
	require.Equal(t, 1, voided)
	require.InDelta(t, 15.0, amount, 1e-9)

	// Frozen quota must drop to zero.
	frozen := querySingleFloat(t, txCtx, client,
		"SELECT aff_frozen_quota::double precision FROM user_affiliates WHERE user_id=$1", inviter.ID)
	require.InDelta(t, 0.0, frozen, 1e-9)

	// A void ledger entry must be written.
	voidCount := querySingleInt(t, txCtx, client,
		"SELECT COUNT(*) FROM user_affiliate_ledger WHERE user_id=$1 AND action='void' AND source_order_id=$2",
		inviter.ID, orderID)
	require.Equal(t, 1, voidCount)

	// Idempotent: calling again must void 0 entries (already cleared).
	voided2, amount2, err := repo.VoidFrozenRebateByOrderID(txCtx, orderID)
	require.NoError(t, err)
	require.Equal(t, 0, voided2)
	require.InDelta(t, 0.0, amount2, 1e-9)
}

// TestAffiliateRepository_AdminCustomCode covers the success path of admin
// invite-code rewrite + reset within a shared test transaction:
// - UpdateUserAffCode replaces aff_code, sets aff_code_custom=true, lookup works
// - the old code can no longer be found
// - ResetUserAffCode reverts aff_code_custom and assigns a new system-format code
//
// The conflict path (duplicate code → ErrAffiliateCodeTaken) lives in its own
// test because a unique-violation aborts the surrounding Postgres tx, which
// would poison subsequent assertions in the same transaction.
func TestAffiliateRepository_AdminCustomCode(t *testing.T) {
	ctx := context.Background()
	tx := testEntTx(t)
	txCtx := dbent.NewTxContext(ctx, tx)
	client := tx.Client()

	repo := NewAffiliateRepository(client, integrationDB)

	u := mustCreateUser(t, client, &service.User{
		Email:        fmt.Sprintf("affiliate-custom-%d@example.com", time.Now().UnixNano()),
		PasswordHash: "hash",
		Role:         service.RoleUser,
		Status:       service.StatusActive,
	})

	original, err := repo.EnsureUserAffiliate(txCtx, u.ID)
	require.NoError(t, err)
	require.False(t, original.AffCodeCustom, "system-generated codes start as non-custom")
	originalCode := original.AffCode

	// Rewrite to a custom code
	customCode := fmt.Sprintf("VIP%09d", time.Now().UnixNano()%1_000_000_000)
	require.NoError(t, repo.UpdateUserAffCode(txCtx, u.ID, customCode))

	updated, err := repo.EnsureUserAffiliate(txCtx, u.ID)
	require.NoError(t, err)
	require.Equal(t, customCode, updated.AffCode)
	require.True(t, updated.AffCodeCustom)

	// Lookup by new custom code finds the user
	byCode, err := repo.GetAffiliateByCode(txCtx, customCode)
	require.NoError(t, err)
	require.Equal(t, u.ID, byCode.UserID)

	// Old system code should no longer match
	_, err = repo.GetAffiliateByCode(txCtx, originalCode)
	require.ErrorIs(t, err, service.ErrAffiliateProfileNotFound)

	// Reset back to a fresh system code, clears custom flag
	newSysCode, err := repo.ResetUserAffCode(txCtx, u.ID)
	require.NoError(t, err)
	require.NotEqual(t, customCode, newSysCode)

	reset, err := repo.EnsureUserAffiliate(txCtx, u.ID)
	require.NoError(t, err)
	require.Equal(t, newSysCode, reset.AffCode)
	require.False(t, reset.AffCodeCustom)

	// The old custom code is now free again
	_, err = repo.GetAffiliateByCode(txCtx, customCode)
	require.ErrorIs(t, err, service.ErrAffiliateProfileNotFound)
}

// TestAffiliateRepository_AdminCustomCode_Conflict isolates the unique-violation
// path. PostgreSQL aborts the enclosing tx when a unique constraint fires, so
// this test must be the only assertion and run in its own tx — production
// callers each have their own outer tx, so this matches real behavior.
func TestAffiliateRepository_AdminCustomCode_Conflict(t *testing.T) {
	ctx := context.Background()
	tx := testEntTx(t)
	txCtx := dbent.NewTxContext(ctx, tx)
	client := tx.Client()

	repo := NewAffiliateRepository(client, integrationDB)

	taker := mustCreateUser(t, client, &service.User{
		Email:        fmt.Sprintf("affiliate-conflict-taker-%d@example.com", time.Now().UnixNano()),
		PasswordHash: "hash",
		Role:         service.RoleUser, Status: service.StatusActive,
	})
	requester := mustCreateUser(t, client, &service.User{
		Email:        fmt.Sprintf("affiliate-conflict-req-%d@example.com", time.Now().UnixNano()),
		PasswordHash: "hash",
		Role:         service.RoleUser, Status: service.StatusActive,
	})

	takenCode := fmt.Sprintf("HOT%09d", time.Now().UnixNano()%1_000_000_000)
	require.NoError(t, repo.UpdateUserAffCode(txCtx, taker.ID, takenCode))

	// Now requester tries to grab the same code → conflict.
	err := repo.UpdateUserAffCode(txCtx, requester.ID, takenCode)
	require.ErrorIs(t, err, service.ErrAffiliateCodeTaken)
}

// TestAffiliateRepository_AdminRebateRate covers per-user exclusive rate
// set/clear and the Batch variant including NULL semantics.
func TestAffiliateRepository_AdminRebateRate(t *testing.T) {
	ctx := context.Background()
	tx := testEntTx(t)
	txCtx := dbent.NewTxContext(ctx, tx)
	client := tx.Client()

	repo := NewAffiliateRepository(client, integrationDB)

	u1 := mustCreateUser(t, client, &service.User{
		Email:        fmt.Sprintf("affiliate-rate-%d-a@example.com", time.Now().UnixNano()),
		PasswordHash: "hash",
		Role:         service.RoleUser,
		Status:       service.StatusActive,
	})
	u2 := mustCreateUser(t, client, &service.User{
		Email:        fmt.Sprintf("affiliate-rate-%d-b@example.com", time.Now().UnixNano()),
		PasswordHash: "hash",
		Role:         service.RoleUser,
		Status:       service.StatusActive,
	})

	// Set exclusive rate for u1
	rate := 42.5
	require.NoError(t, repo.SetUserRebateRate(txCtx, u1.ID, &rate))

	got, err := repo.EnsureUserAffiliate(txCtx, u1.ID)
	require.NoError(t, err)
	require.NotNil(t, got.AffRebateRatePercent)
	require.InDelta(t, 42.5, *got.AffRebateRatePercent, 1e-9)

	// Clear exclusive rate
	require.NoError(t, repo.SetUserRebateRate(txCtx, u1.ID, nil))
	cleared, err := repo.EnsureUserAffiliate(txCtx, u1.ID)
	require.NoError(t, err)
	require.Nil(t, cleared.AffRebateRatePercent)

	// Batch set both users
	batchRate := 15.0
	require.NoError(t, repo.BatchSetUserRebateRate(txCtx, []int64{u1.ID, u2.ID}, &batchRate))

	for _, uid := range []int64{u1.ID, u2.ID} {
		v, err := repo.EnsureUserAffiliate(txCtx, uid)
		require.NoError(t, err)
		require.NotNil(t, v.AffRebateRatePercent)
		require.InDelta(t, 15.0, *v.AffRebateRatePercent, 1e-9)
	}

	// Batch clear
	require.NoError(t, repo.BatchSetUserRebateRate(txCtx, []int64{u1.ID, u2.ID}, nil))
	for _, uid := range []int64{u1.ID, u2.ID} {
		v, err := repo.EnsureUserAffiliate(txCtx, uid)
		require.NoError(t, err)
		require.Nil(t, v.AffRebateRatePercent)
	}
}

// TestAffiliateRepository_ListUsersWithCustomSettings verifies the admin list
// only includes users with at least one override applied.
func TestAffiliateRepository_ListUsersWithCustomSettings(t *testing.T) {
	ctx := context.Background()
	tx := testEntTx(t)
	txCtx := dbent.NewTxContext(ctx, tx)
	client := tx.Client()

	repo := NewAffiliateRepository(client, integrationDB)

	// User without any custom config — should NOT appear in the list.
	plainEmail := fmt.Sprintf("affiliate-plain-%d@example.com", time.Now().UnixNano())
	uPlain := mustCreateUser(t, client, &service.User{
		Email: plainEmail, PasswordHash: "hash",
		Role: service.RoleUser, Status: service.StatusActive,
	})
	_, err := repo.EnsureUserAffiliate(txCtx, uPlain.ID)
	require.NoError(t, err)

	// User with a custom code — should appear.
	uCode := mustCreateUser(t, client, &service.User{
		Email:        fmt.Sprintf("affiliate-codeonly-%d@example.com", time.Now().UnixNano()),
		PasswordHash: "hash",
		Role:         service.RoleUser, Status: service.StatusActive,
	})
	require.NoError(t, repo.UpdateUserAffCode(txCtx, uCode.ID, fmt.Sprintf("VIP%09d", time.Now().UnixNano()%1_000_000_000)))

	// User with only an exclusive rate — should appear.
	uRate := mustCreateUser(t, client, &service.User{
		Email:        fmt.Sprintf("affiliate-rateonly-%d@example.com", time.Now().UnixNano()),
		PasswordHash: "hash",
		Role:         service.RoleUser, Status: service.StatusActive,
	})
	r := 33.3
	require.NoError(t, repo.SetUserRebateRate(txCtx, uRate.ID, &r))

	entries, total, err := repo.ListUsersWithCustomSettings(txCtx, service.AffiliateAdminFilter{
		Page: 1, PageSize: 100,
	})
	require.NoError(t, err)

	// Build a quick lookup to assert per-user attributes (other tests may have
	// inserted custom rows in the same DB; we only care about our 3).
	byUserID := make(map[int64]service.AffiliateAdminEntry, len(entries))
	for _, e := range entries {
		byUserID[e.UserID] = e
	}

	require.NotContains(t, byUserID, uPlain.ID, "users without overrides must not appear")

	codeEntry, ok := byUserID[uCode.ID]
	require.True(t, ok, "custom-code user missing from list")
	require.True(t, codeEntry.AffCodeCustom)
	require.Nil(t, codeEntry.AffRebateRatePercent)

	rateEntry, ok := byUserID[uRate.ID]
	require.True(t, ok, "custom-rate user missing from list")
	require.False(t, rateEntry.AffCodeCustom)
	require.NotNil(t, rateEntry.AffRebateRatePercent)
	require.InDelta(t, 33.3, *rateEntry.AffRebateRatePercent, 1e-9)

	require.GreaterOrEqual(t, total, int64(2), "total must include at least our 2 custom rows")
}
