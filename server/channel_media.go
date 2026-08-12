package server

import (
	"errors"
	"fmt"
	"io"
	"mime"
	"os"
	"path/filepath"
	"strings"
)

type channelMediaDownload struct {
	Body        io.ReadCloser
	FileName    string
	ContentType string
}

func (m *channelMediaDownload) Close() error {
	if m == nil || m.Body == nil {
		return nil
	}
	return m.Body.Close()
}

func saveChannelMediaUpload(uploadType string, media *channelMediaDownload) (uploadPayload, error) {
	if media == nil || media.Body == nil {
		return uploadPayload{}, errors.New("missing media body")
	}
	defer media.Close()
	return saveChannelMediaUploadFromReader(uploadType, media.FileName, media.ContentType, media.Body)
}

func saveChannelMediaUploadFromReader(uploadType, fileName, contentType string, src io.Reader) (uploadPayload, error) {
	uploadType = normalizeChannelMediaUploadType(uploadType)
	if src == nil {
		return uploadPayload{}, errors.New("missing media body")
	}
	fileName = sanitizeChannelMediaFileName(fileName)
	ext := strings.ToLower(filepath.Ext(fileName))
	if ext == "" {
		ext = inferChannelMediaExt(uploadType, contentType)
		if ext == "" {
			return uploadPayload{}, errors.New("missing media file extension")
		}
		fileName += ext
	}

	if uploadType == "image" {
		if !allowedImageExts[ext] {
			return uploadPayload{}, errors.New("invalid image type")
		}
		if mediaType, _, err := mime.ParseMediaType(contentType); err == nil && mediaType != "" && mediaType != "application/octet-stream" && !isAllowedImageContentType(mediaType) {
			return uploadPayload{}, errors.New("invalid image content type")
		}
	} else if !allowedFileExts[ext] {
		return uploadPayload{}, errors.New("file type not allowed")
	}

	subDir := "files"
	maxSize := int64(maxFileSize)
	if uploadType == "image" {
		subDir = "images"
		maxSize = int64(maxImageSize)
	}
	if err := os.MkdirAll(filepath.Join(uploadDir, subDir), 0755); err != nil {
		return uploadPayload{}, err
	}
	fileKey := generateFileKey(ext)
	dstPath := filepath.Join(uploadDir, subDir, fileKey)
	dst, err := os.Create(dstPath)
	if err != nil {
		return uploadPayload{}, err
	}
	limited := &io.LimitedReader{R: src, N: maxSize + 1}
	written, copyErr := io.Copy(dst, limited)
	closeErr := dst.Close()
	if copyErr != nil {
		_ = os.Remove(dstPath)
		return uploadPayload{}, copyErr
	}
	if closeErr != nil {
		_ = os.Remove(dstPath)
		return uploadPayload{}, closeErr
	}
	if written > maxSize {
		_ = os.Remove(dstPath)
		return uploadPayload{}, fmt.Errorf("file too large; maximum supported size is %dMB", maxUploadSizeMB)
	}
	return uploadPayload{
		FileKey:  fileKey,
		URL:      fmt.Sprintf("/uploads/%s/%s", subDir, fileKey),
		Name:     fileName,
		Size:     written,
		Type:     uploadType,
		MimeType: normalizedUploadMimeType(ext, contentType),
	}, nil
}

func normalizeChannelMediaUploadType(uploadType string) string {
	if strings.EqualFold(strings.TrimSpace(uploadType), "image") {
		return "image"
	}
	return "file"
}

func sanitizeChannelMediaFileName(fileName string) string {
	fileName = strings.TrimSpace(strings.ReplaceAll(fileName, "\\", "/"))
	fileName = filepath.Base(fileName)
	if fileName == "" || fileName == "." || fileName == string(filepath.Separator) {
		return "channel-upload"
	}
	return fileName
}

func inferChannelMediaExt(uploadType, contentType string) string {
	mediaType := ""
	if parsed, _, err := mime.ParseMediaType(contentType); err == nil {
		mediaType = strings.ToLower(parsed)
	}
	switch mediaType {
	case "image/jpeg":
		return ".jpg"
	case "image/png":
		return ".png"
	case "image/gif":
		return ".gif"
	case "image/webp":
		return ".webp"
	case "application/pdf":
		return ".pdf"
	case "text/plain":
		return ".txt"
	case "text/csv":
		return ".csv"
	case "application/json":
		return ".json"
	case "application/xml", "text/xml":
		return ".xml"
	case "application/zip":
		return ".zip"
	case "audio/ogg", "application/ogg":
		return ".ogg"
	case "audio/mpeg", "audio/mp3":
		return ".mp3"
	case "audio/wav", "audio/x-wav":
		return ".wav"
	}
	if uploadType == "image" {
		return ".jpg"
	}
	return ""
}

func channelMediaFileNameFromDisposition(disposition string) string {
	_, params, err := mime.ParseMediaType(disposition)
	if err != nil {
		return ""
	}
	if filename := strings.TrimSpace(params["filename"]); filename != "" {
		return filename
	}
	return strings.TrimSpace(params["filename*"])
}

func channelMediaDisplaySummary(files []uploadPayload) string {
	if len(files) == 0 {
		return ""
	}
	if len(files) == 1 {
		if files[0].Type == "image" {
			return "[图片] " + files[0].Name
		}
		return "[文件] " + files[0].Name
	}
	return fmt.Sprintf("[附件] %d 个文件", len(files))
}
