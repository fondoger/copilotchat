package appupdate

import (
	"context"

	"github.com/egoist/mygo"
)

type myGoClient struct{ prereleases bool }

// NewMyGoClient uses the GitHub feed and Ed25519 public key embedded by
// `mygo build`. MyGo verifies downloaded bytes before replacing app files.
func NewMyGoClient(channel string) Client { return myGoClient{prereleases: channel == "dev"} }

func (myGoClient) Enabled() bool { return mygo.Updater.Enabled() }

func (c myGoClient) Check(ctx context.Context) (*Release, error) {
	previous := mygo.Updater.Prereleases()
	mygo.Updater.SetPrereleases(c.prereleases)
	defer mygo.Updater.SetPrereleases(previous)
	update, err := mygo.Updater.Check(ctx)
	if err != nil || update == nil {
		return nil, err
	}
	return &Release{Version: update.Version, Notes: update.Notes, Install: update.Install}, nil
}
