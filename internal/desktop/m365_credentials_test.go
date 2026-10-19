package desktop

import "github.com/fondoger/copilotchat/internal/credentials"

type secretManifest = credentials.Manifest
type secretGeneration = credentials.Generation

var readSecretManifest = credentials.ReadManifest
var m365ChunkKey = credentials.ChunkKey
var errInvalidMicrosoftManifest = credentials.ErrInvalidMicrosoftManifest
