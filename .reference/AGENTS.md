.reference/目录用于存放临时参考的代码，已经添加了全局gitignore`**/*`。

参考代码默认不放到repo，如本地无副本可按需克隆(depth=1)

4个Agent参考实现:
- [claude-code](github.com/tanbiralam/claude-code), 26年3月泄露的源代码
- [codex](github.com/openai/codex)
- [pi](github.com/earendil-works/pi)，同时兼容多种协议
- [opencode](github.com/anomalyco/opencode)，同时兼容多种协议

2个本地网关转发层实现，支持Github Copilot、OpenAI订阅、Anthropic订阅等多种代理转发：
- [cc-switch](github.com/farion1231/cc-switch): 
- [magpie](github.com/yetone/magpie)
