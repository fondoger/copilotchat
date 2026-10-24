
- UI/UX: Avoid unnecessary secondary labels and descriptions. Let the layout do the explaining.
- Don't add UI test unless it covers meaningful user-facing behavior.
- Avoid tests tied to trivial logic, UI copy, layout, or implementation details.
- Work on `main` branch by default.
- Don't patch around poor structure. If a new requirement makes existing code, names, or file layout a bad fit, refactor them in the same change; propose a plan first if it's large.

- ./docs: 文档
- ./reference：Claude Code、Codex等Client端，以及本地网关转发层实现的参考代码。