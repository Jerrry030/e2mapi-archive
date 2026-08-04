package store

import (
	"context"
	"errors"
	"strconv"
	"strings"
	"time"

	"e2m.local/contracts"
	"github.com/jackc/pgx/v5"
)

const redeemCodeColumns = `id, type, code_hash, code_prefix, currency, amount_micros, status,
	batch_id, notes, expires_at, used_by, used_at, created_by, created_at, updated_at`

func scanRedeemCode(row rowScanner) (contracts.RedeemCode, error) {
	var code contracts.RedeemCode
	var codeType, status string
	var usedBy *int64
	err := row.Scan(&code.ID, &codeType, &code.CodeHash, &code.CodePrefix, &code.Currency,
		&code.AmountMicros, &status, &code.BatchID, &code.Notes, &code.ExpiresAt,
		&usedBy, &code.UsedAt, &code.CreatedBy, &code.CreatedAt, &code.UpdatedAt)
	if err != nil {
		return contracts.RedeemCode{}, err
	}
	code.Type = contracts.RedeemCodeType(codeType)
	code.Status = contracts.RedeemCodeStatus(status)
	if usedBy != nil {
		code.UsedBy = *usedBy
	}
	return code, nil
}

func (s *PostgresStore) CreateRedeemCodes(ctx context.Context, codes []contracts.RedeemCode) ([]contracts.RedeemCode, error) {
	if len(codes) == 0 {
		return nil, ErrInvalid
	}
	tx, err := s.pool.Begin(ctx)
	if err != nil {
		return nil, err
	}
	defer func() { _ = tx.Rollback(ctx) }()
	now := nowUTC()
	out := make([]contracts.RedeemCode, 0, len(codes))
	for _, input := range codes {
		code, normErr := normalizeRedeemCodeForCreate(input)
		if normErr != nil {
			return nil, normErr
		}
		code.ID = newID("redeem")
		code.CreatedAt, code.UpdatedAt = now, now
		var usedBy *int64
		if _, err := tx.Exec(ctx, `INSERT INTO redeem_codes
			(id, type, code_hash, code_prefix, currency, amount_micros, status, batch_id, notes, expires_at, used_by, used_at, created_by, created_at, updated_at)
			VALUES ($1,$2,$3,$4,$5,$6,$7,$8,$9,$10,$11,$12,$13,$14,$15)`,
			code.ID, string(code.Type), code.CodeHash, code.CodePrefix, code.Currency, code.AmountMicros,
			string(code.Status), code.BatchID, code.Notes, code.ExpiresAt, usedBy, code.UsedAt,
			code.CreatedBy, code.CreatedAt, code.UpdatedAt); err != nil {
			if isUniqueViolation(err) {
				return nil, ErrDuplicate
			}
			return nil, err
		}
		out = append(out, code)
	}
	if err := tx.Commit(ctx); err != nil {
		return nil, err
	}
	return out, nil
}

func (s *PostgresStore) GetRedeemCodeByHash(ctx context.Context, codeHash string) (contracts.RedeemCode, error) {
	code, err := scanRedeemCode(s.pool.QueryRow(ctx, `SELECT `+redeemCodeColumns+` FROM redeem_codes WHERE code_hash=$1`, codeHash))
	if errors.Is(err, pgx.ErrNoRows) {
		return contracts.RedeemCode{}, ErrNotFound
	}
	return code, err
}

func (s *PostgresStore) ListRedeemCodes(ctx context.Context, filter contracts.RedeemCodeFilter) (contracts.RedeemCodePage, error) {
	page, pageSize := normalizePaymentOrderPage(filter.Page, filter.PageSize)
	where, args := []string{"TRUE"}, []any{}
	arg := func(value any) string {
		args = append(args, value)
		return "$" + strconv.Itoa(len(args))
	}
	if filter.Type != "" {
		where = append(where, "type="+arg(string(filter.Type)))
	}
	if filter.Status != "" {
		where = append(where, "status="+arg(string(filter.Status)))
	}
	if filter.BatchID != "" {
		where = append(where, "batch_id="+arg(filter.BatchID))
	}
	clause := strings.Join(where, " AND ")
	var total int64
	if err := s.pool.QueryRow(ctx, `SELECT COUNT(*) FROM redeem_codes WHERE `+clause, args...).Scan(&total); err != nil {
		return contracts.RedeemCodePage{}, err
	}
	query := `SELECT ` + redeemCodeColumns + ` FROM redeem_codes WHERE ` + clause +
		` ORDER BY created_at DESC, id DESC LIMIT ` + arg(pageSize) + ` OFFSET ` + arg((page-1)*pageSize)
	rows, err := s.pool.Query(ctx, query, args...)
	if err != nil {
		return contracts.RedeemCodePage{}, err
	}
	defer rows.Close()
	items := []contracts.RedeemCode{}
	for rows.Next() {
		code, scanErr := scanRedeemCode(rows)
		if scanErr != nil {
			return contracts.RedeemCodePage{}, scanErr
		}
		items = append(items, code)
	}
	if err := rows.Err(); err != nil {
		return contracts.RedeemCodePage{}, err
	}
	return contracts.RedeemCodePage{Items: items, Total: total, Page: page, PageSize: pageSize}, nil
}

func (s *PostgresStore) DisableRedeemCode(ctx context.Context, id string) (contracts.RedeemCode, error) {
	code, err := scanRedeemCode(s.pool.QueryRow(ctx, `UPDATE redeem_codes
		SET status=$2, updated_at=statement_timestamp()
		WHERE id=$1 AND status=$3
		RETURNING `+redeemCodeColumns, id, string(contracts.RedeemCodeDisabled), string(contracts.RedeemCodeUnused)))
	if err == nil {
		return code, nil
	}
	if !errors.Is(err, pgx.ErrNoRows) {
		return contracts.RedeemCode{}, err
	}
	var exists bool
	if lookupErr := s.pool.QueryRow(ctx, `SELECT EXISTS(SELECT 1 FROM redeem_codes WHERE id=$1)`, id).Scan(&exists); lookupErr != nil {
		return contracts.RedeemCode{}, lookupErr
	}
	if !exists {
		return contracts.RedeemCode{}, ErrNotFound
	}
	return contracts.RedeemCode{}, ErrConflict
}

func (s *PostgresStore) DeleteUnusedRedeemCode(ctx context.Context, id string) error {
	tag, err := s.pool.Exec(ctx, `DELETE FROM redeem_codes WHERE id=$1 AND status<>$2`, id, string(contracts.RedeemCodeUsed))
	if err != nil {
		return err
	}
	if tag.RowsAffected() > 0 {
		return nil
	}
	var exists bool
	if lookupErr := s.pool.QueryRow(ctx, `SELECT EXISTS(SELECT 1 FROM redeem_codes WHERE id=$1)`, id).Scan(&exists); lookupErr != nil {
		return lookupErr
	}
	if !exists {
		return ErrNotFound
	}
	return ErrConflict
}

// RedeemBalanceCode consumes an unused balance code and credits the wallet in
// one serializable transaction: the row lock on the code makes a concurrent
// double redeem impossible, and the journal keeps the ledger balanced.
func (s *PostgresStore) RedeemBalanceCode(ctx context.Context, codeHash string, userID int64, now time.Time) (contracts.RedeemCode, contracts.Wallet, error) {
	if userID <= 0 || strings.TrimSpace(codeHash) == "" {
		return contracts.RedeemCode{}, contracts.Wallet{}, ErrInvalid
	}
	tx, err := s.pool.BeginTx(ctx, pgx.TxOptions{IsoLevel: pgx.Serializable})
	if err != nil {
		return contracts.RedeemCode{}, contracts.Wallet{}, err
	}
	defer func() { _ = tx.Rollback(ctx) }()
	var enabled bool
	if err := tx.QueryRow(ctx, `SELECT enabled FROM users WHERE id=$1`, userID).Scan(&enabled); err != nil || !enabled {
		if err == nil || errors.Is(err, pgx.ErrNoRows) {
			return contracts.RedeemCode{}, contracts.Wallet{}, ErrNotFound
		}
		return contracts.RedeemCode{}, contracts.Wallet{}, err
	}
	code, err := scanRedeemCode(tx.QueryRow(ctx, `SELECT `+redeemCodeColumns+` FROM redeem_codes WHERE code_hash=$1 FOR UPDATE`, codeHash))
	if errors.Is(err, pgx.ErrNoRows) {
		return contracts.RedeemCode{}, contracts.Wallet{}, ErrNotFound
	}
	if err != nil {
		return contracts.RedeemCode{}, contracts.Wallet{}, err
	}
	if code.Type != contracts.RedeemCodeBalance {
		return contracts.RedeemCode{}, contracts.Wallet{}, ErrConflict
	}
	if code.Status == contracts.RedeemCodeUnused && code.ExpiresAt != nil && !code.ExpiresAt.After(now) {
		if _, err := tx.Exec(ctx, `UPDATE redeem_codes SET status=$2, updated_at=statement_timestamp() WHERE id=$1`,
			code.ID, string(contracts.RedeemCodeExpired)); err != nil {
			return contracts.RedeemCode{}, contracts.Wallet{}, err
		}
		if err := tx.Commit(ctx); err != nil {
			return contracts.RedeemCode{}, contracts.Wallet{}, err
		}
		return contracts.RedeemCode{}, contracts.Wallet{}, ErrConflict
	}
	if code.Status != contracts.RedeemCodeUnused {
		return contracts.RedeemCode{}, contracts.Wallet{}, ErrConflict
	}
	stamp := nowUTC()
	usedAt := now.UTC()
	code, err = scanRedeemCode(tx.QueryRow(ctx, `UPDATE redeem_codes
		SET status=$2, used_by=$3, used_at=$4, updated_at=$5
		WHERE id=$1 AND status=$6
		RETURNING `+redeemCodeColumns,
		code.ID, string(contracts.RedeemCodeUsed), userID, usedAt, stamp, string(contracts.RedeemCodeUnused)))
	if errors.Is(err, pgx.ErrNoRows) {
		return contracts.RedeemCode{}, contracts.Wallet{}, ErrConflict
	}
	if err != nil {
		return contracts.RedeemCode{}, contracts.Wallet{}, err
	}
	if _, err := tx.Exec(ctx, `INSERT INTO wallet_accounts(user_id,currency,available_micros,reserved_micros,version,updated_at)
		VALUES($1,$2,0,0,1,$3) ON CONFLICT(user_id,currency) DO NOTHING`, userID, code.Currency, stamp); err != nil {
		return contracts.RedeemCode{}, contracts.Wallet{}, err
	}
	wallet, err := scanHybridWallet(tx.QueryRow(ctx, `UPDATE wallet_accounts SET available_micros=available_micros+$3,version=version+1,updated_at=$4
		WHERE user_id=$1 AND currency=$2
		RETURNING user_id,currency,available_micros,reserved_micros,version,updated_at`, userID, code.Currency, code.AmountMicros, stamp))
	if err != nil {
		return contracts.RedeemCode{}, contracts.Wallet{}, err
	}
	if err := insertWalletJournal(ctx, tx, userID, contracts.WalletJournalRedeem, code.Currency, code.AmountMicros,
		"redeem:"+code.ID, "redeem_code", code.ID,
		contracts.WalletAccountPlatformCash, contracts.WalletAccountUserAvailable, stamp); err != nil {
		if isUniqueViolation(err) {
			return contracts.RedeemCode{}, contracts.Wallet{}, ErrConflict
		}
		return contracts.RedeemCode{}, contracts.Wallet{}, err
	}
	if err := tx.Commit(ctx); err != nil {
		return contracts.RedeemCode{}, contracts.Wallet{}, err
	}
	return code, wallet, nil
}
