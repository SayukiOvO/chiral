package runtimeprovider

import (
	"context"

	chiralv1 "github.com/SayukiOvO/chiral/proto/chiral/v1"
)

// Relay retrieves one archive chunk from Core. Implementations preserve the
// existing pull protocol: one requested version and offset per reply, without
// occupying the config and user-operation queues with an entire archive.
type Relay interface {
	Chunk(ctx context.Context, version string, offset int64) (data []byte, last bool, err error)
}

// ProgressFunc reports an intermediate phase of the Core-requested upgrade.
type ProgressFunc func(phase chiralv1.XrayInstallPhase, message string)

// Upgrader is an optional capability of a Runtime. Ordinary runtime providers
// need not implement it; those without it reject XrayInstall instructions.
//
// Install must honor Core's exact version, archive checksum and activate flag.
// Staging must not restart the running kernel. Activation must retain a local
// rollback path and use the runtime's configured data-path probe; a running
// process alone does not establish ACTIVE. Implementations must distinguish
// ACTIVE, INCONCLUSIVE, ROLLED_BACK and FAILED, including rollback failure.
//
// The client serializes Install calls and transports relay chunks and progress.
// The provider owns artifact retention, activation and recovery, including any
// cleanup required when ctx is cancelled. It must not return while background
// upgrade work can still change the runtime.
type Upgrader interface {
	Install(ctx context.Context, req *chiralv1.XrayInstall, relay Relay, progress ProgressFunc) (chiralv1.XrayInstallPhase, string)
}
