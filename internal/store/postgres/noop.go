// Package postgres — exported for use in server.BankEntry.
// The Store type is the concrete implementation.
package postgres

// Ensure the package is importable even without a live DB connection.
// All actual functionality is in postgres.go.
