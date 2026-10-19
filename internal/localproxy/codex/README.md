# Codex catalog instructions

`prompt.md` is the generic fallback prompt from [openai/codex](https://github.com/openai/codex/blob/bb8f5304fcd9f901d8e0747fccb8c8904cbc504e/codex-rs/models-manager/prompt.md), commit `bb8f5304fcd9f901d8e0747fccb8c8904cbc504e`.

Codex-native model catalogs require an instruction template. Copilot Chat supplies this upstream fallback (with trailing whitespace normalized) rather than inventing model-specific instructions. Inference requests still preserve the client's instructions. The file is distributed under Apache-2.0; see `resources/licenses/codex/LICENSE` and `resources/licenses/codex/NOTICE` in the repository and packaged licenses.
