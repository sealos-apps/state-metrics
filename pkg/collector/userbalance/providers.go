package userbalance

import (
	"context"
	"errors"
	"fmt"
	"math"
)

func (c *Collector) QueryBalance(ctx context.Context, user UserConfig) (float64, error) {
	if c.pgClient == nil {
		return 0, errors.New("database client is not initialized")
	}

	query := `
        SELECT 
            u.uid,
            u.id,
            u.name,
            COALESCE(a.balance, 0) as balance,
            COALESCE(a.deduction_balance, 0) as deduction_balance,
            COALESCE(a."encryptBalance", '') as encrypt_balance
        FROM "User" u
        LEFT JOIN "Account" a ON u.uid = a."userUid"
        WHERE u.id = $1
    `

	var (
		uid, id, name, encryptBalance string
		balance, deductionBalance     int64
	)

	err := c.pgClient.QueryRow(ctx, query, user.UID).Scan(
		&uid,
		&id,
		&name,
		&balance,
		&deductionBalance,
		&encryptBalance,
	)
	if err != nil {
		return 0, fmt.Errorf("query user balance failed: %w", err)
	}

	return roundBalance(balance, deductionBalance), nil
}

func (c *Collector) QueryPositiveBalances(ctx context.Context) ([]balanceSample, error) {
	if c.pgClient == nil {
		return nil, errors.New("database client is not initialized")
	}

	query := `
        SELECT
            COALESCE(a.create_region_id, '') as region,
            u.uid::text as uuid,
            u.id as uid,
            uc."crName" as owner,
            COALESCE(a.balance, 0) as balance,
            COALESCE(a.deduction_balance, 0) as deduction_balance
        FROM "Account" a
        JOIN "User" u ON u.uid = a."userUid"
        JOIN "UserCr" uc ON uc."userUid" = u.uid
        WHERE COALESCE(a.balance, 0) - COALESCE(a.deduction_balance, 0) > 0
        ORDER BY owner, uid
    `

	rows, err := c.pgClient.Query(ctx, query)
	if err != nil {
		return nil, fmt.Errorf("query positive user balances failed: %w", err)
	}
	defer rows.Close()

	var samples []balanceSample
	for rows.Next() {
		var (
			user                      UserConfig
			balance, deductionBalance int64
		)

		if err := rows.Scan(
			&user.Region,
			&user.UUID,
			&user.UID,
			&user.Owner,
			&balance,
			&deductionBalance,
		); err != nil {
			return nil, fmt.Errorf("scan positive user balance: %w", err)
		}

		user.Type = "balance"
		samples = append(samples, balanceSample{
			User:    user,
			Balance: roundBalance(balance, deductionBalance),
		})
	}

	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("iterate positive user balances: %w", err)
	}

	return samples, nil
}

func roundBalance(balance, deductionBalance int64) float64 {
	return math.Round(float64(balance-deductionBalance)/1000000*100) / 100
}
