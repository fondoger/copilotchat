package doubao

import (
	"bytes"
	"context"
	"encoding/base64"
	"encoding/binary"
	"encoding/json"
	"errors"
	"fmt"
	"image"
	_ "image/jpeg"
	_ "image/png"
	"io"
	"mime"
	"mime/multipart"
	"net/http"
	"net/textproto"
	"net/url"
	"strings"

	"github.com/fondoger/copilotchat/internal/provider"
	_ "golang.org/x/image/webp"
)

const maxImage = 5 << 20

type inputImage struct {
	data          []byte
	name, format  string
	width, height int
}

func parseImage(dataURL string, index int) (inputImage, error) {
	header, encoded, ok := strings.Cut(dataURL, ",")
	if !ok || (header != "data:image/png;base64" && header != "data:image/jpeg;base64" && header != "data:image/webp;base64") {
		return inputImage{}, errors.New("Doubao requires base64 PNG, JPEG, or WebP images; remote URLs are not supported")
	}
	if len(encoded) > (maxImage+2)/3*4 {
		return inputImage{}, errors.New("Doubao image exceeds 5 MiB")
	}
	data, err := base64.StdEncoding.DecodeString(encoded)
	if err != nil || len(data) == 0 || len(data) > maxImage {
		return inputImage{}, errors.New("invalid or oversized Doubao image data")
	}
	format := strings.TrimSuffix(strings.TrimPrefix(header, "data:image/"), ";base64")
	if format == "webp" && (len(data) < 12 || uint64(binary.LittleEndian.Uint32(data[4:8]))+8 != uint64(len(data))) {
		return inputImage{}, errors.New("invalid WebP container size")
	}
	config, actual, err := image.DecodeConfig(bytes.NewReader(data))
	if err != nil || actual != format {
		return inputImage{}, errors.New("Doubao image data does not match its MIME type")
	}
	width, height := config.Width, config.Height
	// Decode only headers, never full pixels. Reject implausible dimensions too.
	if width <= 0 || height <= 0 || width > 16384 || height > 16384 || int64(width)*int64(height) > 40_000_000 {
		return inputImage{}, errors.New("invalid or oversized Doubao image dimensions")
	}
	return inputImage{data: data, format: format, name: fmt.Sprintf("image-%d.%s", index, format), width: width, height: height}, nil
}

func uploadImage(ctx context.Context, life *sessionLife, image inputImage) (map[string]any, error) {
	var body bytes.Buffer
	writer := multipart.NewWriter(&body)
	header := make(textproto.MIMEHeader)
	header.Set("Content-Disposition", mime.FormatMediaType("form-data", map[string]string{"name": "data", "filename": image.name}))
	header.Set("Content-Type", "image/"+image.format)
	part, err := writer.CreatePart(header)
	if err != nil {
		return nil, protocolError()
	}
	if _, err = part.Write(image.data); err != nil {
		return nil, protocolError()
	}
	if writer.WriteField("file_type", image.format) != nil || writer.Close() != nil {
		return nil, protocolError()
	}
	var uploaded struct {
		URI string `json:"uri"`
	}
	if err := doJSON(ctx, life, "/samantha/pages/upload_image", writer.FormDataContentType(), body.Bytes(), &uploaded); err != nil {
		return nil, err
	}
	if uploaded.URI == "" {
		return nil, protocolError()
	}
	request, _ := json.Marshal(map[string]any{"uris": []string{uploaded.URI}, "type": "image", "format": image.format, "expire_second": 3600})
	var resolved struct {
		URLs []struct {
			URI     string `json:"uri"`
			MainURL string `json:"main_url"`
			URL     string `json:"url"`
		} `json:"file_urls"`
	}
	if err := doJSON(ctx, life, "/alice/message/get_file_url", "application/json", request, &resolved); err != nil {
		return nil, err
	}
	if len(resolved.URLs) == 0 {
		return nil, protocolError()
	}
	info := resolved.URLs[0]
	cdn := info.MainURL
	if cdn == "" {
		cdn = info.URL
	}
	u, err := url.Parse(cdn)
	if err != nil || u.Scheme != "https" || u.Hostname() == "" || u.User != nil {
		return nil, protocolError()
	}
	uri := info.URI
	if uri == "" {
		uri = uploaded.URI
	}
	return map[string]any{
		"type": 1, "identifier": provider.RequestID(), "parse_state": 0, "review_state": 0,
		"image": map[string]any{"name": image.name, "uri": uri,
			"image_ori": map[string]any{"url": cdn, "width": image.width, "height": image.height, "format": image.format, "url_formats": map[string]any{}}},
	}, nil
}

const maxJSON = 1 << 20

func doJSON(ctx context.Context, life *sessionLife, path, contentType string, body []byte, target any) error {
	resp, err := life.do(path, contentType, body)
	if err != nil {
		return err
	}
	defer life.closeBody()
	data, err := io.ReadAll(io.LimitReader(resp.Body, maxJSON+1))
	if err != nil {
		return transportError(ctx, err)
	}
	if resp.StatusCode < http.StatusOK || resp.StatusCode >= 300 || len(data) > maxJSON || payloadError(data) {
		return responseError(ctx, resp.StatusCode, data)
	}
	var envelope struct {
		Code json.RawMessage `json:"code"`
		Data json.RawMessage `json:"data"`
	}
	if json.Unmarshal(data, &envelope) != nil || len(envelope.Code) == 0 || (string(envelope.Code) != "0" && string(envelope.Code) != `"0"`) || len(envelope.Data) == 0 || string(envelope.Data) == "null" || json.Unmarshal(envelope.Data, target) != nil {
		return protocolError()
	}
	return nil
}
