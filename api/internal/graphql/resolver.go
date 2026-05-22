package graphql

import (
	"context"
	"fmt"
	"math/big"
	"strings"

	"github.com/ethereum/go-ethereum"
	"github.com/ethereum/go-ethereum/accounts/abi"
	"github.com/ethereum/go-ethereum/common"
	"github.com/ethereum/go-ethereum/ethclient"

	"penguinyieldvault/api/internal/store"
)

type Resolver struct {
	store    *store.Store
	client   *ethclient.Client
	vaultAddr common.Address
}

func NewResolver(s *store.Store, client *ethclient.Client, vaultAddr common.Address) *Resolver {
	return &Resolver{
		store:    s,
		client:   client,
		vaultAddr: vaultAddr,
	}
}

// GetTVLHistory returns TVL history
func (r *Resolver) GetTVLHistory(ctx context.Context, from, to int64, interval string) ([]store.TVLSnapshot, error) {
	snaps, err := r.store.GetTVLHistory(from, to)
	if err != nil {
		return nil, err
	}

	// Filter by interval if necessary (e.g. daily, hourly)
	if len(snaps) == 0 {
		return snaps, nil
	}

	var filtered []store.TVLSnapshot
	var lastTime int64
	var intervalSec int64

	switch strings.ToLower(interval) {
	case "hour":
		intervalSec = 3600
	case "day":
		intervalSec = 86400
	default:
		return snaps, nil
	}

	for _, snap := range snaps {
		if snap.Timestamp >= lastTime+intervalSec {
			filtered = append(filtered, snap)
			lastTime = snap.Timestamp
		}
	}
	return filtered, nil
}

// GetAPYHistory returns APY history
func (r *Resolver) GetAPYHistory(ctx context.Context, from, to int64, interval string) ([]store.APYSnapshot, error) {
	return r.store.GetAPYHistory(from, to)
}

// GetUserTransactions returns user transactions with pagination
func (r *Resolver) GetUserTransactions(ctx context.Context, user string, first, skip int) ([]store.UserTransaction, error) {
	if first <= 0 {
		first = 20
	}
	if skip < 0 {
		skip = 0
	}
	return r.store.GetUserTransactions(user, first, skip)
}

// GetBotOperations returns bot operations with pagination
func (r *Resolver) GetBotOperations(ctx context.Context, first, skip int) ([]store.BotOperation, error) {
	if first <= 0 {
		first = 20
	}
	if skip < 0 {
		skip = 0
	}
	return r.store.GetBotOperations(first, skip)
}

// GetVaultStats returns the current vault statistics
func (r *Resolver) GetVaultStats(ctx context.Context) (store.VaultStats, error) {
	stats, err := r.store.GetVaultStats()
	if err != nil {
		return stats, err
	}

	// Query smoothedPnl directly from the contract for real-time accuracy
	smoothedPnl, err := r.querySmoothedPnl(ctx)
	if err != nil {
		// Log error and fallback to 0
		stats.SmoothedPnl = "0"
	} else {
		stats.SmoothedPnl = smoothedPnl.String()
	}

	return stats, nil
}

func (r *Resolver) querySmoothedPnl(ctx context.Context) (*big.Int, error) {
	if r.client == nil {
		return big.NewInt(0), fmt.Errorf("ethclient is nil")
	}

	vaultABI, _ := abi.JSON(strings.NewReader(`[
		{"inputs":[],"name":"smoothedPnl","outputs":[{"type":"int256"}],"stateMutability":"view","type":"function"}
	]`))

	data, err := vaultABI.Pack("smoothedPnl")
	if err != nil {
		return nil, err
	}

	result, err := r.client.CallContract(ctx, ethereum.CallMsg{To: &r.vaultAddr, Data: data}, nil)
	if err != nil {
		return nil, err
	}

	outputs, err := vaultABI.Unpack("smoothedPnl", result)
	if err != nil {
		return nil, err
	}

	if len(outputs) == 0 {
		return big.NewInt(0), nil
	}

	val, ok := outputs[0].(*big.Int)
	if !ok {
		return big.NewInt(0), fmt.Errorf("unexpected type for smoothedPnl")
	}

	return val, nil
}
