package github

import "github.com/fondoger/copilotchat/internal/provider"

type Model = provider.Model
type AccountInfo = provider.AccountInfo
type Attachment = provider.Attachment
type ToolCall = provider.ToolCall
type Tool = provider.Tool
type Message = provider.Message
type Request = provider.Request
type Usage = provider.Usage
type StreamEvent = provider.StreamEvent
type Completion = provider.Completion

const EventText = provider.EventText
const EventReasoning = provider.EventReasoning

var requestBody = provider.RequestBody
