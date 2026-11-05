.PHONY: dev publish-stable

dev:
	pwsh -NoProfile -File scripts/dev.ps1

publish-stable:
	$(if $(filter main,$(shell git branch --show-current)),,$(error publish-stable requires the main branch))
	gh auth switch --hostname github.com --user fondoger
	git pull --rebase --no-autostash origin main && git push origin main && gh workflow run release.yml --repo fondoger/copilotchat --ref main -f channel=stable
