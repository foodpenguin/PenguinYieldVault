package store

import (
	"database/sql"
	"fmt"
	"math/big"
	"sync"

	_ "github.com/mattn/go-sqlite3"
)

// TVLSnapshot represents a historical TVL data point.
type TVLSnapshot struct {
	Timestamp    int64  `json:"timestamp"`
	TotalAssets  string `json:"totalAssets"`
	IdleAssets   string `json:"idleAssets"`
	StrategyDebt string `json:"strategyDebt"`
	BlockNumber  int64  `json:"blockNumber"`
}

// APYSnapshot represents a historical APY data point.
type APYSnapshot struct {
	Timestamp        int64   `json:"timestamp"`
	APY              float64 `json:"apy"`
	CumulativeProfit string  `json:"cumulativeProfit"`
	CumulativeLoss   string  `json:"cumulativeLoss"`
}

// UserTransaction represents a user deposit or withdrawal.
type UserTransaction struct {
	TxHash      string `json:"txHash"`
	BlockNumber int64  `json:"blockNumber"`
	Timestamp   int64  `json:"timestamp"`
	Type        string `json:"type"`
	User        string `json:"user"`
	Assets      string `json:"assets"`
	Shares      string `json:"shares"`
}

// BotOperation represents a bot operation record.
type BotOperation struct {
	TxHash      string `json:"txHash"`
	BlockNumber int64  `json:"blockNumber"`
	Timestamp   int64  `json:"timestamp"`
	Type        string `json:"type"`
	Source      string `json:"source"`
	Profit      string `json:"profit"`
	Loss        string `json:"loss"`
	Details     string `json:"details"`
}

// VaultStats represents aggregated vault statistics.
type VaultStats struct {
	TotalReportedProfit  string `json:"totalReportedProfit"`
	TotalReportedLoss    string `json:"totalReportedLoss"`
	TotalGrossProfit     string `json:"totalGrossProfit"`
	TotalFeeAssetsAccrued string `json:"totalFeeAssetsAccrued"`
	TotalStrategyDebt    string `json:"totalStrategyDebt"`
	SmoothedPnl          string `json:"smoothedPnl"`
}

// Store provides SQLite-backed storage for indexed blockchain events.
type Store struct {
	db *sql.DB
	mu sync.RWMutex
}

// New creates a new Store and initializes the database schema.
func New(dbPath string) (*Store, error) {
	db, err := sql.Open("sqlite3", dbPath+"?_journal_mode=WAL&_busy_timeout=5000")
	if err != nil {
		return nil, fmt.Errorf("open sqlite: %w", err)
	}

	s := &Store{db: db}
	if err := s.migrate(); err != nil {
		db.Close()
		return nil, fmt.Errorf("migrate: %w", err)
	}
	return s, nil
}

func (s *Store) migrate() error {
	// Use parameterized pragmas where possible; these are safe static strings.
	_, err := s.db.Exec(`
		PRAGMA foreign_keys = ON;

		CREATE TABLE IF NOT EXISTS tvl_snapshots (
			id INTEGER PRIMARY KEY AUTOINCREMENT,
			timestamp INTEGER NOT NULL,
			total_assets TEXT NOT NULL,
			idle_assets TEXT NOT NULL,
			strategy_debt TEXT NOT NULL,
			block_number INTEGER NOT NULL
		);
		CREATE INDEX IF NOT EXISTS idx_tvl_timestamp ON tvl_snapshots(timestamp);

		CREATE TABLE IF NOT EXISTS user_transactions (
			id INTEGER PRIMARY KEY AUTOINCREMENT,
			tx_hash TEXT NOT NULL UNIQUE,
			block_number INTEGER NOT NULL,
			timestamp INTEGER NOT NULL,
			type TEXT NOT NULL,
			user_address TEXT NOT NULL,
			assets TEXT NOT NULL,
			shares TEXT NOT NULL
		);
		CREATE INDEX IF NOT EXISTS idx_user_tx_user ON user_transactions(user_address);
		CREATE INDEX IF NOT EXISTS idx_user_tx_timestamp ON user_transactions(timestamp);

		CREATE TABLE IF NOT EXISTS bot_operations (
			id INTEGER PRIMARY KEY AUTOINCREMENT,
			tx_hash TEXT NOT NULL,
			block_number INTEGER NOT NULL,
			timestamp INTEGER NOT NULL,
			type TEXT NOT NULL,
			source TEXT NOT NULL,
			profit TEXT NOT NULL DEFAULT '0',
			loss TEXT NOT NULL DEFAULT '0',
			details TEXT
		);
		CREATE INDEX IF NOT EXISTS idx_bot_ops_timestamp ON bot_operations(timestamp);
		CREATE INDEX IF NOT EXISTS idx_bot_ops_type ON bot_operations(type);

		CREATE TABLE IF NOT EXISTS strategy_settlements (
			id INTEGER PRIMARY KEY AUTOINCREMENT,
			tx_hash TEXT NOT NULL UNIQUE,
			block_number INTEGER NOT NULL,
			timestamp INTEGER NOT NULL,
			strategy TEXT NOT NULL,
			principal TEXT NOT NULL,
			returned_assets TEXT NOT NULL,
			profit TEXT NOT NULL DEFAULT '0',
			loss TEXT NOT NULL DEFAULT '0',
			gross_profit TEXT NOT NULL DEFAULT '0',
			fee_assets TEXT NOT NULL DEFAULT '0'
		);
		CREATE INDEX IF NOT EXISTS idx_settle_timestamp ON strategy_settlements(timestamp);

		CREATE TABLE IF NOT EXISTS indexer_state (
			key TEXT PRIMARY KEY,
			value TEXT NOT NULL
		);

		-- Migration: ensure tx_hash uniqueness for upsert support.
		-- For existing databases where the table was created without UNIQUE,
		-- this adds the unique index. Safe to run repeatedly.
		CREATE UNIQUE INDEX IF NOT EXISTS idx_settle_tx_hash ON strategy_settlements(tx_hash);
	`)
	return err
}

// Close closes the database connection.
func (s *Store) Close() error {
	return s.db.Close()
}

// --- Indexer State ---

// GetLastIndexedBlock returns the last indexed block number.
func (s *Store) GetLastIndexedBlock() (int64, error) {
	s.mu.RLock()
	defer s.mu.RUnlock()

	var val string
	err := s.db.QueryRow("SELECT value FROM indexer_state WHERE key = ?", "last_block").Scan(&val)
	if err == sql.ErrNoRows {
		return 0, nil
	}
	if err != nil {
		return 0, err
	}
	n := new(big.Int)
	n.SetString(val, 10)
	return n.Int64(), nil
}

// SetLastIndexedBlock updates the last indexed block number.
func (s *Store) SetLastIndexedBlock(block int64) error {
	s.mu.Lock()
	defer s.mu.Unlock()

	_, err := s.db.Exec(
		"INSERT OR REPLACE INTO indexer_state (key, value) VALUES (?, ?)",
		"last_block", fmt.Sprintf("%d", block),
	)
	return err
}

// --- TVL Snapshots ---

// InsertTVLSnapshot inserts a TVL snapshot.
func (s *Store) InsertTVLSnapshot(snap TVLSnapshot) error {
	s.mu.Lock()
	defer s.mu.Unlock()

	_, err := s.db.Exec(
		"INSERT INTO tvl_snapshots (timestamp, total_assets, idle_assets, strategy_debt, block_number) VALUES (?, ?, ?, ?, ?)",
		snap.Timestamp, snap.TotalAssets, snap.IdleAssets, snap.StrategyDebt, snap.BlockNumber,
	)
	return err
}

// GetTVLHistory returns TVL snapshots within the given time range.
func (s *Store) GetTVLHistory(from, to int64) ([]TVLSnapshot, error) {
	s.mu.RLock()
	defer s.mu.RUnlock()

	rows, err := s.db.Query(
		"SELECT timestamp, total_assets, idle_assets, strategy_debt, block_number FROM tvl_snapshots WHERE timestamp >= ? AND timestamp <= ? ORDER BY timestamp ASC",
		from, to,
	)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var result []TVLSnapshot
	for rows.Next() {
		var snap TVLSnapshot
		if err := rows.Scan(&snap.Timestamp, &snap.TotalAssets, &snap.IdleAssets, &snap.StrategyDebt, &snap.BlockNumber); err != nil {
			return nil, err
		}
		result = append(result, snap)
	}
	return result, rows.Err()
}

// --- User Transactions ---

// InsertUserTransaction inserts a user transaction record.
func (s *Store) InsertUserTransaction(tx UserTransaction) error {
	s.mu.Lock()
	defer s.mu.Unlock()

	_, err := s.db.Exec(
		"INSERT OR IGNORE INTO user_transactions (tx_hash, block_number, timestamp, type, user_address, assets, shares) VALUES (?, ?, ?, ?, ?, ?, ?)",
		tx.TxHash, tx.BlockNumber, tx.Timestamp, tx.Type, tx.User, tx.Assets, tx.Shares,
	)
	return err
}

// GetUserTransactions returns transactions for a specific user.
func (s *Store) GetUserTransactions(user string, first, skip int) ([]UserTransaction, error) {
	s.mu.RLock()
	defer s.mu.RUnlock()

	rows, err := s.db.Query(
		"SELECT tx_hash, block_number, timestamp, type, user_address, assets, shares FROM user_transactions WHERE user_address = ? ORDER BY timestamp DESC LIMIT ? OFFSET ?",
		user, first, skip,
	)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var result []UserTransaction
	for rows.Next() {
		var tx UserTransaction
		if err := rows.Scan(&tx.TxHash, &tx.BlockNumber, &tx.Timestamp, &tx.Type, &tx.User, &tx.Assets, &tx.Shares); err != nil {
			return nil, err
		}
		result = append(result, tx)
	}
	return result, rows.Err()
}

// --- Bot Operations ---

// InsertBotOperation inserts a bot operation record.
func (s *Store) InsertBotOperation(op BotOperation) error {
	s.mu.Lock()
	defer s.mu.Unlock()

	_, err := s.db.Exec(
		"INSERT INTO bot_operations (tx_hash, block_number, timestamp, type, source, profit, loss, details) VALUES (?, ?, ?, ?, ?, ?, ?, ?)",
		op.TxHash, op.BlockNumber, op.Timestamp, op.Type, op.Source, op.Profit, op.Loss, op.Details,
	)
	return err
}

// GetBotOperations returns bot operations with pagination.
func (s *Store) GetBotOperations(first, skip int) ([]BotOperation, error) {
	s.mu.RLock()
	defer s.mu.RUnlock()

	rows, err := s.db.Query(
		"SELECT tx_hash, block_number, timestamp, type, source, profit, loss, COALESCE(details, '') FROM bot_operations ORDER BY timestamp DESC LIMIT ? OFFSET ?",
		first, skip,
	)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var result []BotOperation
	for rows.Next() {
		var op BotOperation
		if err := rows.Scan(&op.TxHash, &op.BlockNumber, &op.Timestamp, &op.Type, &op.Source, &op.Profit, &op.Loss, &op.Details); err != nil {
			return nil, err
		}
		result = append(result, op)
	}
	return result, rows.Err()
}

// --- Strategy Settlements ---

// InsertSettlement inserts or updates a strategy settlement record.
// If a partial row already exists (created by UpdateSettlementDetails arriving first),
// the existing gross_profit and fee_assets values are preserved via upsert.
func (s *Store) InsertSettlement(txHash string, blockNumber, timestamp int64, strategy, principal, returnedAssets, profit, loss, grossProfit, feeAssets string) error {
	s.mu.Lock()
	defer s.mu.Unlock()

	// Use INSERT ... ON CONFLICT to merge with any partial row that was
	// created by a StrategySettledDetails event arriving before this one.
	// Preserve existing non-zero gross_profit/fee_assets values.
	_, err := s.db.Exec(`
		INSERT INTO strategy_settlements (tx_hash, block_number, timestamp, strategy, principal, returned_assets, profit, loss, gross_profit, fee_assets)
		VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?)
		ON CONFLICT(tx_hash) DO UPDATE SET
			block_number = excluded.block_number,
			timestamp = excluded.timestamp,
			strategy = excluded.strategy,
			principal = excluded.principal,
			returned_assets = excluded.returned_assets,
			profit = excluded.profit,
			loss = excluded.loss,
			gross_profit = CASE WHEN strategy_settlements.gross_profit != '0' THEN strategy_settlements.gross_profit ELSE excluded.gross_profit END,
			fee_assets = CASE WHEN strategy_settlements.fee_assets != '0' THEN strategy_settlements.fee_assets ELSE excluded.fee_assets END
	`, txHash, blockNumber, timestamp, strategy, principal, returnedAssets, profit, loss, grossProfit, feeAssets)
	return err
}

// UpdateSettlementDetails updates or inserts gross_profit and fee_assets for a settlement.
// If the settlement row does not yet exist (StrategySettledDetails arrives before StrategySettled),
// a partial row is created so the data is not lost.
func (s *Store) UpdateSettlementDetails(txHash, grossProfit, feeAssets string) error {
	s.mu.Lock()
	defer s.mu.Unlock()

	// First try to update an existing row.
	result, err := s.db.Exec(
		"UPDATE strategy_settlements SET gross_profit = ?, fee_assets = ? WHERE tx_hash = ?",
		grossProfit, feeAssets, txHash,
	)
	if err != nil {
		return err
	}

	rowsAffected, err := result.RowsAffected()
	if err != nil {
		return err
	}

	// If no row was updated, the StrategySettled event hasn't been processed yet.
	// Insert a partial row so the data is preserved for when InsertSettlement runs.
	if rowsAffected == 0 {
		_, err = s.db.Exec(
			"INSERT INTO strategy_settlements (tx_hash, block_number, timestamp, strategy, principal, returned_assets, profit, loss, gross_profit, fee_assets) VALUES (?, 0, 0, '', '0', '0', '0', '0', ?, ?)",
			txHash, grossProfit, feeAssets,
		)
		return err
	}

	return nil
}

// GetAPYHistory calculates historical APY from settlement data.
func (s *Store) GetAPYHistory(from, to int64) ([]APYSnapshot, error) {
	s.mu.RLock()
	defer s.mu.RUnlock()

	// Group settlements by day and compute cumulative profit/loss
	rows, err := s.db.Query(`
		SELECT
			(timestamp / 86400) * 86400 as day_ts,
			SUM(CAST(profit AS REAL)) as day_profit,
			SUM(CAST(loss AS REAL)) as day_loss
		FROM strategy_settlements
		WHERE timestamp >= ? AND timestamp <= ?
		GROUP BY day_ts
		ORDER BY day_ts ASC
	`, from, to)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var result []APYSnapshot
	var cumProfit, cumLoss float64
	for rows.Next() {
		var dayTs int64
		var dayProfit, dayLoss float64
		if err := rows.Scan(&dayTs, &dayProfit, &dayLoss); err != nil {
			return nil, err
		}
		cumProfit += dayProfit
		cumLoss += dayLoss

		// Simple annualized APY: (daily profit / principal approximation) * 365
		// This is a rough estimation; more sophisticated calculation can be added
		netDaily := dayProfit - dayLoss
		apy := 0.0
		if cumProfit > 0 {
			apy = (netDaily / cumProfit) * 365.0 * 100.0
		}

		result = append(result, APYSnapshot{
			Timestamp:        dayTs,
			APY:              apy,
			CumulativeProfit: fmt.Sprintf("%.0f", cumProfit),
			CumulativeLoss:   fmt.Sprintf("%.0f", cumLoss),
		})
	}
	return result, rows.Err()
}

// GetVaultStats computes aggregated stats from the database.
func (s *Store) GetVaultStats() (VaultStats, error) {
	s.mu.RLock()
	defer s.mu.RUnlock()

	var stats VaultStats
	var profit, loss, gross, fee sql.NullFloat64
	err := s.db.QueryRow(`
		SELECT 
			COALESCE(SUM(CAST(profit AS REAL)), 0),
			COALESCE(SUM(CAST(loss AS REAL)), 0),
			COALESCE(SUM(CAST(gross_profit AS REAL)), 0),
			COALESCE(SUM(CAST(fee_assets AS REAL)), 0)
		FROM strategy_settlements
	`).Scan(&profit, &loss, &gross, &fee)
	if err != nil {
		return stats, err
	}

	stats.TotalReportedProfit = fmt.Sprintf("%.0f", profit.Float64)
	stats.TotalReportedLoss = fmt.Sprintf("%.0f", loss.Float64)
	stats.TotalGrossProfit = fmt.Sprintf("%.0f", gross.Float64)
	stats.TotalFeeAssetsAccrued = fmt.Sprintf("%.0f", fee.Float64)

	var strategyDebt sql.NullString
	err = s.db.QueryRow("SELECT strategy_debt FROM tvl_snapshots ORDER BY timestamp DESC LIMIT 1").Scan(&strategyDebt)
	if err != nil && err != sql.ErrNoRows {
		return stats, err
	}
	stats.TotalStrategyDebt = "0"
	if strategyDebt.Valid && strategyDebt.String != "" {
		stats.TotalStrategyDebt = strategyDebt.String
	}

	stats.SmoothedPnl = "0"

	return stats, nil
}

