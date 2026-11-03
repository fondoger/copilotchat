package doubao

import (
	"encoding/base64"
	"encoding/binary"
	"testing"
)

func TestWebPImageDimensionsAndContainerLimit(t *testing.T) {
	for _, tc := range []struct {
		kind string
		data []byte
	}{
		{"VP8X", []byte{0, 0, 0, 0, 12, 0, 0, 6, 0, 0}},
		{"VP8L", []byte{0x2f, 12, 0x80, 1, 0}},
		{"VP8 ", []byte{0, 0, 0, 0x9d, 1, 0x2a, 13, 0, 7, 0}},
	} {
		t.Run(tc.kind, func(t *testing.T) {
			data := append([]byte("RIFF"), make([]byte, 4)...)
			data = append(data, []byte("WEBP"+tc.kind)...)
			data = binary.LittleEndian.AppendUint32(data, uint32(len(tc.data)))
			data = append(data, tc.data...)
			if len(tc.data)%2 != 0 {
				data = append(data, 0)
			}
			binary.LittleEndian.PutUint32(data[4:8], uint32(len(data)-8))
			image, err := parseImage("data:image/webp;base64,"+base64.StdEncoding.EncodeToString(data), 1)
			if err != nil || image.width != 13 || image.height != 7 {
				t.Fatalf("dimensions=%dx%d, err=%v", image.width, image.height, err)
			}
			for _, invalid := range [][]byte{data[:len(data)-1], append(append([]byte(nil), data...), 0)} {
				if _, err := parseImage("data:image/webp;base64,"+base64.StdEncoding.EncodeToString(invalid), 1); err == nil {
					t.Fatal("mismatched container size accepted")
				}
			}
		})
	}
}
