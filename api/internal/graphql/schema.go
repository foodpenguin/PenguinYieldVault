package graphql

import (
	"github.com/graphql-go/graphql"
)

// Define the schema types

var tvlSnapshotType = graphql.NewObject(graphql.ObjectConfig{
	Name: "TVLSnapshot",
	Fields: graphql.Fields{
		"timestamp": &graphql.Field{
			Type: graphql.NewNonNull(graphql.Int),
		},
		"totalAssets": &graphql.Field{
			Type: graphql.NewNonNull(graphql.String),
		},
		"idleAssets": &graphql.Field{
			Type: graphql.NewNonNull(graphql.String),
		},
		"strategyDebt": &graphql.Field{
			Type: graphql.NewNonNull(graphql.String),
		},
		"blockNumber": &graphql.Field{
			Type: graphql.NewNonNull(graphql.Int),
		},
	},
})

var apySnapshotType = graphql.NewObject(graphql.ObjectConfig{
	Name: "APYSnapshot",
	Fields: graphql.Fields{
		"timestamp": &graphql.Field{
			Type: graphql.NewNonNull(graphql.Int),
		},
		"apy": &graphql.Field{
			Type: graphql.NewNonNull(graphql.Float),
		},
		"cumulativeProfit": &graphql.Field{
			Type: graphql.NewNonNull(graphql.String),
		},
		"cumulativeLoss": &graphql.Field{
			Type: graphql.NewNonNull(graphql.String),
		},
	},
})

var userTransactionType = graphql.NewObject(graphql.ObjectConfig{
	Name: "UserTransaction",
	Fields: graphql.Fields{
		"txHash": &graphql.Field{
			Type: graphql.NewNonNull(graphql.String),
		},
		"blockNumber": &graphql.Field{
			Type: graphql.NewNonNull(graphql.Int),
		},
		"timestamp": &graphql.Field{
			Type: graphql.NewNonNull(graphql.Int),
		},
		"type": &graphql.Field{
			Type: graphql.NewNonNull(graphql.String), // "Deposit" | "Withdraw"
		},
		"user": &graphql.Field{
			Type: graphql.NewNonNull(graphql.String),
		},
		"assets": &graphql.Field{
			Type: graphql.NewNonNull(graphql.String),
		},
		"shares": &graphql.Field{
			Type: graphql.NewNonNull(graphql.String),
		},
	},
})

var botOperationType = graphql.NewObject(graphql.ObjectConfig{
	Name: "BotOperation",
	Fields: graphql.Fields{
		"txHash": &graphql.Field{
			Type: graphql.NewNonNull(graphql.String),
		},
		"blockNumber": &graphql.Field{
			Type: graphql.NewNonNull(graphql.Int),
		},
		"timestamp": &graphql.Field{
			Type: graphql.NewNonNull(graphql.Int),
		},
		"type": &graphql.Field{
			Type: graphql.NewNonNull(graphql.String),
		},
		"source": &graphql.Field{
			Type: graphql.NewNonNull(graphql.String),
		},
		"profit": &graphql.Field{
			Type: graphql.NewNonNull(graphql.String),
		},
		"loss": &graphql.Field{
			Type: graphql.NewNonNull(graphql.String),
		},
		"details": &graphql.Field{
			Type: graphql.String,
		},
	},
})

var vaultStatsType = graphql.NewObject(graphql.ObjectConfig{
	Name: "VaultStats",
	Fields: graphql.Fields{
		"totalReportedProfit": &graphql.Field{
			Type: graphql.NewNonNull(graphql.String),
		},
		"totalReportedLoss": &graphql.Field{
			Type: graphql.NewNonNull(graphql.String),
		},
		"totalGrossProfit": &graphql.Field{
			Type: graphql.NewNonNull(graphql.String),
		},
		"totalFeeAssetsAccrued": &graphql.Field{
			Type: graphql.NewNonNull(graphql.String),
		},
		"totalStrategyDebt": &graphql.Field{
			Type: graphql.NewNonNull(graphql.String),
		},
		"smoothedPnl": &graphql.Field{
			Type: graphql.NewNonNull(graphql.String),
		},
	},
})

// NewSchema creates a new GraphQL schema using the provided Resolver
func NewSchema(r *Resolver) (graphql.Schema, error) {
	queryType := graphql.NewObject(graphql.ObjectConfig{
		Name: "Query",
		Fields: graphql.Fields{
			"tvlHistory": &graphql.Field{
				Type: graphql.NewNonNull(graphql.NewList(graphql.NewNonNull(tvlSnapshotType))),
				Args: graphql.FieldConfigArgument{
					"from": &graphql.ArgumentConfig{
						Type: graphql.NewNonNull(graphql.Int),
					},
					"to": &graphql.ArgumentConfig{
						Type: graphql.NewNonNull(graphql.Int),
					},
					"interval": &graphql.ArgumentConfig{
						Type: graphql.NewNonNull(graphql.String),
					},
				},
				Resolve: func(p graphql.ResolveParams) (interface{}, error) {
					from := int64(p.Args["from"].(int))
					to := int64(p.Args["to"].(int))
					interval := p.Args["interval"].(string)
					return r.GetTVLHistory(p.Context, from, to, interval)
				},
			},
			"apyHistory": &graphql.Field{
				Type: graphql.NewNonNull(graphql.NewList(graphql.NewNonNull(apySnapshotType))),
				Args: graphql.FieldConfigArgument{
					"from": &graphql.ArgumentConfig{
						Type: graphql.NewNonNull(graphql.Int),
					},
					"to": &graphql.ArgumentConfig{
						Type: graphql.NewNonNull(graphql.Int),
					},
					"interval": &graphql.ArgumentConfig{
						Type: graphql.NewNonNull(graphql.String),
					},
				},
				Resolve: func(p graphql.ResolveParams) (interface{}, error) {
					from := int64(p.Args["from"].(int))
					to := int64(p.Args["to"].(int))
					return r.GetAPYHistory(p.Context, from, to, p.Args["interval"].(string))
				},
			},
			"userTransactions": &graphql.Field{
				Type: graphql.NewNonNull(graphql.NewList(graphql.NewNonNull(userTransactionType))),
				Args: graphql.FieldConfigArgument{
					"user": &graphql.ArgumentConfig{
						Type: graphql.NewNonNull(graphql.String),
					},
					"first": &graphql.ArgumentConfig{
						Type: graphql.Int,
					},
					"skip": &graphql.ArgumentConfig{
						Type: graphql.Int,
					},
				},
				Resolve: func(p graphql.ResolveParams) (interface{}, error) {
					user := p.Args["user"].(string)
					first := 20
					if f, ok := p.Args["first"].(int); ok {
						first = f
					}
					skip := 0
					if s, ok := p.Args["skip"].(int); ok {
						skip = s
					}
					return r.GetUserTransactions(p.Context, user, first, skip)
				},
			},
			"botOperations": &graphql.Field{
				Type: graphql.NewNonNull(graphql.NewList(graphql.NewNonNull(botOperationType))),
				Args: graphql.FieldConfigArgument{
					"first": &graphql.ArgumentConfig{
						Type: graphql.Int,
					},
					"skip": &graphql.ArgumentConfig{
						Type: graphql.Int,
					},
				},
				Resolve: func(p graphql.ResolveParams) (interface{}, error) {
					first := 20
					if f, ok := p.Args["first"].(int); ok {
						first = f
					}
					skip := 0
					if s, ok := p.Args["skip"].(int); ok {
						skip = s
					}
					return r.GetBotOperations(p.Context, first, skip)
				},
			},
			"vaultStats": &graphql.Field{
				Type: graphql.NewNonNull(vaultStatsType),
				Resolve: func(p graphql.ResolveParams) (interface{}, error) {
					return r.GetVaultStats(p.Context)
				},
			},
		},
	})

	return graphql.NewSchema(graphql.SchemaConfig{
		Query: queryType,
	})
}
