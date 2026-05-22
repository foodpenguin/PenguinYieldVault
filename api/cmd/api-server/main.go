package main

import (
	"context"
	"log"
	"net/http"
	"os"
	"path/filepath"
	"strconv"

	"github.com/ethereum/go-ethereum/common"
	"github.com/ethereum/go-ethereum/ethclient"
	"github.com/graphql-go/handler"

	"penguinyieldvault/api/internal/graphql"
	"penguinyieldvault/api/internal/indexer"
	"penguinyieldvault/api/internal/store"
)

// CORS Middleware to allow cross-origin requests from frontend (localhost:3000, GitHub Pages, etc.)
func corsHandler(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Access-Control-Allow-Origin", "*")
		w.Header().Set("Access-Control-Allow-Methods", "POST, GET, OPTIONS")
		w.Header().Set("Access-Control-Allow-Headers", "Content-Type, Authorization")
		if r.Method == "OPTIONS" {
			w.WriteHeader(http.StatusOK)
			return
		}
		next.ServeHTTP(w, r)
	})
}

// GraphiQL Playground HTML
const playgroundHTML = `
<!DOCTYPE html>
<html>
<head>
  <title>GraphiQL Playground</title>
  <link rel="stylesheet" href="https://cdnjs.cloudflare.com/ajax/libs/graphiql/3.0.4/graphiql.min.css" />
  <script src="https://cdnjs.cloudflare.com/ajax/libs/react/18.2.0/umd/react.production.min.js"></script>
  <script src="https://cdnjs.cloudflare.com/ajax/libs/react-dom/18.2.0/umd/react-dom.production.min.js"></script>
  <script src="https://cdnjs.cloudflare.com/ajax/libs/graphiql/3.0.4/graphiql.min.js"></script>
</head>
<body style="margin: 0; height: 100vh; overflow: hidden;">
  <div id="graphiql" style="height: 100%;"></div>
  <script>
    const fetcher = GraphiQL.createFetcher({ url: '/graphql' });
    ReactDOM.render(
      React.createElement(GraphiQL, { fetcher: fetcher }),
      document.getElementById('graphiql')
    );
  </script>
</body>
</html>
`

func main() {
	log.Println("Starting PenguinYieldVault API Server...")

	// 1. Determine env configurations
	rpcURL := getEnv("RPC_URL", "https://sepolia.infura.io/v3/9aa3d95b3bc440fa88ea12eaa4456161") // fallback public rpc
	vaultAddrStr := getEnv("VAULT_ADDRESS", "0x57436623bb4fe74e7dab0d7c643aa5442b10ee17")
	routerAddrStr := getEnv("STRATEGY_ROUTER_ADDRESS", "0x13a30c89b07730ade8c8c2f6bf0dd68b9328a702")
	portStr := getEnv("API_PORT", "8080")
	startBlockStr := getEnv("INDEXER_START_BLOCK", "6000000") // Start block near deployment
	dbPath := getEnv("DB_PATH", "./data/indexer.db")

	// Ensure db dir exists
	dbDir := filepath.Dir(dbPath)
	if err := os.MkdirAll(dbDir, 0755); err != nil {
		log.Fatalf("failed to create db directory: %v", err)
	}

	// 2. Initialize Database Store
	s, err := store.New(dbPath)
	if err != nil {
		log.Fatalf("failed to initialize SQLite store: %v", err)
	}
	defer s.Close()
	log.Printf("SQLite database initialized at: %s", dbPath)

	// 3. Setup Ethereum Client
	client, err := ethclient.Dial(rpcURL)
	if err != nil {
		log.Fatalf("failed to connect to Ethereum RPC: %v", err)
	}
	defer client.Close()
	log.Println("Ethereum Sepolia RPC client connected successfully")

	// 4. Initialize Resolver and Schema
	vaultAddress := common.HexToAddress(vaultAddrStr)
	routerAddress := common.HexToAddress(routerAddrStr)
	resolver := graphql.NewResolver(s, client, vaultAddress)
	schema, err := graphql.NewSchema(resolver)
	if err != nil {
		log.Fatalf("failed to create GraphQL schema: %v", err)
	}

	// 5. Start Background Event Indexer
	startBlock, _ := strconv.ParseInt(startBlockStr, 10, 64)
	indexerCfg := indexer.Config{
		RPCURL:          rpcURL,
		VaultAddress:    vaultAddress,
		RouterAddress:   routerAddress,
		PollIntervalSec: 15,
		StartBlock:      startBlock,
	}
	idx := indexer.New(indexerCfg, s)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	go func() {
		log.Println("Starting background blockchain event indexer...")
		if err := idx.Run(ctx); err != nil && err != context.Canceled {
			log.Printf("Indexer stopped with error: %v", err)
		}
	}()

	// 6. Setup GraphQL HTTP Endpoints
	h := handler.New(&handler.Config{
		Schema:   &schema,
		Pretty:   true,
		Playground: false,
	})

	// Wrap handlers in CORS
	http.Handle("/graphql", corsHandler(h))
	http.Handle("/playground", http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/html")
		w.Write([]byte(playgroundHTML))
	}))

	// Redirect root to playground
	http.HandleFunc("/", func(w http.ResponseWriter, r *http.Request) {
		http.Redirect(w, r, "/playground", http.StatusMovedPermanently)
	})

	log.Printf("API Server listening on http://localhost:%s/graphql", portStr)
	log.Printf("Interactive playground available at http://localhost:%s/playground", portStr)
	if err := http.ListenAndServe(":"+portStr, nil); err != nil {
		log.Fatalf("HTTP server failed: %v", err)
	}
}

func getEnv(key, defaultVal string) string {
	if val := os.Getenv(key); val != "" {
		return val
	}
	return defaultVal
}
