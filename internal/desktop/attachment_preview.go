package desktop

import (
	"bytes"
	"crypto/sha256"
	"encoding/base64"
	"fmt"
	"image"
	"os"
	"path/filepath"
	"strings"

	"github.com/egoist/mygo"
	"github.com/egoist/mygo/ui"
	"github.com/fondoger/copilotchat/internal/store"
	"golang.org/x/image/draw"
)

type attachmentThumbnail struct {
	data   string
	bitmap *ui.Bitmap
}

func imageAttachmentData(f store.Attachment) ([]byte, string, error) {
	data, err := base64.StdEncoding.DecodeString(f.Data)
	if err != nil {
		return nil, "", fmt.Errorf("decode image: %w", err)
	}
	config, format, err := image.DecodeConfig(bytes.NewReader(data))
	if err != nil {
		return nil, "", fmt.Errorf("read image: %w", err)
	}
	if config.Width <= 0 || config.Height <= 0 || int64(config.Width)*int64(config.Height) > 50_000_000 {
		return nil, "", fmt.Errorf("image dimensions exceed the preview limit")
	}
	switch format {
	case "png", "jpeg", "gif", "webp", "bmp":
		return data, format, nil
	default:
		return nil, "", fmt.Errorf("unsupported image format")
	}
}

func makeAttachmentThumbnail(f store.Attachment) *ui.Bitmap {
	data, _, err := imageAttachmentData(f)
	if err != nil {
		return nil
	}
	src, _, err := image.Decode(bytes.NewReader(data))
	if err != nil {
		return nil
	}
	bounds := src.Bounds()
	w, h := bounds.Dx(), bounds.Dy()
	if w > 256 || h > 256 {
		if w >= h {
			h, w = max(1, h*256/w), 256
		} else {
			w, h = max(1, w*256/h), 256
		}
	}
	thumb := image.NewRGBA(image.Rect(0, 0, w, h))
	draw.CatmullRom.Scale(thumb, thumb.Bounds(), src, bounds, draw.Src, nil)
	return ui.NewBitmap(thumb)
}

func writeAttachmentPreview(dir string, f store.Attachment) (string, error) {
	data, format, err := imageAttachmentData(f)
	if err != nil {
		return "", err
	}
	// Never use an attachment's filename as an executable path or extension.
	name := fmt.Sprintf("%x.%s", sha256.Sum256(data), format)
	dir = filepath.Join(dir, "image-previews")
	if err = os.MkdirAll(dir, 0700); err != nil {
		return "", err
	}
	path := filepath.Join(dir, name)
	if err = os.WriteFile(path, data, 0600); err != nil {
		return "", err
	}
	return path, nil
}

func (a *app) openAttachmentImage(f store.Attachment) {
	path, err := writeAttachmentPreview(a.dataDir, f)
	if err == nil {
		err = mygo.Shell.OpenPath(path)
	}
	if err != nil {
		a.status = "Could not open image: " + err.Error()
	}
}

func (a *app) attachmentImage(c *ui.Context, f store.Attachment, size float32) {
	b := ui.ButtonBase(c).Label("Open image "+f.Name).Tooltip(f.Name+" · Open original").Size(size, size).Padding(0).Radius(8).Clip().Background(c.Theme().SurfaceHover).Border(1, c.Theme().Border)
	thumb := ui.Local(b, "thumbnail", func() attachmentThumbnail { return attachmentThumbnail{} })
	if thumb.data != f.Data {
		thumb.data, thumb.bitmap = f.Data, makeAttachmentThumbnail(f)
	}
	b.Children(func() {
		if thumb.bitmap != nil {
			ui.Image(c, thumb.bitmap).Fill().Fit(ui.Cover)
		} else {
			ui.Icon(c, iAttach).Size(20, 20)
		}
	}).OnClick(func() { a.openAttachmentImage(f) })
}

func (a *app) messageAttachments(c *ui.Context, attachments []store.Attachment) {
	if len(attachments) == 0 {
		return
	}
	ui.Row(c).Wrap().Justify(ui.End).Gap(6).Children(func() {
		for i, f := range attachments {
			key := c.Key(fmt.Sprintf("attachment:%d:%s", i, f.Name))
			if strings.HasPrefix(f.MIME, "image/") {
				a.attachmentImage(key, f, 80)
			} else {
				ui.Text(key, "↳ "+f.Name).FontSize(12).TextColor(c.Theme().TextMuted)
			}
		}
	})
}

func (a *app) composerAttachments(c *ui.Context) {
	if len(a.attachments) == 0 {
		return
	}
	ui.Row(c).Wrap().Gap(6).Padding(8, 12, 0, 12).Children(func() {
		for i, f := range a.attachments {
			ui.Row(c.Key(fmt.Sprintf("%d:%s", i, f.Name))).Gap(6).Padding(4).Radius(8).Background(c.Theme().SurfaceHover).Children(func() {
				if strings.HasPrefix(f.MIME, "image/") {
					a.attachmentImage(c, f, 44)
				}
				ui.Column(c).MaxWidth(170).Gap(2).Children(func() {
					ui.Text(c, f.Name).SingleLine().FontSize(11)
					ui.Text(c, fileSize(f.Size)).FontSize(10).TextColor(c.Theme().TextMuted)
				})
				messageActionButton(c, iClose, "Remove "+f.Name).OnClick(func() { a.attachments = append(a.attachments[:i], a.attachments[i+1:]...) })
			})
		}
	})
}
