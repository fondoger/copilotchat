// Package webtransport contains the cancellable byte bridge shared by web sessions.
package webtransport

import (
	"context"
	"encoding/base64"
	"io"
	"sync"
)

type Chunk struct {
	Done   bool   `json:"done"`
	Data   string `json:"data"`
	Failed bool   `json:"failed"`
}

type body struct {
	*io.PipeReader
	cancel context.CancelFunc
	once   sync.Once
}

func (b *body) Close() error { b.once.Do(b.cancel); return b.PipeReader.Close() }

func NewBody(ctx context.Context, read func(context.Context) (Chunk, error), failure func(context.Context) error) io.ReadCloser {
	life, cancel := context.WithCancel(ctx)
	reader, writer := io.Pipe()
	go func() {
		defer cancel()
		defer writer.Close()
		for {
			part, err := read(life)
			if err != nil || part.Failed {
				_ = writer.CloseWithError(failure(life))
				return
			}
			data, err := base64.StdEncoding.DecodeString(part.Data)
			if err != nil {
				_ = writer.CloseWithError(failure(life))
				return
			}
			if len(data) > 0 {
				if _, err = writer.Write(data); err != nil {
					return
				}
			}
			if part.Done {
				return
			}
		}
	}()
	go func() { <-life.Done(); _ = writer.CloseWithError(life.Err()) }()
	return &body{PipeReader: reader, cancel: cancel}
}
