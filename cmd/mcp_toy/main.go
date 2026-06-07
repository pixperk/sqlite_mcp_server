// Command mcp_toy is a Model Context Protocol (MCP) server exposing a
// SQLite-backed key/value store to an AI client over stdio.
//
// The client launches this binary as a subprocess and exchanges JSON-RPC
// messages over stdin/stdout. stdout IS the protocol channel, so we only ever
// log to stderr (the log package). Writing to stdout would corrupt the stream.
package main

import (
	"context"
	"flag"
	"log"
	"strings"

	"github.com/modelcontextprotocol/go-sdk/mcp"

	"github.com/pixperk/mcp_toy/internal/handlers"
	"github.com/pixperk/mcp_toy/internal/store"
)

func main() {
	dsn := flag.String("db", "file:mcp_toy.db?_pragma=busy_timeout(5000)",
		"SQLite DSN; use 'file::memory:?cache=shared' for an in-memory store")
	flag.Parse()

	log.SetPrefix("[mcp_toy] ")
	log.SetFlags(0)

	st, err := store.Open(*dsn)
	if err != nil {
		log.Fatalf("opening store: %v", err)
	}
	defer st.Close()

	server := mcp.NewServer(&mcp.Implementation{
		Name:    "mcp_toy",
		Version: "v0.1.0",
	}, nil)

	handlers.New(st).Register(server)

	log.Println("starting mcp_toy server on stdio…")
	if err := server.Run(context.Background(), &mcp.StdioTransport{}); err != nil {
		if strings.Contains(err.Error(), "EOF") {
			log.Println("client disconnected, shutting down")
			return
		}
		log.Fatalf("server error: %v", err)
	}
}
