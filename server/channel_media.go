package server

import (
	"context"
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
	return saveChannelMediaUploadWithStorage(context.Background(), NewLocalUploadStorage(uploadDir), uploadDir, uploadType, media)
}

func saveChannelMediaUploadWithStorage(ctx context.Context, storage UploadStorage, baseDir, uploadType string, media *channelMediaDownload) (uploadPayload, error) {
	if media == nil || media.Body == nil {
		return uploadPayload{}, errors.New("missing media body")
	}
	defer media.Close()
	return saveChannelMediaUploadFromReaderWithStorage(ctx, storage, baseDir, uploadType, media.FileName, media.ContentType, media.Body)
}

func saveChannelMediaUploadFromReader(uploadType, fileName, contentType string, src io.Reader) (uploadPayload, error) {
	return saveChannelMediaUploadFromReaderWithStorage(context.Background(), NewLocalUploadStorage(uploadDir), uploadDir, uploadType, fileName, contentType, src)
}

func saveChannelMediaUploadFromReaderWithStorage(ctx context.Context, storage UploadStorage, baseDir, uploadType, fileName, contentType string, src io.Reader) (uploadPayload, error) {
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
	if err := os.MkdirAll(uploadStagingDir(), 0700); err != nil {
		return uploadPayload{}, err
	}
	temp, err := os.CreateTemp(uploadStagingDir(), "channel-media-*")
	if err != nil {
		return uploadPayload{}, err
	}
	tempPath := temp.Name()
	defer os.Remove(tempPath)
	written, err := io.Copy(temp, io.LimitReader(src, maxSize+1))
	if err != nil {
		_ = temp.Close()
		return uploadPayload{}, err
	}
	if written > maxSize {
		_ = temp.Close()
		return uploadPayload{}, fmt.Errorf("file too large; maximum supported size is %dMB", maxUploadSizeMB)
	}
	storedExt, mimeType := normalizedUploadMetadata(ext, contentType, temp)
	if (uploadType == "image" && !allowedImageExts[storedExt]) || (uploadType != "image" && !allowedFileExts[storedExt]) {
		_ = temp.Close()
		return uploadPayload{}, errors.New("file type not allowed")
	}
	if err := temp.Sync(); err != nil {
		_ = temp.Close()
		return uploadPayload{}, err
	}
	if err := temp.Close(); err != nil {
		return uploadPayload{}, err
	}

	fileKey := generateFileKey(storedExt)
	storedSize, _, err := persistStagedUpload(ctx, storage, "uploads/"+subDir+"/"+fileKey, tempPath, mimeType)
	if err != nil {
		return uploadPayload{}, err
	}
	return uploadPayload{
		FileKey:  fileKey,
		URL:      fmt.Sprintf("/uploads/%s/%s", subDir, fileKey),
		Name:     fileName,
		Size:     storedSize,
		Type:     uploadType,
		MimeType: mimeType,
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
	case "video/ogg":
		return ".ogv"
	case "audio/ogg", "application/ogg":
		return ".ogg"
	case "audio/mpeg", "audio/mp3":
		return ".mp3"
	case "audio/wav", "audio/x-wav":
		return ".wav"
	case "audio/amr", "audio/amr-wb":
		return ".amr"
	case "audio/opus":
		return ".opus"
	case "audio/aac", "audio/x-aac":
		return ".aac"
	case "audio/mp4", "audio/x-m4a":
		return ".m4a"
	case "audio/silk":
		return ".silk"
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
