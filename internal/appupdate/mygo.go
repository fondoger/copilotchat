package appupdate

import (
	"context"

	"github.com/egoist/mygo"
)

type myGoClient struct{}

// NewMyGoClient uses the GitHub feed and Ed25519 public key embedded by
// `mygo build`. MyGo verifies downloaded bytes before replacing app files.
func NewMyGoClient() Client { return myGoClient{} }

func (myGoClient) Enabled() bool { return mygo.Updater.Enabled() }

func (myGoClient) Check(ctx context.Context) (*Release, error) {
	update, err := mygo.Updater.Check(ctx)
	if err != nil || update == nil {
		return nil, err
	}
	return &Release{Version: update.Version, Notes: update.Notes, Install: update.Install}, nil
}
