package desktop

import "github.com/fondoger/copilotchat/internal/credentials"

type m365SavedSignIn = credentials.MicrosoftSignIn

var saveM365SignIn = credentials.SaveMicrosoft
var loadM365SignIn = credentials.LoadMicrosoft
var m365CredentialKey = credentials.MicrosoftKey

func (a *app) clearM365Credentials(id string) error {
	if r := a.providers[id]; r != nil && r.m365Auth != nil {
		r.m365Auth.Revoke()
		r.m365Auth = nil
	}
	return credentials.DeleteMicrosoft(a.credentials(), id)
}
