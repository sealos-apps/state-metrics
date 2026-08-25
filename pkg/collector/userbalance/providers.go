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
            COALESCE(a.balance, 0) as balance,
            COALESCE(a.deduction_balance, 0) as deduction_balance
        FROM "Account" a
        JOIN "User" u ON u.uid = a."userUid"
        WHERE COALESCE(a.balance, 0) - COALESCE(a.deduction_balance, 0) > 0
        ORDER BY uid
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

func (c *Collector) QueryOwners(
	ctx context.Context,
	userUUIDs []string,
) (map[string][]string, error) {
	owners := make(map[string][]string, len(userUUIDs))
	if c.localPgClient == nil || len(userUUIDs) == 0 {
		return owners, nil
	}

	query := `
        SELECT "userUid"::text, "crName"
        FROM "UserCr"
        WHERE "userUid"::text = ANY($1::text[])
        ORDER BY "userUid", "crName"
    `

	rows, err := c.localPgClient.Query(ctx, query, userUUIDs)
	if err != nil {
		return nil, fmt.Errorf("query user owners failed: %w", err)
	}
	defer rows.Close()

	for rows.Next() {
		var userUUID, owner string
		if err := rows.Scan(&userUUID, &owner); err != nil {
			return nil, fmt.Errorf("scan user owner: %w", err)
		}

		owners[userUUID] = append(owners[userUUID], owner)
	}

	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("iterate user owners: %w", err)
	}

	return owners, nil
}

func roundBalance(balance, deductionBalance int64) float64 {
	return math.Round(float64(balance-deductionBalance)/1000000*100) / 100
}
