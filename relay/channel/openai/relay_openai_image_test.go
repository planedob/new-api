package openai

import (
	"bytes"
	"mime/multipart"
	"testing"
)

func TestDetectImageMimeTypeFromFileUsesBytesNotFilename(t *testing.T) {
	var body bytes.Buffer
	writer := multipart.NewWriter(&body)
	part, err := writer.CreateFormFile("image", "reference.jpg")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := part.Write([]byte("\x89PNG\r\n\x1a\n\x00\x00\x00\rIHDR")); err != nil {
		t.Fatal(err)
	}
	if err := writer.Close(); err != nil {
		t.Fatal(err)
	}

	form, err := multipart.NewReader(bytes.NewReader(body.Bytes()), writer.Boundary()).ReadForm(1 << 20)
	if err != nil {
		t.Fatal(err)
	}
	defer form.RemoveAll()
	file, err := form.File["image"][0].Open()
	if err != nil {
		t.Fatal(err)
	}
	defer file.Close()

	mimeType, err := detectImageMimeTypeFromFile(file)
	if err != nil {
		t.Fatal(err)
	}
	if mimeType != "image/png" {
		t.Fatalf("detected MIME = %q, want image/png", mimeType)
	}
	firstByte := make([]byte, 1)
	if _, err := file.Read(firstByte); err != nil {
		t.Fatal(err)
	}
	if firstByte[0] != 0x89 {
		t.Fatalf("file was not rewound; first byte = %#x", firstByte[0])
	}
}
