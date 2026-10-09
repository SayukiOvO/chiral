package runtimeprovider

import "context"

// AcknowledgedReporter is implemented by runtimes with cumulative counters and
// a durable outbox. Sending succeeds only after Core commits a matching receipt;
// the legacy destructive-read Stats path must not consume this outbox.
type AcknowledgedReporter interface {
	StatsBatch(context.Context) (*AccountingBatch, error)
	AcknowledgeStats(reporterID string, sequence uint64) error
}
